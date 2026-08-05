package workspace

import (
	"testing"
)

func TestListFiltersByRepoRoot(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	idA, err := NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if _, err := Create("/repo/a", idA, "feature-a", "main", ""); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	idB, err := NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if _, err := Create("/repo/b", idB, "feature-b", "main", ""); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	infos, err := List("/repo/a")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(infos) != 1 || infos[0].ID != idA {
		t.Fatalf("List(/repo/a) = %+v, want only %s", infos, idA)
	}
}

func TestListAllReturnsAcrossRepos(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	idA, err := NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if _, err := Create("/repo/a", idA, "feature-a", "main", ""); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	idB, err := NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if _, err := Create("/repo/b", idB, "feature-b", "main", ""); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	infos, err := ListAll()
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("ListAll() returned %d workspaces, want 2", len(infos))
	}
	seen := map[string]bool{}
	for _, info := range infos {
		seen[info.ID] = true
	}
	if !seen[idA] || !seen[idB] {
		t.Fatalf("ListAll() = %+v, want both %s and %s", infos, idA, idB)
	}
}

func TestListAllEmptyWhenNoWorkspaces(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	infos, err := ListAll()
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("ListAll() = %+v, want empty", infos)
	}
}
