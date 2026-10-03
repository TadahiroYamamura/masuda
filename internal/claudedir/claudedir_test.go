package claudedir

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMergeLocalWinsAndIgnoresWhatMasudaOwns(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"claude/CLAUDE.md":                  "shared",
		"claude/rules/style.md":             "style",
		"claude/rules/test.md":              "test",
		"claude/rules/notes.txt":            "x",
		"claude/skills/lint/SKILL.md":       "lint",
		"claude/skills/lint/scripts/run.sh": "run",
		"claude/skills/README.md":           "x",
		"claude/agents/mine.md":             "x",
		"claude/settings.json":              "{}",
		"claude.local/CLAUDE.md":            "local",
		"claude.local/rules/test.md":        "my test",
		"claude.local/skills/mine/SKILL.md": "mine",
		"claude.local/commands/hello.md":    "x",
		"reviews/dead-code.md":              "not ours",
	})
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "claude/skills/lint/link")); err != nil {
		t.Fatal(err)
	}
	files, ignored, err := Merge(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for p, b := range files {
		got[p] = string(b)
	}
	want := map[string]string{
		"CLAUDE.md":                  "local",
		"rules/style.md":             "style",
		"rules/test.md":              "my test",
		"skills/lint/SKILL.md":       "lint",
		"skills/lint/scripts/run.sh": "run",
		"skills/mine/SKILL.md":       "mine",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged = %v", got)
	}
	wantIgnored := []string{"claude/agents/", "claude/rules/notes.txt", "claude/settings.json", "claude/skills/README.md", "claude/skills/lint/link", "claude.local/commands/"}
	if !reflect.DeepEqual(ignored, wantIgnored) {
		t.Fatalf("ignored = %v", ignored)
	}
	w := Warning(ignored)
	if strings.Count(w, "\n") != 0 || !strings.Contains(w, ".masuda/claude/agents/") || !strings.Contains(w, ".masuda/claude/settings.json") || !strings.Contains(w, "owned by masuda") {
		t.Fatalf("warning = %q", w)
	}
}

func TestMergeWithoutDirsIsEmptyAndWriteLoadRoundTrips(t *testing.T) {
	files, ignored, err := Merge(t.TempDir())
	if err != nil || len(files) != 0 || len(ignored) != 0 || Warning(ignored) != "" {
		t.Fatalf("empty .masuda: %v %v %v", files, ignored, err)
	}
	dst := filepath.Join(t.TempDir(), "claude")
	if err := Write(dst, files); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("nothing to write must not create %s", dst)
	}
	if got, err := Load(dst); err != nil || len(got) != 0 {
		t.Fatalf("Load(missing) = %v %v", got, err)
	}
	in := map[string][]byte{"CLAUDE.md": []byte("a"), "skills/x/SKILL.md": []byte("b")}
	if err := Write(dst, in); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(dst); err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("Load = %v %v", got, err)
	}
	if err := Write(dst, map[string][]byte{"../x": nil}); err == nil {
		t.Fatal("a path outside dst must be refused")
	}
}
