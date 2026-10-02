package serve

import (
	"context"
	"errors"
	"fmt"

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
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("gate %s of occurrence %s has already been decided", g.Gate, g.Occurrence))
	}
	// 承認は人間が見た内容に結びつける。engineも同じ検査をするが、エラーの種類を区別できる
	// 形では返さないので、ここで先に見てFailedPreconditionにする。
	if m.Decision.Outcome == engine.OutcomeApproved && m.Decision.TargetHash != g.TargetHash {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("target_hash %q does not match the gate's %q; the content changed or was not the one reviewed", m.Decision.TargetHash, g.TargetHash))
	}
	c := s.backend.runFor(w.ID)
	if c == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s is not running", w.ID))
	}
	err = c.decide(g, engine.Decision{
		Outcome:       m.Decision.Outcome,
		Comment:       m.Decision.Comment,
		TargetHash:    m.Decision.TargetHash,
		ApprovedFiles: m.Decision.ApprovedFiles,
	})
	switch {
	case errors.Is(err, engine.ErrNotImplemented):
		return nil, connect.NewError(connect.CodeUnimplemented, err)
	case errors.Is(err, errDecision):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(gateToProto(w.ID, g)), nil
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
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no gate for occurrence %q in workspace %s", occ, w.ID))
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
