package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/guest"
)

func TestClaudeCodeNote(t *testing.T) {
	const pre = "RUN curl -fsSL https://claude.ai/install.sh | bash"
	for _, tc := range []struct {
		name, dockerfile, want string
	}{
		{"verified", pre + " -s -- " + guest.ClaudeCodeVersion + "\n", ""},
		{"unpinned", pre + "\nENV X=1\n", "without a pinned version"},
		{"latest", pre + " -s latest\n", "without a pinned version"},
		{"other", pre + " -s -- 2.0.1\n", "installs Claude Code 2.0.1;"},
		{"no install line", "FROM ubuntu:24.04\n", ""},
	} {
		got := claudeCodeNote("Dockerfile", []byte(tc.dockerfile))
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: note = %q, want containing %q", tc.name, got, tc.want)
		}
	}
}

// image buildは、entryを省けばsettings.jsonのimageのDockerfileを見る。
func TestImageClaudeCodeNoteFollowsSettingsImage(t *testing.T) {
	root := t.TempDir()
	if _, err := initRepo(root); err != nil {
		t.Fatal(err)
	}
	if note := imageClaudeCodeNote(root, ""); note != "" {
		t.Fatalf("the template must not be noted: %q", note)
	}
	dir := filepath.Join(root, ".masuda/images/go")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("RUN curl -fsSL https://claude.ai/install.sh | bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".masuda/settings.json"), []byte(`{"image":"go"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if note := imageClaudeCodeNote(root, ""); !strings.Contains(note, filepath.Join(".masuda/images/go/Dockerfile")) {
		t.Fatalf("note = %q", note)
	}
	if note := imageClaudeCodeNote(root, "default"); note != "" {
		t.Fatalf("explicit entry: note = %q", note)
	}
}

func TestPrintVersionShowsVerifiedClaudeCode(t *testing.T) {
	var b bytes.Buffer
	printVersion(&b, filepath.Join(t.TempDir(), "none.sock"), nil)
	if want := "\n  claude code: " + guest.ClaudeCodeVersion + " (guest, verified)\n"; !strings.Contains(b.String(), want) {
		t.Fatalf("version output lacks %q:\n%s", want, b.String())
	}
}
