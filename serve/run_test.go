package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
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
	task, err := c.NextTask(ctx, "")
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

func writeGuestTranscript(t *testing.T, dataDir, id string) {
	t.Helper()
	writeRepoFile(t, filepath.Join(FakeDir(dataDir), id, "root", "home", "ubuntu", ".claude", "projects"), "-workspace/s1.jsonl", "{\"a\":1}\n")
}

func assertSandboxDestroyed(t *testing.T, srv *Server, id string) {
	t.Helper()
	_, err := srv.backend.sandbox.GetSandbox(context.Background(), connect.NewRequest(&sandboxv1.GetSandboxRequest{Id: id}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("the sandbox must be destroyed: %v", err)
	}
}

func assertExported(t *testing.T, dataDir, id, logWants string) {
	t.Helper()
	exports := filepath.Join(dataDir, "workspaces", id, "exports")
	log, err := os.ReadFile(filepath.Join(exports, "execution-log.jsonl"))
	if err != nil || !strings.Contains(string(log), logWants) {
		t.Fatalf("execution log in exports must contain %q: %v\n%s", logWants, err, log)
	}
	if b, err := os.ReadFile(filepath.Join(exports, "transcripts", "-workspace", "s1.jsonl")); err != nil || string(b) != "{\"a\":1}\n" {
		t.Fatalf("transcript in exports: %q %v", b, err)
	}
}

// publish・discardを通らずにendで終わった実行も、DONEになった時点で会話ログと実行ログを
// 書き出してVMを壊す。chatは断られる。
func TestEndWithoutPublishCleansUpSandbox(t *testing.T) {
	dataDir := t.TempDir()
	srv, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.json", `{"checks": {"test": "true"}}`)
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "settings")
	ctx := context.Background()
	res, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/fix", Branch: "fix/end", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	writeGuestTranscript(t, dataDir, id)
	c := srv.backend.runFor(id)
	task, err := c.NextTask(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	occ := task.(map[string]any)["occurrence"].(string)
	if r, err := c.ReportResult(ctx, occ, "needs_human", "どちらを直すか", ""); err != nil || r.(map[string]any)["accepted"] != true {
		t.Fatalf("report_result: %v %v", r, err)
	}
	got := waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_DONE)
	if got.Activity.GetKind() != apiv1.ActivityKind_ACTIVITY_KIND_IDLE {
		t.Fatalf("activity after cleanup: %v", got.Activity)
	}
	assertSandboxDestroyed(t, srv, id)
	assertExported(t, dataDir, id, "needs_human")
	// 後から写し直しても（ゲストのnext_task等）失敗しない。
	if done, err := c.NextTask(ctx, ""); err != nil || done.(map[string]any)["kind"] != "done" {
		t.Fatalf("next_task after done: %v %v", done, err)
	}
	if _, err := (&workspaceService{store: srv.backend.store, backend: srv.backend}).AttachInfo(ctx, connect.NewRequest(&apiv1.AttachInfoRequest{Id: id})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("AttachInfo of a done workspace: %v", err)
	}
}

// Stopは会話ログと実行ログを書き出してからVMを壊す。Removeは消す前に実行ログを書き出す。
func TestStopAndRemoveExportLogs(t *testing.T) {
	dataDir := t.TempDir()
	srv, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	ctx := context.Background()
	res, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/stop", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	writeGuestTranscript(t, dataDir, id)
	if _, err := srv.backend.runFor(id).NextTask(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Stop(ctx, connect.NewRequest(&apiv1.StopRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	assertSandboxDestroyed(t, srv, id)
	assertExported(t, dataDir, id, `"kind":`)

	exportedLog := filepath.Join(dataDir, "workspaces", id, "exports", "execution-log.jsonl")
	if err := os.Remove(exportedLog); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Remove(ctx, connect.NewRequest(&apiv1.RemoveRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(exportedLog); err != nil || len(b) == 0 {
		t.Fatalf("Remove must export the execution log: %q %v", b, err)
	}
}
