package main

import (
	"os"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// newWorkspaceForRepo creates a workspace whose metadata records repoRoot,
// with masuda's data dir pointed at a temp directory. Unlike
// statedaemon_test.go's newTestWorkspace it takes the repo root as an
// argument, which is the whole subject of these tests.
func newWorkspaceForRepo(t *testing.T, repoRoot string) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id, err := workspace.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Create(repoRoot, id, "feature-branch", "develop", ""); err != nil {
		t.Fatal(err)
	}
	return id
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
}

// TestGateCommandStartsTheDaemon pins Issue #52: every gate command reaches
// the gate through the workspace's state daemon, so resolving a workspace
// for a gate has to make sure that daemon is running -- otherwise a
// workspace left waiting at a gate across a host reboot is unanswerable.
func TestGateCommandStartsTheDaemon(t *testing.T) {
	called := stubEnsureDaemon(t)
	id := newWorkspaceForRepo(t, t.TempDir())

	if _, err := ensureGateWorkspace(id); err != nil {
		t.Fatalf("ensureGateWorkspace() error = %v", err)
	}
	if len(*called) != 1 || (*called)[0] != id {
		t.Errorf("ensureDaemon called with %v, want exactly [%s]", *called, id)
	}
}

// TestGateWorkspaceRefusesUnknownIDBeforeStartingAnything keeps the
// existence check ahead of the daemon: a typo'd workspace ID must be an
// error, not a spawned daemon against a directory nothing owns.
func TestGateWorkspaceRefusesUnknownIDBeforeStartingAnything(t *testing.T) {
	called := stubEnsureDaemon(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if _, err := ensureGateWorkspace("nosuch"); err == nil {
		t.Fatal("ensureGateWorkspace() succeeded for an unknown workspace, want an error")
	}
	if len(*called) != 0 {
		t.Errorf("ensureDaemon called with %v for an unknown workspace, want no calls", *called)
	}
}
