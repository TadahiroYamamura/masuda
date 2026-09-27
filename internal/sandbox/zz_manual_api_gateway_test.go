package sandbox

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// TestManualGuestReachesAPIWithoutHoldingToken checks the two halves of the
// API gateway on a real VM: nothing in the guest holds the real Claude
// token, and Claude Code there still gets answers from the API through the
// gateway. The second half makes one small real API call on the host's
// registered subscription token.
//
// The image entry disables masuda-loop.service so the loop does not start
// a session of its own (and spend usage) while the test runs; the test
// instead runs the environment setup lines of the image's own
// runtime/entrypoint.sh, so what is checked is what the loop would get.
func TestManualGuestReachesAPIWithoutHoldingToken(t *testing.T) {
	if os.Getenv("MASUDA_MANUAL_VM_TEST") == "" {
		t.Skip("set MASUDA_MANUAL_VM_TEST=1 to run this real-machine VM test")
	}
	tokenPath, err := ClaudeOAuthTokenPath()
	if err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil || strings.TrimSpace(string(token)) == "" {
		t.Skip("no Claude token registered on this host (masuda claude set-token)")
	}
	exe := buildMasudaForTest(t)
	originalResolve := resolveMasudaExe
	resolveMasudaExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { resolveMasudaExe = originalResolve })

	repoRoot := t.TempDir()
	writeImageEntry(t, repoRoot, config.DefaultImageEntry,
		"FROM masuda-loop:latest\nUSER root\nRUN rm -f /etc/systemd/system/multi-user.target.wants/masuda-loop.service\n")
	buildImageEntry(t, repoRoot, config.DefaultImageEntry)

	id := "manvm4"
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
	h, err := vmHost()
	if err != nil {
		t.Fatal(err)
	}
	run := guestRunner(t, h.AttachArgs, id)
	waitForGuestSSH(t, run)

	// Searched as the guest's own user, the one the agent runs as: what
	// matters is whether the agent can read the token, and files only root
	// can read are out of its reach (its sudo is limited to poweroff). grep
	// exits 2 when some file was unreadable and nothing matched, which is a
	// "not found" here. The token goes to grep on stdin, never on a command
	// line.
	guestHas := func(needle string) (bool, error) {
		args, err := h.AttachArgs(id, "grep -rqsFf /dev/stdin / --exclude-dir=proc --exclude-dir=sys --exclude-dir=dev")
		if err != nil {
			return false, err
		}
		grep := exec.Command(args[0], args[1:]...)
		grep.Stdin = strings.NewReader(needle + "\n")
		err = grep.Run()
		if err == nil {
			return true, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok && (exitErr.ExitCode() == 1 || exitErr.ExitCode() == 2) {
			return false, nil
		}
		return false, err
	}
	// Control: the search has to find something that is there, or a
	// "not found" below could just mean the pattern never arrived.
	if found, err := guestHas("masuda-sandbox-placeholder-token"); err != nil || !found {
		t.Fatalf("searching the guest for the placeholder found=%v err=%v; the token search below would prove nothing", found, err)
	}
	if found, err := guestHas(strings.TrimSpace(string(token))); err != nil {
		t.Errorf("searching the guest for the token did not complete (%v), so its absence is unverified", err)
	} else if found {
		t.Error("the real Claude token is readable somewhere in the guest")
	}

	envSetup := "eval \"$(sed -n '/^API_GATEWAY_ADDR=/,/^export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC/p' /opt/masuda/runtime/entrypoint.sh)\""
	out, err := run(envSetup + " && curl -s -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer not-the-placeholder' \"$ANTHROPIC_BASE_URL/v1/messages\"")
	if strings.TrimSpace(out) != "401" {
		t.Errorf("gateway answered a foreign credential with %q (err %v), want 401", out, err)
	}

	out, err = run(envSetup + " && cd /workspace && timeout 180 claude -p 'Reply with exactly: pong' < /dev/null")
	if err != nil || !strings.Contains(out, "pong") {
		t.Errorf("claude -p through the gateway: output %q, err %v; want it to answer", out, err)
	}
}
