package perspectives

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinHasFourteenPerspectives(t *testing.T) {
	entries, err := fs.ReadDir(Builtin(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 14 {
		t.Fatalf("builtin perspectives = %d, want 14", len(entries))
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

func TestSnapshotMergesRepoOverBuiltinAndIsKeptOnSecondCall(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "dead-code.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "extra.md"), []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "reviews")
	if err := Snapshot(os.DirFS(repo), dst); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 15 || string(got["dead-code.md"]) != "mine" || string(got["extra.md"]) != "extra" || got["notes.txt"] != nil {
		t.Fatalf("snapshot: %d files, dead-code=%q", len(got), got["dead-code.md"])
	}
	// 再開では写しを作り直さない。
	if err := os.WriteFile(filepath.Join(repo, "dead-code.md"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Snapshot(os.DirFS(repo), dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(dst); string(got["dead-code.md"]) != "mine" {
		t.Fatalf("snapshot was rebuilt: %q", got["dead-code.md"])
	}
}

func TestSnapshotWithoutRepoReviewsIsBuiltin(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "reviews")
	if err := Snapshot(nil, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(dst); len(got) != 14 {
		t.Fatalf("snapshot of builtin = %d files", len(got))
	}
}
