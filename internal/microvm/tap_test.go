package microvm

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
	if _, err := exec.LookPath(testNetHelper); err != nil {
		t.Skipf("%s not installed", testNetHelper)
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

// The real host setup's names: these tests exercise it rather than a
// throwaway bridge (see requireTestBridge).
const (
	testBridge    = "br-masuda0"
	testNetHelper = "masuda-net-helper"
)

func TestEnsureTapAndReleaseTap(t *testing.T) {
	requireNetHelper(t)
	requireTestBridge(t, testBridge)

	h := Host{Bridge: testBridge, NetHelper: testNetHelper}
	id := "vmnettest01"
	t.Cleanup(func() { _ = h.releaseTap(id) })

	name, err := h.ensureTap(id, "ubuntu")
	if err != nil {
		t.Fatalf("ensureTap() error = %v", err)
	}
	if want := TapName(id); name != want {
		t.Errorf("ensureTap() name = %q, want %q", name, want)
	}
	if err := exec.Command("ip", "link", "show", name).Run(); err != nil {
		t.Fatalf("tap %s not present after ensureTap(): %v", name, err)
	}

	// EnsureTap must be safe to call again for the same id -- this is the
	// "stale reclaim" path (a crashed previous run's leftover tap), not
	// just idempotent allocation.
	if _, err := h.ensureTap(id, "ubuntu"); err != nil {
		t.Fatalf("second ensureTap() error = %v", err)
	}

	if err := h.releaseTap(id); err != nil {
		t.Fatalf("releaseTap() error = %v", err)
	}
	if err := exec.Command("ip", "link", "show", name).Run(); err == nil {
		t.Errorf("tap %s still present after releaseTap()", name)
	}

	// Not an error to release something already gone.
	if err := h.releaseTap(id); err != nil {
		t.Errorf("second releaseTap() error = %v, want nil", err)
	}
}

// TestEnsureTapNamesTheFixWhenTheBridgeIsGone covers the case a reboot (or,
// on WSL2, an idle shutdown) puts every developer in: the bridge that
// scripts/setup-vm-host.sh created is kernel runtime state and is simply
// gone. Needs no privileges and no setup, unlike the test above -- the
// point is precisely that nothing is there.
func TestEnsureTapNamesTheFixWhenTheBridgeIsGone(t *testing.T) {
	h := Host{Bridge: "br-masuda-does-not-exist", NetHelper: testNetHelper, BridgeMissingHint: "  run the fix"}
	_, err := h.ensureTap("vmnettest02", "ubuntu")
	if err == nil {
		t.Fatal("ensureTap() error = nil for a bridge that does not exist, want an error")
	}
	// The message has to carry the fix, not just the fact: a bare
	// "no such device" from the net helper reads as a bug rather than as
	// "the host setup is gone" (Issue #40).
	for _, want := range []string{"br-masuda-does-not-exist", "run the fix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ensureTap() error = %q, want it to mention %q", err, want)
		}
	}
}
