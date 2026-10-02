package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/internal/mcp"
	"github.com/TadahiroYamamura/masuda/internal/staging"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

func stopAndResume(t *testing.T, ws interface {
	Stop(context.Context, *connect.Request[apiv1.StopRequest]) (*connect.Response[apiv1.Workspace], error)
	Resume(context.Context, *connect.Request[apiv1.ResumeRequest]) (*connect.Response[apiv1.Workspace], error)
}, id string) {
	t.Helper()
	if _, err := ws.Stop(context.Background(), connect.NewRequest(&apiv1.StopRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Resume(context.Background(), connect.NewRequest(&apiv1.ResumeRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
}

// 再開で作り直したゲストの作業ツリーには、最新のWIPスナップショットが未コミットの変更として戻る。
// HEADはブランチの先頭のまま。
func TestResumeRestoresLatestWIPIntoWorkTree(t *testing.T) {
	dataDir := t.TempDir()
	srv, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	ctx := context.Background()
	res, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/wip", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	guestTree := filepath.Join(FakeDir(dataDir), id, "root", "workspace")
	writeRepoFile(t, guestTree, "new.txt", "new\n")
	writeRepoFile(t, guestTree, "README.md", "# changed\n")
	if err := os.Remove(filepath.Join(guestTree, ".masuda", "images", "default", "Dockerfile")); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.backend.runFor(id).runner.SnapshotNamed(ctx, "9999999"); err != nil {
		t.Fatal(err)
	}

	stopAndResume(t, ws, id)
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)

	if b, err := os.ReadFile(filepath.Join(guestTree, "new.txt")); err != nil || string(b) != "new\n" {
		t.Fatalf("new.txt after resume: %q %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(guestTree, "README.md")); string(b) != "# changed\n" {
		t.Fatalf("README.md after resume: %q", b)
	}
	if _, err := os.Stat(filepath.Join(guestTree, ".masuda", "images", "default", "Dockerfile")); !os.IsNotExist(err) {
		t.Fatalf("a file deleted in the WIP must stay deleted: %v", err)
	}
	branch := gitT(t, filepath.Join(dataDir, "workspaces", id, "staging.git"), "rev-parse", staging.BranchRef("feat/wip"))
	if head := gitT(t, guestTree, "rev-parse", "HEAD"); head != branch {
		t.Fatalf("guest HEAD %s must stay at the branch %s", head, branch)
	}
	status := gitT(t, guestTree, "status", "--porcelain")
	for _, want := range []string{"A  new.txt", "M  README.md", "D  .masuda/images/default/Dockerfile"} {
		if !strings.Contains(status, want) {
			t.Fatalf("the WIP must come back as uncommitted changes; status:\n%s", status)
		}
	}
}

const askWorkflow = `version: 1
start: ask
nodes:
  ask: {type: question, role: agents/asker, outputs: [answers], next: {answered: finish}}
  finish: {type: discard, export: [answers], next: end}
`

const askAgent = `---
name: asker
description: asks the human
tools: Read
outputs: [answers]
outcomes:
  done: asked
---
Ask.
`

// 再開の前にask_humanで開いていた質問は「再開で破棄」と記録して閉じ、再開後は同じ出現の
// タスクが渡し直される（新しいエージェントが聞き直す）。
func TestResumeDiscardsQuestionsAskedByTheAgent(t *testing.T) {
	dataDir := t.TempDir()
	srv, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/workflows/ask.yaml", askWorkflow)
	writeRepoFile(t, repo, ".masuda/agents/asker.md", askAgent)
	ctx := context.Background()
	res, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/ask", Branch: "feat/ask"}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	c := srv.backend.runFor(id)
	task, err := c.NextTask(ctx)
	if err != nil {
		t.Fatal(err)
	}
	occ := task.(map[string]any)["occurrence"].(string)
	asked := make(chan error, 1)
	go func() {
		_, err := c.AskHuman(ctx, occ, []mcp.Question{{ID: "q", Text: "which?"}})
		asked <- err
	}()
	got := waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_QUESTION)
	if len(got.OpenQuestions) != 1 {
		t.Fatalf("open questions before stop: %v", got.OpenQuestions)
	}

	stopAndResume(t, ws, id)
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("ask_human must return when the workspace stops")
	}
	got = waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	if len(got.OpenQuestions) != 0 {
		t.Fatalf("questions asked before the resume must be closed: %v", got.OpenQuestions)
	}
	w, err := workspace.NewStore(dataDir).Get(id)
	if err != nil {
		t.Fatal(err)
	}
	qs, _ := w.Questions()
	if len(qs) != 1 || qs[0].DiscardReason != questionDiscardReason || qs[0].DiscardedAt == nil {
		t.Fatalf("question record: %+v", qs)
	}
	again, err := srv.backend.runFor(id).NextTask(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m := again.(map[string]any); m["occurrence"] != occ || m["role"] != "asker" {
		t.Fatalf("after resume the same question task is handed out again: %v", m)
	}
}
