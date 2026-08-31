package sandbox

import (
	"os/exec"
	"strings"
	"testing"
)

// requireNetHelper skips the test unless masuda-net-helper is on PATH and
// actually carries CAP_NET_ADMIN. Skipping (not failing) keeps `go test
// ./...` usable without the one-time `sudo setcap` step -- masuda has no CI
// job that runs `go test` (release.yml only builds/signs binaries on tag
// push), so this is purely a local developer convenience.
func requireNetHelper(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(netHelperBinary); err != nil {
		t.Skipf("%s not installed", netHelperBinary)
	}
	if _, err := exec.LookPath("bridge"); err != nil {
		t.Skip("bridge (iproute2) not installed")
	}
}

// requireTestBridge skips unless a bridge named name already exists --
// EnsureTap attaches to an existing bridge, it doesn't create one (the
// bridge itself is shared, host-level, one-time setup; see
// docs/INSTALLATION.md). Rather than creating/tearing down a throwaway
// bridge (itself a CAP_NET_ADMIN operation this test has no privileged way
// to do), this test relies on the same br-masuda0 a developer following the
// VM setup instructions already has.
func requireTestBridge(t *testing.T, name string) {
	t.Helper()
	if err := exec.Command("ip", "link", "show", name).Run(); err != nil {
		t.Skipf("bridge %s not found (see docs/INSTALLATION.md's VM network setup)", name)
	}
}

const testBridge = "br-masuda0"

func TestEnsureTapAndReleaseTap(t *testing.T) {
	requireNetHelper(t)
	requireTestBridge(t, testBridge)

	id := "vmnettest01"
	t.Cleanup(func() { _ = ReleaseTap(id) })

	name, err := EnsureTap(id, testBridge, "ubuntu")
	if err != nil {
		t.Fatalf("EnsureTap() error = %v", err)
	}
	if want := TapName(id); name != want {
		t.Errorf("EnsureTap() name = %q, want %q", name, want)
	}
	if err := exec.Command("ip", "link", "show", name).Run(); err != nil {
		t.Fatalf("tap %s not present after EnsureTap(): %v", name, err)
	}

	// EnsureTap must be safe to call again for the same id -- this is the
	// "stale reclaim" path (a crashed previous run's leftover tap), not
	// just idempotent allocation.
	if _, err := EnsureTap(id, testBridge, "ubuntu"); err != nil {
		t.Fatalf("second EnsureTap() error = %v", err)
	}

	if err := ReleaseTap(id); err != nil {
		t.Fatalf("ReleaseTap() error = %v", err)
	}
	if err := exec.Command("ip", "link", "show", name).Run(); err == nil {
		t.Errorf("tap %s still present after ReleaseTap()", name)
	}

	// Not an error to release something already gone.
	if err := ReleaseTap(id); err != nil {
		t.Errorf("second ReleaseTap() error = %v, want nil", err)
	}
}

// TestEnsureTapNamesTheFixWhenTheBridgeIsGone covers the case a reboot (or,
// on WSL2, an idle shutdown) puts every developer in: the bridge that
// scripts/setup-vm-host.sh created is kernel runtime state and is simply
// gone. Needs no privileges and no setup, unlike the test above -- the
// point is precisely that nothing is there.
func TestEnsureTapNamesTheFixWhenTheBridgeIsGone(t *testing.T) {
	_, err := EnsureTap("vmnettest02", "br-masuda-does-not-exist", "ubuntu")
	if err == nil {
		t.Fatal("EnsureTap() error = nil for a bridge that does not exist, want an error")
	}
	// The message has to carry the fix, not just the fact: a bare
	// "no such device" from masuda-net-helper reads as a masuda bug rather
	// than as "the host setup is gone" (Issue #40).
	for _, want := range []string{"br-masuda-does-not-exist", "systemctl restart masuda-vm-host", "setup-vm-host.sh"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("EnsureTap() error = %q, want it to mention %q", err, want)
		}
	}
}
