package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetStoresOutsideRepoWithTightPermissions(t *testing.T) {
	data := t.TempDir()
	s := New(data)
	repo := "/home/u/repo"
	if s.Has(repo, "API_KEY") {
		t.Fatal("unset value reported as set")
	}
	if err := s.Set(repo, "API_KEY", "v1"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(data, "secrets", RepoHash(repo), "API_KEY")
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file %s: %v %v", p, err, st)
	}
	if dst, _ := os.Stat(filepath.Dir(p)); dst.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", dst.Mode().Perm())
	}
	if err := s.Set(repo, "API_KEY", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.Get(repo, "API_KEY"); !ok || v != "v2" {
		t.Fatalf("Get = %q %v", v, ok)
	}
	if s.Has("/home/u/other", "API_KEY") {
		t.Fatal("values must be per repository")
	}
	if err := s.Set(repo, "../x", "v"); err == nil {
		t.Fatal("path-like names must be refused")
	}
	if len(RepoHash(repo)) != 16 {
		t.Fatalf("RepoHash = %q", RepoHash(repo))
	}
}

func TestClaudeTokenFallsBackToLegacyFile(t *testing.T) {
	data := t.TempDir()
	s := New(data)
	if _, ok, _ := s.ClaudeToken("/r", "CLAUDE_CODE_OAUTH_TOKEN"); ok {
		t.Fatal("no token expected")
	}
	if err := os.WriteFile(filepath.Join(data, legacyTokenFile), []byte("legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.ClaudeToken("/r", "CLAUDE_CODE_OAUTH_TOKEN"); !ok || v != "legacy" {
		t.Fatalf("legacy fallback: %q %v", v, ok)
	}
	if err := s.Set("/r", "CLAUDE_CODE_OAUTH_TOKEN", "stored\n"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.ClaudeToken("/r", "CLAUDE_CODE_OAUTH_TOKEN"); v != "stored" {
		t.Fatalf("store must win over the legacy file: %q", v)
	}
}
