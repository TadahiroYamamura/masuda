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
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

const reviewFirstWorkflow = `version: 1
start: review
nodes:
  review: {type: approval, gate: review, target: diff, next: {approved: end, rejected: rework}}
  rework: {type: agent, role: agents/reworker, next: end}
`

const reworkerAgent = `---
name: reworker
description: reworks the diff
tools: Read
outcomes:
  done: reworked
---
Rework.
`

// 差分ゲートを却下すると、stagingCommitへの人間の行コメントが本文とともに差し戻し先の
// タスクへ届く。記録とAPIのDecision.commentは人間が送った本文のまま。
func TestRejectSendsHumanLineComments(t *testing.T) {
	dataDir := t.TempDir()
	cl := startClients(t, dataDir, Options{})
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/workflows/review-first.yaml", reviewFirstWorkflow)
	writeRepoFile(t, repo, ".masuda/agents/reworker.md", reworkerAgent)
	ctx := context.Background()
	res, err := cl.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/review-first", Branch: "feat/lc"}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, cl.ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	// 実行はゲストのnext_taskで進む。ゲートで待つnext_taskは、却下の後に差し戻し先のタスクを返す。
	tasks := make(chan any, 1)
	go func() {
		task, err := cl.srv.backend.runFor(id).NextTask(ctx)
		if err != nil {
			t.Error(err)
		}
		tasks <- task
	}()
	waitFor(t, cl.ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE)
	open, err := cl.gates.ListOpen(ctx, connect.NewRequest(&apiv1.ListOpenGatesRequest{WorkspaceId: id}))
	if err != nil || len(open.Msg.Gates) != 1 || open.Msg.Gates[0].StagingCommit == "" {
		t.Fatalf("ListOpen: %v %+v", err, open)
	}
	g := open.Msg.Gates[0]
	for _, c := range []*apiv1.AddCommentRequest{
		{WorkspaceId: id, Commit: g.StagingCommit, Path: "README.md", Line: 1, Body: "見出しを英語にする"},
		{WorkspaceId: id, Commit: g.StagingCommit, Path: "a.go", Line: 3, Body: "ここは消す"},
	} {
		if _, err := cl.staging.AddComment(ctx, connect.NewRequest(c)); err != nil {
			t.Fatal(err)
		}
	}
	// エージェントの指摘（findings由来）は届けない
	w, err := cl.srv.backend.store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.AddComment(workspace.Comment{Commit: g.StagingCommit, Path: "README.md", Line: 1, Author: "cross-cutting", Body: "agent finding", Severity: "低"}); err != nil {
		t.Fatal(err)
	}

	d, err := cl.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: id, Occurrence: g.Occurrence, Decision: &apiv1.Decision{Outcome: "rejected", Comment: "直して"}}))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Msg.Decision.GetComment(); got != "直して" {
		t.Fatalf("Decide must return the human's body as is: %q", got)
	}
	var task any
	select {
	case task = <-tasks:
	case <-time.After(10 * time.Second):
		t.Fatal("next_task did not return the rework task")
	}
	taskPath := task.(map[string]any)["task_path"].(string)
	b, err := os.ReadFile(filepath.Join(FakeDir(dataDir), id, "root", taskPath))
	if err != nil {
		t.Fatal(err)
	}
	want := "直して\n\n## 差分への行コメント\n- README.md:1: 見出しを英語にする\n- a.go:3: ここは消す"
	if !strings.Contains(string(b), want) || strings.Contains(string(b), "agent finding") {
		t.Fatalf("task file must carry the body and the human line comments:\n%s", b)
	}
	got, err := cl.gates.Get(ctx, connect.NewRequest(&apiv1.GetGateRequest{WorkspaceId: id, Occurrence: g.Occurrence}))
	if err != nil || got.Msg.Decision.GetComment() != "直して" {
		t.Fatalf("the recorded comment must stay the human's body: %v %q", err, got.Msg.Decision.GetComment())
	}
}

func TestRejectFeedback(t *testing.T) {
	t0 := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	cs := []workspace.Comment{
		{Path: "b.go", Line: 2, Author: "human", Body: "後", Time: t0.Add(time.Minute)},
		{Path: "a.go", Line: 1, Author: "human", Body: "先\n続き", Time: t0},
		{Path: "a.go", Line: 1, Author: "correctness", Body: "指摘", Time: t0},
		{Author: "human", Body: "全体", Time: t0.Add(2 * time.Minute)},
	}
	for _, tc := range []struct {
		name, body string
		cs         []workspace.Comment
		want       string
	}{
		{"本文と行コメント", "理由", cs, "理由\n\n## 差分への行コメント\n- a.go:1: 先\n  続き\n- b.go:2: 後\n- （コミット全体）: 全体"},
		{"行コメントだけ", "", cs[:1], "## 差分への行コメント\n- b.go:2: 後"},
		{"人間のコメントが無い", "理由", cs[2:3], "理由"},
		{"何も無い", "", nil, ""},
	} {
		if got := rejectFeedback(tc.body, tc.cs); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
