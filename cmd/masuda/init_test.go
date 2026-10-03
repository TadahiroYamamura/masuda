package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/guest"
)

func TestInitRepoWritesTemplatesAndKeepsExistingFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("node_modules"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := initRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2+15+2 {
		t.Fatalf("created %d files: %v", len(created), created)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("the template settings.json must load: %v", err)
	}
	// Claude APIは常に許可されるので、雛形は宣言しない（宣言・承認しても意味が無い）。
	if len(cfg.Egress) != 0 || cfg.Checks["test"] == "" {
		t.Fatalf("settings template: %+v", cfg)
	}
	ignore, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if string(ignore) != "node_modules\n.masuda/settings.local.json\n.masuda/claude.local/\n" {
		t.Fatalf(".gitignore = %q", ignore)
	}
	docker, _ := os.ReadFile(filepath.Join(root, ".masuda/images/default/Dockerfile"))
	for _, want := range []string{"tmux", "git", "openssh-server", "ca-certificates", "/workspace /masuda",
		"claude.ai/install.sh | bash -s -- " + guest.ClaudeCodeVersion + "\n", "masuda " + version + "が"} {
		if !strings.Contains(string(docker), want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
	if strings.Contains(string(docker), "__") {
		t.Errorf("Dockerfile has an unfilled marker:\n%s", docker)
	}
	if got, ok := pinnedClaudeCode(docker); !ok || got != guest.ClaudeCodeVersion {
		t.Errorf("pinnedClaudeCode(template) = %q, %v", got, ok)
	}

	// 2回目は既にあるものに触らず、消えたものだけ足す。
	settings := filepath.Join(root, ".masuda/settings.json")
	if err := os.WriteFile(settings, []byte(`{"image":"go"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".masuda/reviews/dead-code.md")); err != nil {
		t.Fatal(err)
	}
	created, err = initRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0] != filepath.Join(".masuda", "reviews", "dead-code.md") {
		t.Fatalf("second init created %v", created)
	}
	if b, _ := os.ReadFile(settings); string(b) != `{"image":"go"}` {
		t.Fatalf("existing settings.json was overwritten: %s", b)
	}
}

// `.masuda/`ごと無視しているリポジトリには、settings.local.jsonの行を重ねて足さない。
func TestInitDoesNotDuplicateIgnoreWhenDirIsIgnored(t *testing.T) {
	for _, line := range []string{".masuda/", "/.masuda", ".masuda/*", ".masuda/settings.local.json\n.masuda/claude.local"} {
		root := t.TempDir()
		want := "node_modules\n" + line + "\n"
		if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := initRepo(root); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(filepath.Join(root, ".gitignore")); string(got) != want {
			t.Errorf("%s: .gitignore = %q", line, got)
		}
	}
}
