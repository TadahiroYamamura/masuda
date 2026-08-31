package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/gate"
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

// TestGateShowResolvesWorkspaceOutsideAnyRepository pins Issue #25's fix for
// the read-only half: a workspace ID identifies its workspace on its own, so
// gate commands must not care what the cwd is -- here it isn't even a git
// repository, which the previous repoRoot()-based resolution rejected
// outright.
func TestGateShowResolvesWorkspaceOutsideAnyRepository(t *testing.T) {
	id := newWorkspaceForRepo(t, t.TempDir())
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(stateDir, "review_results", "final_report.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("# findings"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, t.TempDir())

	cmd := newGateCommand(gate.Review)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"show", id})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("review show error = %v, out = %s", err, out.String())
	}
	if !strings.Contains(out.String(), "# findings") {
		t.Errorf("review show printed %q, want the workspace's final_report.md", out.String())
	}
}

// TestReviewApprovalRefusesStaleRepoRoot covers the writing half: approval
// must act on the repository the workspace was created against, so a
// recorded root that has since gone away is an error even though the cwd is
// a perfectly valid git repository the old code would have used instead.
func TestReviewApprovalRefusesStaleRepoRoot(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "moved-away")
	id := newWorkspaceForRepo(t, gone)
	newTestRepo(t) // chdirs into a valid git repository the old code would have used

	err := finalizeReviewApproval(id)
	if err == nil {
		t.Fatal("finalizeReviewApproval succeeded, want an error naming the missing repository")
	}
	if !strings.Contains(err.Error(), gone) {
		t.Errorf("error = %v, want it to name the recorded repository %s", err, gone)
	}
}
