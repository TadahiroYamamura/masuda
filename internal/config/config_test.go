package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReturnsZeroValue(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Image != "" {
		t.Fatalf("Image = %q, want empty", cfg.Image)
	}
	if cfg.Base != "" {
		t.Fatalf("Base = %q, want empty", cfg.Base)
	}
	if cfg.ClaudeSettings != nil {
		t.Fatalf("ClaudeSettings = %q, want nil", cfg.ClaudeSettings)
	}
}

func TestLoadReadsImage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"image": "masuda-loop:go"}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Image != "masuda-loop:go" {
		t.Fatalf("Image = %q, want %q", cfg.Image, "masuda-loop:go")
	}
}

func TestLoadReadsBase(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"base": "main"}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Base != "main" {
		t.Fatalf("Base = %q, want %q", cfg.Base, "main")
	}
}

func TestLoadReadsClaudeSettings(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"claudeSettings": {"theme": "dark-ansi"}}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	want := `{"theme": "dark-ansi"}`
	if string(cfg.ClaudeSettings) != want {
		t.Fatalf("ClaudeSettings = %q, want %q", cfg.ClaudeSettings, want)
	}
}

func TestLoadMalformedJSONErrors(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{not valid json`)

	if _, err := Load(dir); err == nil {
		t.Fatal("Load() error = nil, want an error for malformed JSON")
	}
}

func TestLoadReadsMCPServers(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"mcpServers": {"github": {"command": "npx", "args": ["-y", "gh-mcp"], "env": ["GITHUB_TOKEN"], "tools": ["get_issue"]}}}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	decl, ok := cfg.MCPServers["github"]
	if !ok {
		t.Fatal("MCPServers[\"github\"] missing")
	}
	if decl.Command != "npx" {
		t.Fatalf("Command = %q, want %q", decl.Command, "npx")
	}
	if len(decl.Args) != 2 || decl.Args[0] != "-y" || decl.Args[1] != "gh-mcp" {
		t.Fatalf("Args = %v, want [-y gh-mcp]", decl.Args)
	}
	if len(decl.Env) != 1 || decl.Env[0] != "GITHUB_TOKEN" {
		t.Fatalf("Env = %v, want [GITHUB_TOKEN]", decl.Env)
	}
	if len(decl.Tools) != 1 || decl.Tools[0] != "get_issue" {
		t.Fatalf("Tools = %v, want [get_issue]", decl.Tools)
	}
}

func TestDeclHashStableAndSensitiveToChange(t *testing.T) {
	decl := MCPServerDecl{Command: "npx", Args: []string{"-y", "gh-mcp"}, Env: []string{"GITHUB_TOKEN"}, Tools: []string{"get_issue"}}

	h1, err := DeclHash(decl)
	if err != nil {
		t.Fatalf("DeclHash() error = %v, want nil", err)
	}
	h2, err := DeclHash(decl)
	if err != nil {
		t.Fatalf("DeclHash() error = %v, want nil", err)
	}
	if h1 != h2 {
		t.Fatalf("DeclHash() not stable: %q != %q", h1, h2)
	}

	variants := []MCPServerDecl{
		{Command: "npx2", Args: decl.Args, Env: decl.Env, Tools: decl.Tools},
		{Command: decl.Command, Args: []string{"-y", "other-mcp"}, Env: decl.Env, Tools: decl.Tools},
		{Command: decl.Command, Args: decl.Args, Env: []string{"OTHER_TOKEN"}, Tools: decl.Tools},
		{Command: decl.Command, Args: decl.Args, Env: decl.Env, Tools: []string{"other_tool"}},
	}
	for i, v := range variants {
		h, err := DeclHash(v)
		if err != nil {
			t.Fatalf("DeclHash(variant %d) error = %v, want nil", i, err)
		}
		if h == h1 {
			t.Fatalf("DeclHash(variant %d) = %q, want different from base hash %q", i, h, h1)
		}
	}
}

func TestDockerfilePath(t *testing.T) {
	got := DockerfilePath("/repo")
	want := filepath.Join("/repo", DirName, DockerfileName)
	if got != want {
		t.Fatalf("DockerfilePath() = %q, want %q", got, want)
	}
}

func write(t *testing.T, dir, content string) {
	t.Helper()
	settingsDir := filepath.Join(dir, DirName)
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, SettingsFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
