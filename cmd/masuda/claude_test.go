package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/sandbox"
)

func TestClaudeSetTokenSavesFromStdin(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cmd := newClaudeSetTokenCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader("sk-ant-oat-EXAMPLE\n"))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !sandbox.HasClaudeOAuthToken() {
		t.Fatal("no token registered after set-token")
	}
	if !strings.Contains(out.String(), "saved Claude OAuth token to") {
		t.Fatalf("output did not confirm the save: %q", out.String())
	}
}

// The failure this warning exists for is silent (see warnIfNoClaudeToken):
// the VM boots, `claude` exits for lack of credentials, and the loop service
// reports a completed loop. Starting must still work, so this is stderr
// only, never an error.
func TestWarnIfNoClaudeTokenOnlyFiresWhenNoneIsRegistered(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cmd := newClaudeSetTokenCommand()
	var missing bytes.Buffer
	cmd.SetErr(&missing)
	warnIfNoClaudeToken(cmd)
	if !strings.Contains(missing.String(), "masuda claude set-token") {
		t.Fatalf("warning did not name the command that fixes it: %q", missing.String())
	}

	if err := sandbox.SetClaudeOAuthToken("sk-ant-oat-EXAMPLE"); err != nil {
		t.Fatal(err)
	}
	var registered bytes.Buffer
	cmd.SetErr(&registered)
	warnIfNoClaudeToken(cmd)
	if registered.Len() != 0 {
		t.Fatalf("warned even though a token is registered: %q", registered.String())
	}
}

// An empty file is not a usable credential, and would otherwise silence the
// warning while the guest still fails to authenticate.
func TestHasClaudeOAuthTokenIgnoresAnEmptyFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if err := sandbox.SetClaudeOAuthToken("   "); err == nil {
		t.Fatal("expected SetClaudeOAuthToken to reject a blank token")
	}
	if sandbox.HasClaudeOAuthToken() {
		t.Fatal("reported a token after a rejected write")
	}
}
