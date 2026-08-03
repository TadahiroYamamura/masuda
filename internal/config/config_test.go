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

func TestLoadMalformedJSONErrors(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{not valid json`)

	if _, err := Load(dir); err == nil {
		t.Fatal("Load() error = nil, want an error for malformed JSON")
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
