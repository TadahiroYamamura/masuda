package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
)

// 観点は実リポジトリの`.masuda/reviews/<集合>/`を集合ごとにスナップショットし、ゲストの
// `/masuda/reviews/<集合>/`へ置く。implementの集合だけが同梱に重なる。実リポジトリ側が
// コミットされていなくても（作業ツリーにあるだけでも）揃う。
func TestRunPlacesReviewSnapshotInGuest(t *testing.T) {
	dataDir := t.TempDir()
	_, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/reviews/implement/dead-code.md", "mine\n")
	writeRepoFile(t, repo, ".masuda/reviews/implement/team-rule.md", "team\n")
	writeRepoFile(t, repo, ".masuda/reviews/implement-plan/layering.md", "layering\n")

	res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/rv", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)

	guestDir := filepath.Join(FakeDir(dataDir), id, "root", "masuda", "reviews")
	count := func(set string) int {
		entries, err := os.ReadDir(filepath.Join(guestDir, set))
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	t.Run("implementの集合は同梱の15観点にリポジトリの観点が重なる", func(t *testing.T) {
		if n := count("implement"); n != 16 {
			t.Fatalf("guest /masuda/reviews/implement has %d files, want 15 builtin + 1 extra", n)
		}
		if b, _ := os.ReadFile(filepath.Join(guestDir, "implement", "dead-code.md")); string(b) != "mine\n" {
			t.Fatalf("repo perspective must replace the builtin one: %q", b)
		}
	})
	t.Run("ほかの集合は置いたものだけがゲストに届く", func(t *testing.T) {
		if n := count("implement-plan"); n != 1 {
			t.Fatalf("guest /masuda/reviews/implement-plan has %d files", n)
		}
	})
	t.Run("ゲストのreviewsの直下には集合のディレクトリしか無い", func(t *testing.T) {
		entries, _ := os.ReadDir(guestDir)
		for _, e := range entries {
			if !e.IsDir() {
				t.Fatalf("a file at the top of /masuda/reviews: %s", e.Name())
			}
		}
	})
	t.Run("ホストの写しも集合ごとに置かれる", func(t *testing.T) {
		host, err := os.ReadFile(filepath.Join(dataDir, "workspaces", id, "records", "reviews", "implement-plan", "layering.md"))
		if err != nil || string(host) != "layering\n" {
			t.Fatalf("host snapshot: %q %v", host, err)
		}
	})
}

// 集合に入れていない直下の観点があれば、黙って外さずに実行を断る。
func TestRunRefusesPerspectivesOutsideSets(t *testing.T) {
	dataDir := t.TempDir()
	_, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/reviews/dead-code.md", "mine\n")

	_, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/rv", Inputs: smokeInputs}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "move it to implement/") {
		t.Fatalf("err = %v", err)
	}
}
