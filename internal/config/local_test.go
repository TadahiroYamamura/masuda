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

func TestLocalStallAfterAndDiskWarn(t *testing.T) {
	var zero LocalSettings
	if d, err := zero.StallAfterDuration(); err != nil || d != DefaultStallAfter {
		t.Fatalf("default stallAfter: %v %v", d, err)
	}
	if zero.DiskWarnThreshold() != DefaultDiskWarnBytes {
		t.Fatalf("default diskWarnBytes: %d", zero.DiskWarnThreshold())
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SettingsLocalPath(dir), []byte(`{"stallAfter":"90s","diskWarnBytes":1024}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := got.StallAfterDuration(); err != nil || d.Seconds() != 90 {
		t.Fatalf("stallAfter: %v %v", d, err)
	}
	if got.DiskWarnThreshold() != 1024 {
		t.Fatalf("diskWarnBytes: %d", got.DiskWarnThreshold())
	}
	for _, bad := range []string{"soon", "-1m", "0s"} {
		if _, err := (LocalSettings{StallAfter: bad}).StallAfterDuration(); err == nil {
			t.Fatalf("stallAfter %q must be refused", bad)
		}
	}
}
