package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

func runEgressCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runEgressCommandWithInput(t, "", args...)
}

// runEgressCommandWithInput is runEgressCommand with something on stdin, for
// the confirmation --all asks for. An empty string reaches EOF immediately,
// which is the "no" every non-interactive caller gets.
func runEgressCommandWithInput(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := newEgressCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestEgressApproveThenListShowsApproved(t *testing.T) {
	root := newTestRepo(t)
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		EgressAllowlist: []string{"github.com"},
	})

	if out, err := runEgressCommand(t, "approve", "github.com"); err != nil {
		t.Fatalf("approve error = %v, out = %s", err, out)
	}

	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.EgressAllowlist) != 1 || local.EgressAllowlist[0] != "github.com" {
		t.Fatalf("EgressAllowlist = %v, want [github.com]", local.EgressAllowlist)
	}

	out, err := runEgressCommand(t, "list")
	if err != nil {
		t.Fatalf("list error = %v, out = %s", err, out)
	}
	if !strings.Contains(out, "github.com") || !strings.Contains(out, "approved") {
		t.Fatalf("list output = %q, want it to mention github.com as approved", out)
	}
}

func TestEgressApproveRejectsUndeclaredHostname(t *testing.T) {
	newTestRepo(t)
	if _, err := runEgressCommand(t, "approve", "nonexistent.example"); err == nil {
		t.Fatal("approve undeclared hostname: error = nil, want an error")
	}
}

func TestEgressApproveIsIdempotent(t *testing.T) {
	root := newTestRepo(t)
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		EgressAllowlist: []string{"github.com"},
	})

	if _, err := runEgressCommand(t, "approve", "github.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := runEgressCommand(t, "approve", "github.com"); err != nil {
		t.Fatal(err)
	}

	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.EgressAllowlist) != 1 {
		t.Fatalf("EgressAllowlist = %v, want exactly one entry", local.EgressAllowlist)
	}
}

func TestEgressRejectRemovesApproval(t *testing.T) {
	root := newTestRepo(t)
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		EgressAllowlist: []string{"github.com"},
	})
	if _, err := runEgressCommand(t, "approve", "github.com"); err != nil {
		t.Fatal(err)
	}

	if _, err := runEgressCommand(t, "reject", "github.com"); err != nil {
		t.Fatal(err)
	}

	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.EgressAllowlist) != 0 {
		t.Fatalf("EgressAllowlist = %v, want empty after reject", local.EgressAllowlist)
	}
}

func TestEgressListWithNoDeclarationsReportsEmpty(t *testing.T) {
	newTestRepo(t)
	out, err := runEgressCommand(t, "list")
	if err != nil {
		t.Fatalf("list error = %v, out = %s", err, out)
	}
	if !strings.Contains(out, "no egress hostnames declared") {
		t.Fatalf("list output = %q, want a no-hostnames message", out)
	}
}

// --all is the shortcut a fresh checkout needs: masuda init declares the
// hosts Claude Code cannot start without, and every one of them still has to
// be approved before the sandbox may reach it.
func TestEgressApproveAllApprovesEveryDeclaredHostname(t *testing.T) {
	root := newTestRepo(t)
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		EgressAllowlist: []string{"api.anthropic.com", "platform.claude.com"},
	})

	out, err := runEgressCommandWithInput(t, "y\n", "approve", "--all")
	if err != nil {
		t.Fatalf("approve --all error = %v, out = %s", err, out)
	}

	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.EgressAllowlist) != 2 {
		t.Fatalf("EgressAllowlist = %v, want both declared hosts", local.EgressAllowlist)
	}
	for _, want := range []string{"api.anthropic.com", "platform.claude.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not name %q: %s", want, out)
		}
	}
}

