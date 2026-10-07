package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
	// 利用者が先に書いていた個人用の指示とフックは残し、masudaの分を足す。
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.local.md"), []byte("私のメモ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	localSettings := filepath.Join(root, ".claude/settings.local.json")
	if err := os.WriteFile(localSettings, []byte(`{"permissions":{"allow":["Bash(ls)"],"deny":["Bash(rm *)","Bash(masuda remove *)"]},"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"echo hi"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := initRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2+15+4+4 {
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
	if string(ignore) != "node_modules\n.masuda/settings.local.json\n.masuda/claude.local/\nCLAUDE.local.md\n.claude/settings.local.json\n" {
		t.Fatalf(".gitignore = %q", ignore)
	}
	claudeMD, _ := os.ReadFile(filepath.Join(root, "CLAUDE.local.md"))
	if !strings.HasPrefix(string(claudeMD), "私のメモ\n\n<!-- BEGIN MASUDA -->\n") || !strings.Contains(string(claudeMD), "`masuda prime`") {
		t.Errorf("CLAUDE.local.md = %q", claudeMD)
	}
	var ls struct {
		Permissions map[string][]string
		Hooks       map[string][]struct {
			Matcher string
			Hooks   []struct{ Type, Command string }
		}
	}
	b, _ := os.ReadFile(localSettings)
	if err := json.Unmarshal(b, &ls); err != nil {
		t.Fatal(err)
	}
	// 人間が判断することを前提にしたコマンドはdeny、質問への回答はask。list・showは打てるまま。
	wantDeny := []string{"Bash(rm *)", "Bash(masuda remove *)",
		"Bash(masuda secret set *)", "Bash(masuda secret approve *)", "Bash(masuda secret reject *)",
		"Bash(masuda egress approve *)", "Bash(masuda egress reject *)",
		"Bash(masuda gate approve *)", "Bash(masuda gate reject *)", "Bash(masuda gate comment *)",
		"Bash(masuda gate dismiss *)", "Bash(masuda gate halt *)", "Bash(masuda gate redo *)",
		"Bash(masuda privileged-command approve *)"}
	if !slices.Equal(ls.Permissions["deny"], wantDeny) {
		t.Errorf("permissions.deny = %q", ls.Permissions["deny"])
	}
	if !slices.Equal(ls.Permissions["ask"], []string{"Bash(masuda question answer *)"}) {
		t.Errorf("permissions.ask = %q", ls.Permissions["ask"])
	}
	ss := ls.Hooks["SessionStart"]
	if len(ls.Permissions["allow"]) != 1 || len(ss) != 2 ||
		ss[0].Matcher != "startup" || ss[0].Hooks[0].Command != "echo hi" ||
		ss[1].Matcher != "" || len(ss[1].Hooks) != 1 || ss[1].Hooks[0] != (struct{ Type, Command string }{"command", "masuda prime --hook-json"}) {
		t.Errorf("settings.local.json = %s", b)
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
	// 足すものが無ければ、利用者の書き方（ここでは1行のJSON）のまま残す。
	var compact bytes.Buffer
	if err := json.Compact(&compact, b); err != nil {
		t.Fatal(err)
	}
	b = compact.Bytes()
	if err := os.WriteFile(localSettings, b, 0o644); err != nil {
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
	if b2, _ := os.ReadFile(localSettings); string(b2) != string(b) {
		t.Errorf("second init rewrote settings.local.json: %s", b2)
	}
	if b2, _ := os.ReadFile(filepath.Join(root, "CLAUDE.local.md")); string(b2) != string(claudeMD) {
		t.Errorf("second init rewrote CLAUDE.local.md: %q", b2)
	}
}

func TestInitはsettingsLocalJSONを読めなければ書き換えずにエラーにする(t *testing.T) {
	for _, body := range []string{`{"hooks":`, `{"hooks":[]}`, `{"hooks":{"SessionStart":{}}}`, `{"permissions":[]}`, `{"permissions":{"deny":"x"}}`} {
		root := t.TempDir()
		p := filepath.Join(root, ".claude/settings.local.json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := initRepo(root); err == nil {
			t.Errorf("%s: initRepo succeeded", body)
		}
		if got, _ := os.ReadFile(p); string(got) != body {
			t.Errorf("%s: rewritten to %s", body, got)
		}
	}
}

// `.masuda/`・`.claude/`ごと無視しているリポジトリには、その下のファイルの行を重ねて足さない。
func TestInitDoesNotDuplicateIgnoreWhenDirIsIgnored(t *testing.T) {
	for _, line := range []string{
		".masuda/\n/CLAUDE.local.md\n.claude/",
		"/.masuda\nCLAUDE.local.md\n/.claude",
		".masuda/*\nCLAUDE.local.md\n.claude/*",
		".masuda/settings.local.json\n.masuda/claude.local\nCLAUDE.local.md\n.claude/settings.local.json",
	} {
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
