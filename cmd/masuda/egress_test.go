package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

func runEgressCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newEgressCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
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

	out, err := runEgressCommand(t, "approve", "--all")
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

	if out, err := runEgressCommand(t, "approve", "--all"); err != nil {
		t.Fatalf("approve --all error = %v, out = %s", err, out)
	}

	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.EgressAllowlist) != 2 {
		t.Fatalf("EgressAllowlist = %v, want both hosts exactly once", local.EgressAllowlist)
	}
}