// A flag's help text is not a control: --all has to show what it is about to
// grant and ask, because what it grants is network reach for an AI agent.
func TestEgressApproveAllAsksBeforeApproving(t *testing.T) {
	root := newTestRepo(t)
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		EgressAllowlist: []string{"api.anthropic.com", "platform.claude.com"},
	})

	out, err := runEgressCommandWithInput(t, "n\n", "approve", "--all")
	if err != nil {
		t.Fatalf("approve --all error = %v, out = %s", err, out)
	}

	if !strings.Contains(out, "api.anthropic.com") || !strings.Contains(out, "platform.claude.com") {
		t.Errorf("the prompt must list what it would approve: %s", out)
	}
	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.EgressAllowlist) != 0 {
		t.Fatalf("answering no approved %v anyway", local.EgressAllowlist)
	}
}

// Nothing on stdin (a script, a pipe, a closed stdin) is the answer that
// changes nothing -- never a silent yes.
func TestEgressApproveAllTreatsNoInputAsNo(t *testing.T) {
	root := newTestRepo(t)
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		EgressAllowlist: []string{"api.anthropic.com"},
	})

	if out, err := runEgressCommand(t, "approve", "--all"); err != nil {
		t.Fatalf("approve --all error = %v, out = %s", err, out)
	}

	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.EgressAllowlist) != 0 {
		t.Fatalf("EOF on stdin approved %v", local.EgressAllowlist)
	}
}

// --all approves what the project declared, so it must not invent anything
// when there is nothing to agree to.
func TestEgressApproveAllWithNoDeclarationsIsAnError(t *testing.T) {
	newTestRepo(t)
	if _, err := runEgressCommand(t, "approve", "--all"); err == nil {
		t.Fatal("approve --all with an empty declaration: error = nil, want an error")
	}
}

func TestEgressApproveRejectsBothOrNeitherOfHostnameAndAll(t *testing.T) {
	root := newTestRepo(t)
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		EgressAllowlist: []string{"github.com"},
	})

	if _, err := runEgressCommand(t, "approve", "github.com", "--all"); err == nil {
		t.Error("approve <hostname> --all: error = nil, want an error")
	}
	if _, err := runEgressCommand(t, "approve"); err == nil {
		t.Error("approve with no hostname and no --all: error = nil, want an error")
	}
}

// --all approving one already-approved host and one new one must still save
// the new one rather than short-circuiting on the first "already approved".
func TestEgressApproveAllAddsOnlyTheMissingOnes(t *testing.T) {
	root := newTestRepo(t)
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		EgressAllowlist: []string{"api.anthropic.com", "platform.claude.com"},
	})
	if out, err := runEgressCommand(t, "approve", "api.anthropic.com"); err != nil {
		t.Fatalf("approve error = %v, out = %s", err, out)
	}

	out, err := runEgressCommandWithInput(t, "y\n", "approve", "--all")
	if err != nil {
		t.Fatalf("approve --all error = %v, out = %s", err, out)
	}

	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.EgressAllowlist) != 2 {
		t.Fatalf("EgressAllowlist = %v, want both hosts exactly once", local.EgressAllowlist)
	}
	// The already-approved host is not what the user asked about under
	// --all; reporting it once per host is noise that grows with the list.
	if strings.Contains(out, "already approved") {
		t.Errorf("--all must not report already-approved hosts: %s", out)
	}
	if strings.Contains(out, "approved \"api.anthropic.com\"") {
		t.Errorf("--all must not re-announce a host it did not change: %s", out)
	}
}

func TestEgressApproveAllWithNothingLeftSaysSoOnce(t *testing.T) {
	root := newTestRepo(t)
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		EgressAllowlist: []string{"a.example", "b.example"},
	})
	for _, h := range []string{"a.example", "b.example"} {
		if out, err := runEgressCommand(t, "approve", h); err != nil {
			t.Fatalf("approve %s error = %v, out = %s", h, err, out)
		}
	}

	out, err := runEgressCommand(t, "approve", "--all")
	if err != nil {
		t.Fatalf("approve --all error = %v, out = %s", err, out)
	}
	if !strings.Contains(out, "already approved") || strings.Count(out, "already approved") != 1 {
		t.Errorf("want exactly one summary line, got: %s", out)
	}
}
