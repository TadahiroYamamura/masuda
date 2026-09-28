package sharedfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileReplacesASymlinkInsteadOfWritingThroughIt(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, ".bashrc")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "wf", "feedback"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "wf", "feedback", "0000031.md")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, "wf/feedback/0000031.md", []byte("feedback")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "original" {
		t.Fatalf("the file behind the symlink became %q", b)
	}
	st, err := os.Lstat(target)
	if err != nil || !st.Mode().IsRegular() {
		t.Fatalf("target = %v %v, want a regular file", st, err)
	}
}

func TestWriteFileRefusesADirectorySymlinkLeavingTheRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "wf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "wf", "feedback")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, "wf/feedback/0000031.md", []byte("feedback")); err == nil {
		t.Fatal("WriteFile followed a directory symlink out of the root")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("files appeared outside the root: %v", entries)
	}
}

func TestOpenAppendStaysInsideTheRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "x"), filepath.Join(root, "daemon.log")); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenAppend(root, "daemon.log"); err == nil {
		f.Close()
		t.Fatal("OpenAppend followed a symlink out of the root")
	}
}
