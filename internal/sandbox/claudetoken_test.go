package sandbox

import (
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

func TestValidateClaudeTokenNameRejectsPathLikeNames(t *testing.T) {
	for _, bad := range []string{"", "../x", "a/b", ".", "..", "has space", strings.Repeat("a", 65)} {
		if ValidateClaudeTokenName(bad) == nil {
			t.Errorf("ValidateClaudeTokenName(%q) = nil, want an error", bad)
		}
	}
	for _, good := range []string{"default", "work", "personal-max_20"} {
		if err := ValidateClaudeTokenName(good); err != nil {
			t.Errorf("ValidateClaudeTokenName(%q) = %v, want nil", good, err)
		}
	}
}

// The default keeps the path masuda used before tokens had names, so a host
// set up then needs no migration.
func TestDefaultTokenKeepsItsOriginalPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path, err := ClaudeTokenPath(DefaultClaudeToken)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "/masuda/claude-oauth-token") {
		t.Errorf("default token path = %s, want .../masuda/claude-oauth-token", path)
	}
}

func TestResolveRepoClaudeToken(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := t.TempDir()

	name, _, err := ResolveRepoClaudeToken(repo)
	if err != nil || name != DefaultClaudeToken {
		t.Fatalf("with no choice recorded: name %q err %v, want the default", name, err)
	}

	if err := config.SaveLocal(repo, config.LocalSettings{ClaudeToken: "work"}); err != nil {
		t.Fatal(err)
	}
	// Selected but not registered: an error, never a quiet fallback to an
	// account the user did not pick for this repository.
	if _, _, err := ResolveRepoClaudeToken(repo); err == nil || !strings.Contains(err.Error(), "set-token --name work") {
		t.Errorf("unregistered selected token: err %v, want one naming the fix", err)
	}

	if err := SetClaudeToken("work", "sk-ant-oat-EXAMPLE"); err != nil {
		t.Fatal(err)
	}
	name, path, err := ResolveRepoClaudeToken(repo)
	if err != nil || name != "work" {
		t.Fatalf("registered selected token: name %q err %v", name, err)
	}
	want, _ := ClaudeTokenPath("work")
	if path != want {
		t.Errorf("path = %s, want %s", path, want)
	}
}

func TestListClaudeTokens(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, n := range []string{"personal", DefaultClaudeToken, "work"} {
		if err := SetClaudeToken(n, "sk-ant-oat-EXAMPLE"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ListClaudeTokens()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "default,personal,work" {
		t.Errorf("ListClaudeTokens() = %v", got)
	}
}
