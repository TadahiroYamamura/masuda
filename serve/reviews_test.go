package serve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
)

// 観点は実リポジトリの`.masuda/reviews/`を同梱に重ねてスナップショットし、ゲストの
// `/masuda/reviews/`へ置く。実リポジトリ側がコミットされていなくても（作業ツリーにあるだけでも）揃う。
func TestRunPlacesReviewSnapshotInGuest(t *testing.T) {
	dataDir := t.TempDir()
	_, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/reviews/dead-code.md", "mine\n")
	writeRepoFile(t, repo, ".masuda/reviews/team-rule.md", "team\n")

	res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/rv", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)

	guestDir := filepath.Join(FakeDir(dataDir), id, "root", "masuda", "reviews")
	entries, err := os.ReadDir(guestDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 15 {
		t.Fatalf("guest /masuda/reviews has %d files, want 14 builtin + 1 extra", len(entries))
	}
	if b, _ := os.ReadFile(filepath.Join(guestDir, "dead-code.md")); string(b) != "mine\n" {
		t.Fatalf("repo perspective must replace the builtin one: %q", b)
	}
	host, err := os.ReadFile(filepath.Join(dataDir, "workspaces", id, "records", "reviews", "team-rule.md"))
	if err != nil || string(host) != "team\n" {
		t.Fatalf("host snapshot: %q %v", host, err)
	}
}
