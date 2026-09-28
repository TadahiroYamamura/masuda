package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
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
	if !sandbox.HasClaudeToken(sandbox.DefaultClaudeToken) {
		t.Fatal("no default token registered after set-token")
	}
	if !strings.Contains(out.String(), "saved Claude OAuth token") {
		t.Fatalf("output did not confirm the save: %q", out.String())
	}
}

func TestClaudeSetTokenUnderAName(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cmd := newClaudeSetTokenCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader("sk-ant-oat-EXAMPLE\n"))
	cmd.SetArgs([]string{"--name", "personal"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !sandbox.HasClaudeToken("personal") {
		t.Error("the named token is not registered")
	}
	if sandbox.HasClaudeToken(sandbox.DefaultClaudeToken) {
		t.Error("saving a named token also registered the default")
	}
}

// The failure this warning exists for is silent (see warnIfNoClaudeToken):
// the VM boots, the loop's API calls are refused, and the loop service
// reports a completed loop. Starting must still work, so this is stderr
// only, never an error.
func TestWarnIfNoClaudeTokenOnlyFiresWhenNoneIsRegistered(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()

	cmd := newClaudeSetTokenCommand()
	var missing bytes.Buffer
	cmd.SetErr(&missing)
	warnIfNoClaudeToken(cmd, root)
	if !strings.Contains(missing.String(), "masuda claude set-token") {
		t.Fatalf("warning did not name the command that fixes it: %q", missing.String())
	}

	if err := sandbox.SetClaudeToken(sandbox.DefaultClaudeToken, "sk-ant-oat-EXAMPLE"); err != nil {
		t.Fatal(err)
	}
	var registered bytes.Buffer
	cmd.SetErr(&registered)
	warnIfNoClaudeToken(cmd, root)
	if registered.Len() != 0 {
		t.Fatalf("warned even though a token is registered: %q", registered.String())
	}
}

// An empty file is not a usable credential, and would otherwise silence the
// warning while the guest still fails to authenticate.
func TestHasClaudeTokenIgnoresAnEmptyFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if err := sandbox.SetClaudeToken(sandbox.DefaultClaudeToken, "   "); err == nil {
		t.Fatal("expected SetClaudeToken to reject a blank token")
	}
	if sandbox.HasClaudeToken(sandbox.DefaultClaudeToken) {
		t.Fatal("reported a token after a rejected write")
	}
}

func TestClaudeUseRecordsTheChoicePerRepository(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	t.Chdir(root)
	if err := sandbox.SetClaudeToken("work", "sk-ant-oat-EXAMPLE"); err != nil {
		t.Fatal(err)
	}

	cmd := newClaudeUseCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"work"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if local.ClaudeToken != "work" {
		t.Errorf("settings.local.json claudeToken = %q, want %q", local.ClaudeToken, "work")
	}

	cmd = newClaudeUseCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"not-registered"})
	if err := cmd.Execute(); err == nil {
		t.Error("use accepted a token that is not registered")
	}
}
