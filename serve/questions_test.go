package serve

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/internal/mcp"
)

// developのplan-interviewerは計画の未回答の問いを1回のask_humanでまとめて聞く。公開APIは
// それを1つの質問の複数の項目として見せ、答えはすべての項目に揃って初めて受け付ける。
func TestAskHumanWithSeveralQuestions(t *testing.T) {
	srv, ws := startServe(t, t.TempDir())
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
	task, err := c.NextTask(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	occ := task.(map[string]any)["occurrence"].(string)
	type result struct {
		v   any
		err error
	}
	asked := make(chan result, 1)
	go func() {
		v, err := c.AskHuman(ctx, occ, []mcp.Question{
			{ID: "SPEC-1", Text: "退化三角形を不正として扱うか\n理由: 指示書に記述が無い"},
			{ID: "REGRESSION-2", Text: "circle.py の呼び出し元に影響は無いか"},
		})
		asked <- result{v, err}
	}()
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_QUESTION)

	qs := &questionService{store: srv.backend.store, backend: srv.backend}
	list, err := qs.ListOpen(ctx, connect.NewRequest(&apiv1.ListOpenQuestionsRequest{WorkspaceId: id}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Questions) != 1 || len(list.Msg.Questions[0].Items) != 2 ||
		list.Msg.Questions[0].Items[0].Id != "SPEC-1" || list.Msg.Questions[0].Items[1].Id != "REGRESSION-2" {
		t.Fatalf("open questions: %v", list.Msg.Questions)
	}
	_, err = qs.Answer(ctx, connect.NewRequest(&apiv1.AnswerRequest{WorkspaceId: id, Occurrence: occ, Answers: map[string]string{"SPEC-1": "不正とする"}}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a partial answer must be refused as InvalidArgument: %v", err)
	}
	answers := map[string]string{"SPEC-1": "不正とする", "REGRESSION-2": "影響しない"}
	if _, err := qs.Answer(ctx, connect.NewRequest(&apiv1.AnswerRequest{WorkspaceId: id, Occurrence: occ, Answers: answers})); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-asked:
		if r.err != nil {
			t.Fatal(r.err)
		}
		got, _ := r.v.(map[string]any)["answers"].(map[string]string)
		if len(got) != 2 || got["SPEC-1"] != "不正とする" || got["REGRESSION-2"] != "影響しない" {
			t.Fatalf("ask_human returned %v", r.v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask_human must return once every question is answered")
	}
}
