package serve

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
)

// 同梱のworkflows/fixで、計画を立てる役がneeds_humanで終えると、実行はoutcome needs_humanの
// doneになり、役がfeedbackに書いた疑問がreasonとして人間に見える。
func TestNeedsHumanEndCarriesFeedbackAsReason(t *testing.T) {
	dataDir := t.TempDir()
	srv, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.json", `{"checks": {"test": "true"}}`)
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "settings")
	ctx := context.Background()
	res, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/fix", Branch: "fix/vague", Inputs: smokeInputs}))
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
	m := task.(map[string]any)
	if m["role"] != "quick-planner" {
		t.Fatalf("first task of workflows/fix: %v", m)
	}
	question := "「遅い」の対象が一覧APIか検索APIか分からない\nどちらを直すか指定してほしい"
	r, err := c.ReportResult(ctx, m["occurrence"].(string), "needs_human", question, "")
	if err != nil || r.(map[string]any)["accepted"] != true {
		t.Fatalf("report_result: %v %v", r, err)
	}
	got := waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_DONE)
	if got.Outcome != "needs_human" || got.Reason != question {
		t.Fatalf("outcome %q reason %q", got.Outcome, got.Reason)
	}
	// 後から状態を写し直しても理由は消えない。
	if _, err := c.advance(); err != nil {
		t.Fatal(err)
	}
	if got := waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_DONE); got.Reason != question {
		t.Fatalf("reason after re-reflect: %q", got.Reason)
	}
}
