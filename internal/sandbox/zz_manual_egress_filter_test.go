package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// TestManualEgressFiltering is a throwaway real-machine check that the
// REDIRECT rule + masuda-egress-proxy actually intercept a VM guest's
// outbound TLS traffic and enforce the declare/approve allowlist
// end to end (Issue #11 M3-M5): a hostname the test repo both declares
// (.masuda/settings.json) and approves (.masuda/settings.local.json, what
// `masuda egress approve` would write) must be reachable; one that's
// neither declared nor approved must still be denied.
func TestManualEgressFiltering(t *testing.T) {
	if os.Getenv("MASUDA_MANUAL_VM_TEST") == "" {
		t.Skip("set MASUDA_MANUAL_VM_TEST=1 to run this real-machine VM test")
	}
	exe := buildMasudaForTest(t)
	resolveMasudaExe = func() (string, error) { return exe, nil }

	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, config.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.SettingsPath(repoRoot), []byte(`{"egressAllowlist": ["example.com"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveLocal(repoRoot, config.LocalSettings{EgressAllowlist: []string{"example.com"}}); err != nil {
		t.Fatal(err)
	}

	id := "manvm1"
	if _, err := workspace.Create(repoRoot, id, "manual-test-branch", "develop", ""); err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Remove(id) })

	worktreeDir := t.TempDir()
	stateDir := t.TempDir()
	var backend Backend = VMBackend{}
	t.Cleanup(func() { _ = backend.Stop(id) })

	if _, err := backend.Start(id, worktreeDir, stateDir, repoRoot, "masuda-loop:latest"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(3 * time.Second)
	guestIP, err := LookupGuestIP(MACFor(id), vmDHCPLeaseFile, 5*time.Second)
	if err != nil {
		t.Fatalf("LookupGuestIP: %v", err)
	}
	privKeyPath, _, err := SSHKeyPaths()
	if err != nil {
		t.Fatalf("SSHKeyPaths: %v", err)
	}
	run := func(remoteCmd string) (string, error) {
		args := append(sshBaseArgs(guestIP, privKeyPath), remoteCmd)
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		return string(out), err
	}

	// DNS resolution itself is never blocked (only the TLS connection
	// is), so this should succeed regardless of the allowlist.
	out, err := run("getent hosts example.com")
	t.Logf("DNS resolution for example.com:\n%s (err: %v)", out, err)

	// example.com is both declared and approved above -- must succeed.
	out, err = run("curl -4 -sS -m 8 -o /dev/null -w '%{http_code}' https://example.com/")
	t.Logf("curl -4 https://example.com/ (allowlisted) output=%q err=%v", out, err)
	if err != nil {
		t.Errorf("curl to an allowlisted host failed (output %q, err %v), want it allowed by the egress proxy", out, err)
	}

	// iana.org is neither declared nor approved -- must still be denied.
	out, err = run("curl -4 -sS -m 8 -o /dev/null -w '%{http_code}' https://www.iana.org/")
	t.Logf("curl -4 https://www.iana.org/ (not allowlisted) output=%q err=%v", out, err)
	if err == nil {
		t.Errorf("curl to a non-allowlisted host succeeded (output %q), want it denied by the egress proxy", out)
	}
}
