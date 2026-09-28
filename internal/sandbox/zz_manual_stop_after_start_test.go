package sandbox

import (
	"os"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// TestManualStopRightAfterStartIsGraceful: a Stop issued the moment Start
// returns has to power the guest off over SSH, not fall back to killing the
// VMM (which can corrupt the disk image). The fallback only runs after
// waiting out the whole graceful-shutdown timeout for the VMM to exit on its
// own, so a Stop that finishes well inside that timeout took the graceful
// path.
//
// Start and Stop run in this one process here, so the exited VMM stays an
// unreaped child; this is what caught Shutdown mistaking that zombie for a
// running VMM. It does not reliably catch Start returning before the guest's
// sshd is up: the guest's sshd is socket-activated, so an early connection
// usually waits rather than being refused, and the refusal Start now guards
// against only shows up when the lease beats the socket.
func TestManualStopRightAfterStartIsGraceful(t *testing.T) {
	if os.Getenv("MASUDA_MANUAL_VM_TEST") == "" {
		t.Skip("set MASUDA_MANUAL_VM_TEST=1 to run this real-machine VM test")
	}
	exe := buildMasudaForTest(t)
	originalResolve := resolveMasudaExe
	resolveMasudaExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { resolveMasudaExe = originalResolve })

	repoRoot := t.TempDir()
	writeImageEntry(t, repoRoot, config.DefaultImageEntry,
		"FROM masuda-loop:latest\nUSER root\nRUN rm -f /etc/systemd/system/multi-user.target.wants/masuda-loop.service\n")
	buildImageEntry(t, repoRoot, config.DefaultImageEntry)

	id := "manvm5"
	if _, err := workspace.Create(repoRoot, id, "manual-test-branch", "develop", ""); err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Remove(id) })
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		t.Fatal(err)
	}
	var backend Backend = VMBackend{}
	t.Cleanup(func() { _ = backend.Stop(id) })
	if _, err := backend.Start(id, t.TempDir(), stateDir, repoRoot, config.DefaultImageEntry); err != nil {
		t.Fatalf("Start: %v", err)
	}

	begin := time.Now()
	if err := backend.Stop(id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// 15s is microvm's graceful-shutdown timeout; a guest that powers off
	// over SSH is gone in a few seconds.
	if took := time.Since(begin); took >= 12*time.Second {
		t.Errorf("Stop right after Start took %s, want it to finish by graceful poweroff rather than waiting out the timeout and killing the VMM", took)
	}
}
