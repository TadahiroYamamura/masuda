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
