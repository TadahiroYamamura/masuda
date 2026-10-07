package staging

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitState は利用者のリポジトリの`.git`のうち、refs・オブジェクト・インデックスの中身を並べる。
// SnapshotWorktreeの前後で同じなら、利用者のリポジトリに何も書いていない。
func gitState(t *testing.T, repo string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(git(t, repo, "for-each-ref"))
	b.WriteString("\n")
	root := filepath.Join(repo, ".git")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if !strings.HasPrefix(rel, "objects"+string(filepath.Separator)) && rel != "index" && !strings.HasPrefix(rel, "refs"+string(filepath.Separator)) && rel != "packed-refs" && rel != "config" {
			return nil
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		b.WriteString(rel + " " + string(content) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func treeFiles(t *testing.T, r *Repo, rev string) []string {
	t.Helper()
	out := git(t, r.Dir, "ls-tree", "-r", "--name-only", rev)
	files := strings.Split(out, "\n")
	slices.Sort(files)
	return files
}

func TestSnapshotWorktree(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	write(t, repo, ".gitignore", "build/\n*.log\n")
	write(t, repo, "kept.log", "tracked although ignored\n")
	git(t, repo, "add", ".gitignore")
	git(t, repo, "add", "-f", "kept.log")
	git(t, repo, "commit", "-qm", "ignore")
	head := git(t, repo, "rev-parse", "HEAD")
	// 作業ツリー: 追跡しているファイルの変更、未追跡のファイル、gitignoreとinfo/excludeに当たるファイル。
	write(t, repo, "a.go", "package a // changed\n")
	write(t, repo, "new.go", "package a\n")
	write(t, repo, "build/out", "artifact\n")
	write(t, repo, "debug.log", "ignored\n")
	write(t, repo, "local.txt", "excluded by info/exclude\n")
	write(t, repo, ".git/info/exclude", "local.txt\n")
	before := gitState(t, repo)

	t.Run("refを指定しなければ作業ツリーの今の状態を、gitignoreされていない範囲で取り込む", func(t *testing.T) {
		r, err := SnapshotWorktree(ctx, repo, filepath.Join(t.TempDir(), "snap.git"), "")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{".gitignore", "README.md", "a.go", "kept.log", "new.go"}
		if got := treeFiles(t, r, WorktreeSnapshotRef); !slices.Equal(got, want) {
			t.Fatalf("files %v, want %v", got, want)
		}
		if got := git(t, r.Dir, "show", WorktreeSnapshotRef+":a.go"); got != "package a // changed" {
			t.Fatalf("a.go %q", got)
		}
		if parent := git(t, r.Dir, "rev-parse", WorktreeSnapshotRef+"^"); parent != head {
			t.Fatalf("parent %s, want HEAD %s", parent, head)
		}
	})

	t.Run("refを指定すればそのコミットのツリーを取り込み、作業ツリーの変更は含めない", func(t *testing.T) {
		r, err := SnapshotWorktree(ctx, repo, filepath.Join(t.TempDir(), "snap.git"), "HEAD~1")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := git(t, r.Dir, "rev-parse", WorktreeSnapshotRef), git(t, repo, "rev-parse", "HEAD~1"); got != want {
			t.Fatalf("ref %s, want %s", got, want)
		}
		if got := treeFiles(t, r, WorktreeSnapshotRef); !slices.Equal(got, []string{"README.md", "a.go"}) {
			t.Fatalf("files %v", got)
		}
	})

	t.Run("無いrefはエラーにする", func(t *testing.T) {
		if _, err := SnapshotWorktree(ctx, repo, filepath.Join(t.TempDir(), "snap.git"), "nope"); err == nil {
			t.Fatal("no error")
		}
	})

	t.Run("利用者のリポジトリのref・オブジェクト・インデックスは変わらない", func(t *testing.T) {
		if after := gitState(t, repo); after != before {
			t.Fatal("the user's repository changed")
		}
	})
}

func TestSnapshotWorktreeWithoutCommits(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	write(t, repo, "a.txt", "a\n")
	t.Run("まだコミットの無いリポジトリでは親の無いコミットに作業ツリーを取り込む", func(t *testing.T) {
		r, err := SnapshotWorktree(context.Background(), repo, filepath.Join(t.TempDir(), "snap.git"), "")
		if err != nil {
			t.Fatal(err)
		}
		if got := treeFiles(t, r, WorktreeSnapshotRef); !slices.Equal(got, []string{"a.txt"}) {
			t.Fatalf("files %v", got)
		}
		if parents := git(t, r.Dir, "rev-list", "--parents", "-n1", WorktreeSnapshotRef); strings.Contains(parents, " ") {
			t.Fatalf("has a parent: %s", parents)
		}
	})
}
