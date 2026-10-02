package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
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
	if len(created) != 2+14+1 {
		t.Fatalf("created %d files: %v", len(created), created)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("the template settings.json must load: %v", err)
	}
	if cfg.Egress[0] != "api.anthropic.com" || cfg.Checks["test"] == "" {
		t.Fatalf("settings template: %+v", cfg)
	}
	ignore, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if string(ignore) != "node_modules\n.masuda/settings.local.json\n" {
		t.Fatalf(".gitignore = %q", ignore)
	}
	docker, _ := os.ReadFile(filepath.Join(root, ".masuda/images/default/Dockerfile"))
	for _, want := range []string{"tmux", "git", "openssh-server", "ca-certificates", "claude.ai/install.sh", "/workspace /masuda"} {
		if !strings.Contains(string(docker), want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
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
