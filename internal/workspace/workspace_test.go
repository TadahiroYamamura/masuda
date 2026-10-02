package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateGetListRemove(t *testing.T) {
	data := t.TempDir()
	s := NewStore(data)
	if l, err := s.List(""); err != nil || len(l) != 0 {
		t.Fatalf("fresh List: %v %v", l, err)
	}
	a, err := s.Create(Meta{RepoRoot: "/r/a", Branch: "x", Workflow: "workflows/w", State: StateStarting})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(Meta{RepoRoot: "/r/b/", Branch: "y", Workflow: "workflows/w", State: StateStarting})
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(a.ID) || a.ID == b.ID || a.Dir != filepath.Join(data, "workspaces", a.ID) {
		t.Fatalf("ids %q %q dir %q", a.ID, b.ID, a.Dir)
	}
	for _, d := range []string{a.DataDir(), a.RecordsDir(), a.ExportsDir()} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Fatalf("missing %s", d)
		}
	}
	got, err := s.Get(a.ID)
	if err != nil || got.Branch != "x" || got.CreatedAt.IsZero() {
		t.Fatalf("Get: %v %+v", err, got)
	}
	if l, _ := s.List(""); len(l) != 2 {
		t.Fatalf("List all: %d", len(l))
	}
	if l, _ := s.List("/r/b"); len(l) != 1 || l[0].ID != b.ID {
		t.Fatalf("List by repo: %+v", l)
	}
	for _, id := range []string{"nope", "../workspaces", ""} {
		if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get(%q) = %v", id, err)
		}
	}
	if err := s.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after Remove: %v", err)
	}
	if err := s.Remove(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Remove: %v", err)
	}
}

func TestComments(t *testing.T) {
	s := NewStore(t.TempDir())
	w, err := s.Create(Meta{RepoRoot: "/r", Branch: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if cs, err := w.Comments(""); err != nil || len(cs) != 0 {
		t.Fatalf("empty: %v %v", cs, err)
	}
	c1, err := w.AddComment(Comment{Commit: "c1", Path: "a.go", Line: 3, Author: "human", Body: "hm"})
	if err != nil || c1.ID == "" || c1.Time.IsZero() {
		t.Fatalf("AddComment: %v %+v", err, c1)
	}
	if _, err := w.AddComment(Comment{Commit: "c2", Author: "reviewer", Body: "x", Severity: "high"}); err != nil {
		t.Fatal(err)
	}
	if cs, _ := w.Comments("c1"); len(cs) != 1 || cs[0].ID != c1.ID || cs[0].Line != 3 {
		t.Fatalf("c1 comments: %+v", cs)
	}
	if cs, _ := w.Comments(""); len(cs) != 2 {
		t.Fatalf("all comments: %+v", cs)
	}
}
