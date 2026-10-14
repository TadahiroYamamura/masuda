package perspectives

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinHasFifteenPerspectives(t *testing.T) {
	entries, err := fs.ReadDir(Builtin(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 15 {
		t.Fatalf("builtin perspectives = %d, want 15", len(entries))
	}
}

// テスト漏れの観点は途中レビュー（ステップ単位）で選ばれてはならない。計画はテストを後の
// ステップに置くことが多く、1ステップの差分だけでは漏れかどうかを判断できないため。
func TestMissingTestsPerspectivesHaveNoTrigger(t *testing.T) {
	for _, name := range []string{"missing-tests-new-code.md", "missing-tests-guard-clauses.md"} {
		b, err := fs.ReadFile(Builtin(), name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "\ntrigger:") {
			t.Errorf("%s has a trigger; it would be picked for interim (per-step) reviews", name)
		}
	}
}

// writeFiles はdirの下に、相対パス→中身のファイルを作る。
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotMergesRepoOverBuiltinAndIsKeptOnSecondCall(t *testing.T) {
	repo := t.TempDir()
	writeFiles(t, repo, map[string]string{
		"implement/dead-code.md":     "mine",
		"implement/extra.md":         "extra",
		"implement/notes.txt":        "x",
		"implement-plan/layering.md": "layering",
		"test-plan/expected.md":      "expected",
	})
	dst := filepath.Join(t.TempDir(), "reviews")
	if err := Snapshot(os.DirFS(repo), dst); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dst)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("implementの集合は同梱の15観点に重なり、同じ名前は置き換わり、無い名前は足される", func(t *testing.T) {
		n := 0
		for name := range got {
			if strings.HasPrefix(name, "implement/") {
				n++
			}
		}
		if n != 16 || string(got["implement/dead-code.md"]) != "mine" || string(got["implement/extra.md"]) != "extra" || got["implement/notes.txt"] != nil {
			t.Fatalf("implement: %d files, dead-code=%q", n, got["implement/dead-code.md"])
		}
	})
	t.Run("ほかの集合は同梱と重ならず、置いたものだけになる", func(t *testing.T) {
		if string(got["implement-plan/layering.md"]) != "layering" || string(got["test-plan/expected.md"]) != "expected" || len(got) != 18 {
			t.Fatalf("sets: %d files", len(got))
		}
		if _, ok := got["implement-plan/dead-code.md"]; ok {
			t.Fatal("builtin perspectives must not leak into other sets")
		}
	})
	t.Run("再開では写しを作り直さない", func(t *testing.T) {
		writeFiles(t, repo, map[string]string{"implement/dead-code.md": "changed", "test-plan/expected.md": "changed"})
		if err := Snapshot(os.DirFS(repo), dst); err != nil {
			t.Fatal(err)
		}
		got, _ := Load(dst)
		if string(got["implement/dead-code.md"]) != "mine" || string(got["test-plan/expected.md"]) != "expected" {
			t.Fatalf("snapshot was rebuilt: %q %q", got["implement/dead-code.md"], got["test-plan/expected.md"])
		}
	})
}

func TestSnapshotWithoutRepoReviewsIsBuiltin(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "reviews")
	if err := Snapshot(nil, dst); err != nil {
		t.Fatal(err)
	}
	got, _ := Load(dst)
	if len(got) != 15 || got["implement/dead-code.md"] == nil {
		t.Fatalf("snapshot of builtin = %d files", len(got))
	}
}

func TestMergeDropsDisabledPerspectives(t *testing.T) {
	repo := t.TempDir()
	off := "---\nname: \"x\"\nenable: false\n---\nbody\n"
	writeFiles(t, repo, map[string]string{
		"implement/dead-code.md": off,
		"implement/mine.md":      off,
		"implement/plain.md":     "no frontmatter",
		"implement-plan/off.md":  off,
		"implement-plan/kept.md": "kept",
	})
	all, err := Merge(os.DirFS(repo))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("同梱の観点はリポジトリの同名ファイルのenable: falseで外れる", func(t *testing.T) {
		if _, ok := all["implement/dead-code.md"]; ok {
			t.Fatal("dropped")
		}
	})
	t.Run("リポジトリだけの観点もenable: falseで外れる", func(t *testing.T) {
		if _, ok := all["implement/mine.md"]; ok {
			t.Fatal("implement/mine.md must be dropped")
		}
		if _, ok := all["implement-plan/off.md"]; ok {
			t.Fatal("implement-plan/off.md must be dropped")
		}
	})
	t.Run("frontmatterの無い観点は有効のまま残る", func(t *testing.T) {
		if _, ok := all["implement/plain.md"]; !ok || all["implement-plan/kept.md"] == nil || len(all) != 16 {
			t.Fatalf("enabled ones stay: %d", len(all))
		}
	})
	t.Run("frontmatterが読めなければエラー", func(t *testing.T) {
		writeFiles(t, repo, map[string]string{"test-plan/bad.md": "---\nenable: [\n---\n"})
		if _, err := Merge(os.DirFS(repo)); err == nil || !strings.Contains(err.Error(), "test-plan/bad.md") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestMergeRefusesMisplacedPerspectives(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"集合に入っていない直下の観点は、implementへ移すよう案内して断る", map[string]string{"dead-code.md": "x"}, "move it to implement/"},
		{"集合の中のディレクトリは断る", map[string]string{"implement-plan/deep/x.md": "x"}, "cannot be nested"},
		{"ドットで始まる集合は断る", map[string]string{".draft/x.md": "x"}, "must not start with"},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFiles(t, repo, c.files)
			if _, err := Merge(os.DirFS(repo)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	t.Run("直下の.md以外のファイルとドットで始まるファイルは無視する", func(t *testing.T) {
		repo := t.TempDir()
		writeFiles(t, repo, map[string]string{"README.txt": "x", ".hidden.md": "x", "implement/.wip.md": "x"})
		all, err := Merge(os.DirFS(repo))
		if err != nil || len(all) != 15 {
			t.Fatalf("%d %v", len(all), err)
		}
	})
}
