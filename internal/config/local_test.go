package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLocalMissingFileReturnsZeroValue(t *testing.T) {
	settings, err := LoadLocal(t.TempDir())
	if err != nil {
		t.Fatalf("LoadLocal() error = %v, want nil", err)
	}
	if settings.MCPServers != nil {
		t.Fatalf("MCPServers = %v, want nil", settings.MCPServers)
	}
}

func TestSaveLocalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := LocalSettings{
		MCPServers: map[string]MCPServerApproval{
			"github": {
				Approved: true,
				DeclHash: "abc123",
				Env:      map[string]string{"GITHUB_TOKEN": "secret"},
			},
		},
	}
	if err := SaveLocal(dir, want); err != nil {
		t.Fatalf("SaveLocal() error = %v, want nil", err)
	}

	got, err := LoadLocal(dir)
	if err != nil {
		t.Fatalf("LoadLocal() error = %v, want nil", err)
	}
	approval, ok := got.MCPServers["github"]
	if !ok {
		t.Fatal("MCPServers[\"github\"] missing after round trip")
	}
	if !approval.Approved || approval.DeclHash != "abc123" || approval.Env["GITHUB_TOKEN"] != "secret" {
		t.Fatalf("approval = %+v, want Approved=true DeclHash=abc123 Env[GITHUB_TOKEN]=secret", approval)
	}
}

func TestSaveLocalPermissions0600(t *testing.T) {
	dir := t.TempDir()
	if err := SaveLocal(dir, LocalSettings{}); err != nil {
		t.Fatalf("SaveLocal() error = %v, want nil", err)
	}
	info, err := os.Stat(SettingsLocalPath(dir))
	if err != nil {
		t.Fatalf("Stat() error = %v, want nil", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permissions = %o, want 600", perm)
	}
}

func TestLoadLocalMalformedJSONErrors(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, DirName)
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, SettingsLocalFileName), []byte(`{not valid json`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadLocal(dir); err == nil {
		t.Fatal("LoadLocal() error = nil, want an error for malformed JSON")
	}
}
