package mcpaggregator

import (
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

func TestResolveApprovedRequiresApproval(t *testing.T) {
	decl := config.MCPServerDecl{Command: "npx"}
	cfg := config.Config{MCPServers: map[string]config.MCPServerDecl{"github": decl}}

	approved, skipped := resolveApproved(cfg, config.LocalSettings{})
	if len(approved) != 0 {
		t.Fatalf("approved = %v, want empty (no approval entry at all)", approved)
	}
	if len(skipped) != 1 || skipped[0].name != "github" {
		t.Fatalf("skipped = %v, want one entry for github", skipped)
	}

	local := config.LocalSettings{MCPServers: map[string]config.MCPServerApproval{"github": {Approved: false}}}
	approved, skipped = resolveApproved(cfg, local)
	if len(approved) != 0 {
		t.Fatalf("approved = %v, want empty (Approved: false)", approved)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want one entry", skipped)
	}
}

func TestResolveApprovedRejectsStaleHash(t *testing.T) {
	decl := config.MCPServerDecl{Command: "npx"}
	cfg := config.Config{MCPServers: map[string]config.MCPServerDecl{"github": decl}}
	local := config.LocalSettings{MCPServers: map[string]config.MCPServerApproval{
		"github": {Approved: true, DeclHash: "stale-hash-from-before-a-project-side-change"},
	}}

	approved, skipped := resolveApproved(cfg, local)
	if len(approved) != 0 {
		t.Fatalf("approved = %v, want empty (stale DeclHash)", approved)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want one entry", skipped)
	}
}

func TestResolveApprovedRequiresAllEnvValues(t *testing.T) {
	decl := config.MCPServerDecl{Command: "npx", Env: []string{"GITHUB_TOKEN", "OTHER_TOKEN"}}
	cfg := config.Config{MCPServers: map[string]config.MCPServerDecl{"github": decl}}
	hash, err := config.DeclHash(decl)
	if err != nil {
		t.Fatal(err)
	}

	// Only one of two required env values supplied -- still skipped.
	local := config.LocalSettings{MCPServers: map[string]config.MCPServerApproval{
		"github": {Approved: true, DeclHash: hash, Env: map[string]string{"GITHUB_TOKEN": "abc"}},
	}}
	approved, skipped := resolveApproved(cfg, local)
	if len(approved) != 0 {
		t.Fatalf("approved = %v, want empty (missing OTHER_TOKEN)", approved)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want one entry", skipped)
	}

	// Both supplied, matching hash -- approved.
	local.MCPServers["github"] = config.MCPServerApproval{
		Approved: true, DeclHash: hash,
		Env: map[string]string{"GITHUB_TOKEN": "abc", "OTHER_TOKEN": "def"},
	}
	approved, skipped = resolveApproved(cfg, local)
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want empty", skipped)
	}
	rs, ok := approved["github"]
	if !ok {
		t.Fatal("approved[\"github\"] missing")
	}
	if rs.env["GITHUB_TOKEN"] != "abc" || rs.env["OTHER_TOKEN"] != "def" {
		t.Fatalf("env = %v, want both values resolved", rs.env)
	}
}

func TestResolveApprovedSkipsUndeclaredApprovals(t *testing.T) {
	// An approval for a server no longer declared in settings.json must
	// never start anything -- resolveApproved only ever iterates
	// cfg.MCPServers, so this is really a "doesn't panic / doesn't leak
	// a phantom entry" check.
	cfg := config.Config{}
	local := config.LocalSettings{MCPServers: map[string]config.MCPServerApproval{
		"ghost": {Approved: true, DeclHash: "irrelevant"},
	}}
	approved, skipped := resolveApproved(cfg, local)
	if len(approved) != 0 || len(skipped) != 0 {
		t.Fatalf("approved = %v, skipped = %v, want both empty", approved, skipped)
	}
}
