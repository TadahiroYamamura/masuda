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
	if settings.PrivilegedCommandsApproved != nil || settings.ClaudeTokenName() != ReservedSecret {
		t.Fatalf("zero value expected: %+v", settings)
	}
}

func TestSaveLocalRoundTripEgressAllowlist(t *testing.T) {
	dir := t.TempDir()
	want := LocalSettings{EgressApproved: []string{"github.com"}}
	if err := SaveLocal(dir, want); err != nil {
		t.Fatalf("SaveLocal() error = %v, want nil", err)
	}

	got, err := LoadLocal(dir)
	if err != nil {
		t.Fatalf("LoadLocal() error = %v, want nil", err)
	}
	if len(got.EgressApproved) != 1 || got.EgressApproved[0] != "github.com" {
		t.Fatalf("EgressApproved = %v, want [github.com]", got.EgressApproved)
	}
}

func TestSaveLocalRoundTripPrivilegedCommands(t *testing.T) {
	dir := t.TempDir()
	want := LocalSettings{
		PrivilegedCommandsApproved: map[string]PrivilegedCommandApproval{
			"e2e": {DeclHash: "abc123"},
		},
	}
	if err := SaveLocal(dir, want); err != nil {
		t.Fatalf("SaveLocal() error = %v, want nil", err)
	}

	got, err := LoadLocal(dir)
	if err != nil {
		t.Fatalf("LoadLocal() error = %v, want nil", err)
	}
	approval, ok := got.PrivilegedCommandsApproved["e2e"]
	if !ok {
		t.Fatal("PrivilegedCommands[\"e2e\"] missing after round trip")
	}
	if approval.DeclHash != "abc123" {
		t.Fatalf("approval = %+v, want DeclHash=abc123", approval)
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

func TestLocalStallAfter(t *testing.T) {
	var zero LocalSettings
	if d, err := zero.StallAfterDuration(); err != nil || d != 0 {
		t.Fatalf("unset stallAfter leaves it to the serve default: %v %v", d, err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SettingsLocalPath(dir), []byte(`{"stallAfter":"90s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := got.StallAfterDuration(); err != nil || d.Seconds() != 90 {
		t.Fatalf("stallAfter: %v %v", d, err)
	}
	for _, bad := range []string{"soon", "-1m", "0s"} {
		if _, err := (LocalSettings{StallAfter: bad}).StallAfterDuration(); err == nil {
			t.Fatalf("stallAfter %q must be refused", bad)
		}
	}
	// diskWarnBytesはserve全体の設定（config.json）へ移ったので、ここでは知らないキー。
	if err := os.WriteFile(SettingsLocalPath(dir), []byte(`{"diskWarnBytes":1024}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocal(dir); err == nil {
		t.Fatal("diskWarnBytes in settings.local.json must be refused")
	}
}

func TestLoadServe(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if c, err := LoadServe(p); err != nil || c != (ServeConfig{}) || c.DiskWarnThreshold() != DefaultDiskWarnBytes {
		t.Fatalf("missing config.json: %+v %v", c, err)
	}
	if err := os.WriteFile(p, []byte(`{"listen":"127.0.0.1:7788","stallAfter":"90s","diskWarnBytes":1024,"sandboxSocket":"/s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServe(p)
	if err != nil || c.Listen != "127.0.0.1:7788" || c.DiskWarnThreshold() != 1024 || c.SandboxSocket != "/s" {
		t.Fatalf("config.json: %+v %v", c, err)
	}
	if d, _ := c.StallAfterDuration(); d.Seconds() != 90 {
		t.Fatalf("stallAfter: %v", d)
	}
	for _, bad := range []string{`{"listen":"0.0.0.0:7788"}`, `{"listen":"example.com:80"}`, `{"listen":"127.0.0.1"}`, `{"stallAfter":"0s"}`, `{"nope":1}`} {
		if err := os.WriteFile(p, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadServe(p); err == nil {
			t.Fatalf("%s must be refused", bad)
		}
	}
}
