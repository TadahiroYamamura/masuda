package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChangedFilesAndDigests(t *testing.T) {
	dir := initTestRepo(t, "main")
	writeFile(t, filepath.Join(dir, "new/a.go"), "a")
	writeFile(t, filepath.Join(dir, "README.md"), "changed\n")
	writeFile(t, filepath.Join(dir, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(dir, "build.log"), "ignored")
	files, err := ChangedFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(files, ","); got != ".gitignore,README.md,new/a.go" {
		t.Fatalf("ChangedFiles = %s", got)
	}
	before, _ := Digests(dir)
	writeFile(t, filepath.Join(dir, "new/a.go"), "a2")
	after, _ := Digests(dir)
	if before["new/a.go"] == after["new/a.go"] || before["README.md"] != after["README.md"] {
		t.Fatalf("digests did not track the edit: %v %v", before, after)
	}
	if HashDigests(before) == HashDigests(after) {
		t.Fatal("HashDigests did not change")
	}
}

func TestCommitFilesCommitsOnlyThoseFiles(t *testing.T) {
	dir := initTestRepo(t, "main")
	writeFile(t, filepath.Join(dir, "in.go"), "in")
	writeFile(t, filepath.Join(dir, "out.go"), "out")
	if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "add", "out.go") // staged but not ours to commit
	ok, err := CommitFiles(dir, dir, []string{"in.go", "README.md"}, "step 1")
	if err != nil || !ok {
		t.Fatalf("CommitFiles = %v %v", ok, err)
	}
	files, _ := ChangedFiles(dir)
	if strings.Join(files, ",") != "out.go" {
		t.Fatalf("left changed = %v, want only out.go", files)
	}
	if ok, err := CommitFiles(dir, dir, []string{"in.go"}, "nothing"); ok || err != nil {
		t.Fatalf("second CommitFiles = %v %v, want nothing to commit", ok, err)
	}
	if err := Tag(dir, "masuda-step-x-1"); err != nil {
		t.Fatal(err)
	}
	if !TagExists(dir, "masuda-step-x-1") || TagExists(dir, "masuda-step-x-2") {
		t.Fatal("TagExists is wrong")
	}
}

func TestDiffAgainstIncludesUntrackedAndKeepsIndex(t *testing.T) {
	dir := initTestRepo(t, "main")
	writeFile(t, filepath.Join(dir, "untracked.go"), "package x\n")
	d, err := DiffAgainst(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "untracked.go") {
		t.Fatalf("diff lacks the untracked file:\n%s", d)
	}
	files, _ := ChangedFiles(dir)
	if len(files) != 1 {
		t.Fatalf("status changed: %v", files)
	}
	staged, _ := hasStagedChanges(dir)
	if staged {
		t.Fatal("DiffAgainst touched the real index")
	}
}

func TestSnapshotTreeIsolatesLaterChanges(t *testing.T) {
	dir := initTestRepo(t, "main")
	writeFile(t, filepath.Join(dir, "before.go"), "left over from earlier work\n")
	tree, err := SnapshotTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "fix.go"), "the fixer's change\n")
	d, err := DiffAgainst(dir, tree)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "fix.go") || strings.Contains(d, "before.go") {
		t.Fatalf("diff since the snapshot:\n%s", d)
	}
}
