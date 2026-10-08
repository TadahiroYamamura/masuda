package serve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// gateService はGateServiceの実装。ゲートはRunner.OpenGateがワークスペースの記録
// （records/gates/）に残したもので、判断はengine.Decideへ渡してから記録に書く。
type gateService struct {
	apiv1connect.UnimplementedGateServiceHandler
	store   *workspace.Store
	backend *backend
}

func (s *gateService) ListOpen(_ context.Context, req *connect.Request[apiv1.ListOpenGatesRequest]) (*connect.Response[apiv1.ListOpenGatesResponse], error) {
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
	out := &apiv1.ListOpenGatesResponse{}
	for _, w := range ws {
		gs, err := w.OpenGates()
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		for _, g := range gs {
			out.Gates = append(out.Gates, gateToProto(w.ID, g))
		}
	}
	return connect.NewResponse(out), nil
}

func (s *gateService) Get(_ context.Context, req *connect.Request[apiv1.GetGateRequest]) (*connect.Response[apiv1.Gate], error) {
	w, err := lookupWorkspace(s.store, req.Msg.WorkspaceId)
	if err != nil {
		return nil, err
	}
	g, err := latestGate(w, req.Msg.Occurrence)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(gateToProto(w.ID, g)), nil
}

func (s *gateService) Decide(_ context.Context, req *connect.Request[apiv1.DecideRequest]) (*connect.Response[apiv1.Gate], error) {
	m := req.Msg
	if m.Decision == nil || m.Decision.Outcome == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("decision.outcome is required"))
	}
	w, err := lookupWorkspace(s.store, m.WorkspaceId)
	if err != nil {
		return nil, err
	}
	g, err := latestGate(w, m.Occurrence)
	if err != nil {
		return nil, err
	}
	if g.Decision != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("gate %s of occurrence %s has already been decided; list the open gates again", g.Gate, g.Occurrence))
	}
	// ゲートの種類に合わないoutcomeはリクエストの誤りとして返す（契約「エラーコードの約束」）。
	// engineはdeviationの不一致をErrNotImplemented、他を一般のエラーで返し区別できないため、先に見る。
	if allowed := gateOutcomes(g.Gate); !slices.Contains(allowed, m.Decision.Outcome) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("outcome %q is not valid for a %s gate (use one of: %s)", m.Decision.Outcome, g.Gate, strings.Join(allowed, ", ")))
	}
	// 承認は人間が見た内容に結びつける。engineも同じ検査をするが、エラーの種類を区別できる
	// 形では返さないので、ここで先に見てFailedPreconditionにする。
	if m.Decision.Outcome == engine.OutcomeApproved && m.Decision.TargetHash != g.TargetHash {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("target_hash %q does not match the gate's %q; the content changed or was not the one reviewed", m.Decision.TargetHash, g.TargetHash))
	}
	c := s.backend.runFor(w.ID)
	if c == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s is not running; resume it first", w.ID))
	}
	err = c.decide(g, engine.Decision{
		Outcome:       m.Decision.Outcome,
		Comment:       m.Decision.Comment,
		TargetHash:    m.Decision.TargetHash,
		ApprovedFiles: m.Decision.ApprovedFiles,
	})
	switch {
	case errors.Is(err, errDecision):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(gateToProto(w.ID, g)), nil
}

// rejectFeedback は差分ゲートを却下したときにengineへ渡す差し戻しの本文。人間の本文の後に、
// 承認対象のコミットへ人間（author "human"）が付けた行コメントを時刻順に並べる。同じコミットに
// 取り込まれたエージェントの指摘（findings由来）は人間の差し戻し理由ではないので含めない。
func rejectFeedback(body string, comments []workspace.Comment) string {
	var human []workspace.Comment
	for _, c := range comments {
		if c.Author == "human" {
			human = append(human, c)
		}
	}
	if len(human) == 0 {
		return body
	}
	slices.SortStableFunc(human, func(a, b workspace.Comment) int { return a.Time.Compare(b.Time) })
	var b strings.Builder
	if body = strings.TrimSpace(body); body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}
	b.WriteString("## 差分への行コメント\n")
	for _, c := range human {
		b.WriteString("- ")
		b.WriteString(commentLocation(c))
		b.WriteString(": ")
		// 複数行の本文は箇条書きの項目の中に収まるよう字下げする
		b.WriteString(strings.ReplaceAll(strings.TrimSpace(c.Body), "\n", "\n  "))
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// commentLocation はコメントの場所。AddCommentはpath・lineを省けるので、無い部分は書かない。
func commentLocation(c workspace.Comment) string {
	switch {
	case c.Path == "":
		return "（コミット全体）"
	case c.Line == 0:
		return c.Path
	default:
		return fmt.Sprintf("%s:%d", c.Path, c.Line)
	}
}

// gateOutcomes はゲートの種類ごとに受け付けるoutcome。
func gateOutcomes(gate string) []string {
	if gate == engine.GateTriage {
		return []string{"dismiss", "halt", "redo"}
	}
	return []string{engine.OutcomeApproved, engine.OutcomeRejected}
}

// latestGate はoccurrenceが開いたゲートのうち最後のもの（deviationは同じ出現で何度も開く）。
func latestGate(w *workspace.Workspace, occ string) (*workspace.GateRecord, error) {
	gs, err := w.Gates()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var found *workspace.GateRecord
	for _, g := range gs {
		if g.Occurrence == occ {
			found = g
		}
	}
	if found == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no gate for occurrence %q in workspace %s; list the gates to find the occurrence", occ, w.ID))
	}
	return found, nil
}

func gateToProto(wsID string, g *workspace.GateRecord) *apiv1.Gate {
	out := &apiv1.Gate{
		WorkspaceId:   wsID,
		Occurrence:    g.Occurrence,
		Gate:          g.Gate,
		Target:        g.Target,
		TargetHash:    g.TargetHash,
		Subject:       g.Subject,
		StagingCommit: g.StagingCommit,
		OpenedAt:      timestamppb.New(g.OpenedAt),
	}
	if d := g.Decision; d != nil {
		out.Decision = &apiv1.Decision{
			Outcome:       d.Outcome,
			Comment:       d.Comment,
			TargetHash:    d.TargetHash,
			ApprovedFiles: d.ApprovedFiles,
			DecidedAt:     timestamppb.New(d.DecidedAt),
		}
	}
	return out
}
