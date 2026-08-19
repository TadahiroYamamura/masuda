package sandbox

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestManualEgressFiltering is a throwaway real-machine check that the
// REDIRECT rule + masuda-egress-proxy actually intercept a VM guest's
// outbound TLS traffic (Issue #11 M3). M4 (the allowlist declare/approve
// mechanism) isn't implemented yet -- resolveEgressAllowlist is still a
// placeholder returning an empty list -- so every connection should be
// denied right now; that's the thing this test confirms.
func TestManualEgressFiltering(t *testing.T) {
	if os.Getenv("MASUDA_MANUAL_VM_TEST") == "" {
		t.Skip("set MASUDA_MANUAL_VM_TEST=1 to run this real-machine VM test")
	}
	exe := buildMasudaForTest(t)
	resolveMasudaExe = func() (string, error) { return exe, nil }

	id := "manvm1"
	worktreeDir := t.TempDir()
	stateDir := t.TempDir()
	var backend Backend = VMBackend{}
	t.Cleanup(func() { _ = backend.Stop(id) })

	if _, err := backend.Start(id, worktreeDir, stateDir, "", "masuda-loop:latest"); err != nil {
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

	// With an empty allowlist (M4 placeholder), this TLS connection must
	// be denied -- curl should fail (connection reset/timeout), not
	// succeed.
	out, err = run("curl -4 -sS -m 8 -o /dev/null -w '%{http_code}' https://example.com/")
	t.Logf("curl -4 https://example.com/ output=%q err=%v", out, err)
	if err == nil {
		t.Errorf("curl to a non-allowlisted host succeeded (output %q), want it denied by the egress proxy", out)
	}
}
