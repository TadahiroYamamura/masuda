package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

// newTestRepo git-inits a fresh temp directory and chdirs the test process
// into it, restoring the original cwd on cleanup. repoRoot() (cmd/masuda/
// main.go) resolves via `git rev-parse --show-toplevel` against the
// process's cwd, so exercising the mcp subcommands realistically means
// actually running from inside a git repository, the same as a real user
// invocation.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	return dir
}

func runMCPCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newMCPCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestMCPApproveThenListShowsApproved(t *testing.T) {
	root := newTestRepo(t)
	decl := config.MCPServerDecl{Command: "npx", Args: []string{"-y", "gh-mcp"}, Tools: []string{"get_issue"}}
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		MCPServers: map[string]config.MCPServerDecl{"github": decl},
	})

	if out, err := runMCPCommand(t, "approve", "github"); err != nil {
		t.Fatalf("approve error = %v, out = %s", err, out)
	}

	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	approval, ok := local.MCPServers["github"]
	if !ok || !approval.Approved {
		t.Fatalf("MCPServers[\"github\"] = %+v, want an approved entry", approval)
	}
	wantHash, err := config.DeclHash(decl)
	if err != nil {
		t.Fatal(err)
	}
	if approval.DeclHash != wantHash {
		t.Fatalf("DeclHash = %q, want %q", approval.DeclHash, wantHash)
	}

	out, err := runMCPCommand(t, "list")
	if err != nil {
		t.Fatalf("list error = %v, out = %s", err, out)
	}
	if !strings.Contains(out, "github") || !strings.Contains(out, "approved") {
		t.Fatalf("list output = %q, want it to mention github as approved", out)
	}
}

func TestMCPApproveRejectsUndeclaredServer(t *testing.T) {
	newTestRepo(t)
	if _, err := runMCPCommand(t, "approve", "nonexistent"); err == nil {
		t.Fatal("approve nonexistent server: error = nil, want an error")
	}
}

func TestMCPApproveWithEnvFlagStoresValue(t *testing.T) {
	root := newTestRepo(t)
	decl := config.MCPServerDecl{Command: "npx", Env: []string{"GITHUB_TOKEN"}}
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		MCPServers: map[string]config.MCPServerDecl{"github": decl},
	})

	if out, err := runMCPCommand(t, "approve", "github", "--env", "GITHUB_TOKEN=secret123"); err != nil {
		t.Fatalf("approve error = %v, out = %s", err, out)
	}

	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if local.MCPServers["github"].Env["GITHUB_TOKEN"] != "secret123" {
		t.Fatalf("Env[GITHUB_TOKEN] = %q, want %q", local.MCPServers["github"].Env["GITHUB_TOKEN"], "secret123")
	}
}

func TestMCPRejectRemovesApproval(t *testing.T) {
	root := newTestRepo(t)
	decl := config.MCPServerDecl{Command: "npx"}
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		MCPServers: map[string]config.MCPServerDecl{"github": decl},
	})
	if _, err := runMCPCommand(t, "approve", "github"); err != nil {
		t.Fatal(err)
	}

	if _, err := runMCPCommand(t, "reject", "github"); err != nil {
		t.Fatal(err)
	}

	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := local.MCPServers["github"]; ok {
		t.Fatalf("MCPServers[\"github\"] still present after reject: %+v", local.MCPServers["github"])
	}
}

func TestMCPListWithNoDeclarationsReportsEmpty(t *testing.T) {
	newTestRepo(t)
	out, err := runMCPCommand(t, "list")
	if err != nil {
		t.Fatalf("list error = %v, out = %s", err, out)
	}
	if !strings.Contains(out, "no MCP servers declared") {
		t.Fatalf("list output = %q, want a no-servers message", out)
	}
}
