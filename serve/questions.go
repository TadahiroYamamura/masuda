package serve

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// questionService はQuestionServiceの実装。質問はRunner.OpenQuestion（固定の質問のノード）と
// ask_human（role付きのquestionノード）が`records/questions/`に残したもの。
type questionService struct {
	apiv1connect.UnimplementedQuestionServiceHandler
	store   *workspace.Store
	backend *backend
}

func (s *questionService) ListOpen(_ context.Context, req *connect.Request[apiv1.ListOpenQuestionsRequest]) (*connect.Response[apiv1.ListOpenQuestionsResponse], error) {
	var ws []*workspace.Workspace
	if id := req.Msg.WorkspaceId; id != "" {
		w, err := lookupWorkspace(s.store, id)
		if err != nil {
			return nil, err
		}
		ws = []*workspace.Workspace{w}
	} else {
		all, err := s.store.List("")
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		ws = all
	}
	out := &apiv1.ListOpenQuestionsResponse{}
	for _, w := range ws {
		qs, err := w.OpenQuestions()
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		for _, q := range qs {
			out.Questions = append(out.Questions, questionToProto(w.ID, q))
		}
	}
	return connect.NewResponse(out), nil
}

func (s *questionService) Answer(_ context.Context, req *connect.Request[apiv1.AnswerRequest]) (*connect.Response[apiv1.AnswerResponse], error) {
	m := req.Msg
	w, err := lookupWorkspace(s.store, m.WorkspaceId)
	if err != nil {
		return nil, err
	}
	qs, err := w.OpenQuestions()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var rec *workspace.QuestionRecord
	for _, q := range qs {
		if q.Occurrence == m.Occurrence {
			rec = q // 同じ出現で何度も聞かれていれば最後のもの
		}
	}
	if rec == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no open question for occurrence %q in workspace %s", m.Occurrence, w.ID))
	}
	// 答えの過不足と選択肢はengineも見るが、エラーの種類を区別できる形では返さないので、
	// 引数の誤りはここで先に見てInvalidArgumentにする。
	if err := checkAnswers(rec, m.Answers); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	c := s.backend.runFor(w.ID)
	if c == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s is not running", w.ID))
	}
	err = c.answer(rec, m.Answers)
	switch {
	case errors.Is(err, engine.ErrNotImplemented):
		return nil, connect.NewError(connect.CodeUnimplemented, err)
	case errors.Is(err, errDecision):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&apiv1.AnswerResponse{}), nil
}

func checkAnswers(rec *workspace.QuestionRecord, answers map[string]string) error {
	asked := map[string]bool{}
	for _, q := range rec.Questions {
		asked[q.ID] = true
		got, ok := answers[q.ID]
		if !ok {
			return fmt.Errorf("no answer to %q", q.ID)
		}
		if len(q.Options) > 0 && !slices.Contains(q.Options, got) {
			return fmt.Errorf("%q is not one of %v for %q", got, q.Options, q.ID)
		}
	}
	for id := range answers {
		if !asked[id] {
			return fmt.Errorf("%q was not asked", id)
		}
	}
	return nil
}

func questionToProto(wsID string, q *workspace.QuestionRecord) *apiv1.OpenQuestion {
	out := &apiv1.OpenQuestion{WorkspaceId: wsID, Occurrence: q.Occurrence, OpenedAt: timestamppb.New(q.OpenedAt)}
	for _, it := range q.Questions {
		out.Items = append(out.Items, &apiv1.QuestionItem{Id: it.ID, Text: it.Text, Options: it.Options})
	}
	return out
}
