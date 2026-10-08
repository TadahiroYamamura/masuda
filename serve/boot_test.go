package serve

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyDefinitionsはイメージのビルドコンテキストの権限を作業ツリーと同じに写す(t *testing.T) {
	repo := t.TempDir()
	ctxDir := filepath.Join(repo, ".masuda", "images", "default")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]os.FileMode{
		"Dockerfile":   0o644,
		"package.json": 0o644,
		"setup.sh":     0o755,
		"private.txt":  0o600,
	}
	for name, mode := range files {
		p := filepath.Join(ctxDir, name)
		if err := os.WriteFile(p, []byte(name), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(t.TempDir(), "definitions")
	if err := copyDefinitions(repo, dst); err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		st, err := os.Stat(filepath.Join(dst, "images", "default", name))
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Errorf("%s: mode = %o, want %o", name, got, want)
		}
	}
	st, err := os.Stat(filepath.Join(dst, "images", "default"))
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o755 {
		t.Errorf("images/default: mode = %o, want 755", got)
	}
}
