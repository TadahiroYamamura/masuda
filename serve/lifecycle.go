package serve

import (
	"context"
	"errors"
	"fmt"
	"os"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/guest"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// active は、sandboxと実行の窓口（runCtl）があるはずの状態か。
func active(s workspace.State) bool {
	switch s {
	case workspace.StateStarting, workspace.StateRunning, workspace.StateWaitingGate, workspace.StateWaitingQuestion:
		return true
	}
	return false
}

// stopRun はidの実行を止める。起動の途中なら取り消して戻りを待ち、MCPを閉じ、sandboxを壊す。
// 記録（engine.json等）とstagingには触らない。lifeMuを持って呼ぶ。
func (b *backend) stopRun(id string) {
	if c := b.runFor(id); c != nil {
		c.cancel()
		<-c.bootDone
		b.removeRun(id)
	}
	b.destroySandbox(id)
	b.acts.drop(id)
}

func (s *workspaceService) Stop(_ context.Context, req *connect.Request[apiv1.StopRequest]) (*connect.Response[apiv1.Workspace], error) {
	b := s.backend
	b.lifeMu.Lock()
	defer b.lifeMu.Unlock()
	w, err := s.lookup(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	switch w.State {
	case workspace.StateStopped:
		return connect.NewResponse(b.toProto(w)), nil
	case workspace.StateDone:
		// publish・discardで終わった実行はsandboxも既に無い。止めるものが無い。
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s is done", w.ID))
	}
	b.stopRun(w.ID)
	// stopRunの間に状態が書かれていることがある（起動の失敗等）ので読み直してから書く。
	if w, err = s.lookup(w.ID); err != nil {
		return nil, err
	}
	w.State = workspace.StateStopped
	if err := w.Save(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b.statusChanged(w.ID)
	return connect.NewResponse(b.toProto(w)), nil
}

func (s *workspaceService) Resume(_ context.Context, req *connect.Request[apiv1.ResumeRequest]) (*connect.Response[apiv1.Workspace], error) {
	b := s.backend
	b.lifeMu.Lock()
	defer b.lifeMu.Unlock()
	w, err := s.lookup(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if w.State != workspace.StateStopped {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s is %s; only a stopped workspace can be resumed", w.ID, w.State))
	}
	if b.runFor(w.ID) != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s is already running", w.ID))
	}
	if _, err := os.Stat(w.DefinitionsDir()); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s has no snapshot of its definitions: %w", w.ID, err))
	}
	set, reviews, err := loadDefinitions(w.DefinitionsDir())
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("loading the workspace's definitions: %w", err))
	}
	w.State = workspace.StateStarting
	w.Reason = ""
	if err := w.Save(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	c, err := b.newRunCtl(w, set, reviews)
	if err != nil {
		w.State = workspace.StateStopped
		_ = w.Save()
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b.statusChanged(w.ID)
	b.goBackground(func(context.Context) { b.boot(c, true) })
	return connect.NewResponse(b.toProto(w)), nil
}

func (s *workspaceService) Remove(_ context.Context, req *connect.Request[apiv1.RemoveRequest]) (*connect.Response[apiv1.RemoveResponse], error) {
	b := s.backend
	b.lifeMu.Lock()
	defer b.lifeMu.Unlock()
	w, err := s.lookup(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if active(w.State) && !req.Msg.Force {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s is %s; stop it first or remove with force", w.ID, w.State))
	}
	b.stopRun(w.ID)
	if err := s.store.RemoveKeepExports(w.ID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b.events.forget(w.ID)
	return connect.NewResponse(&apiv1.RemoveResponse{}), nil
}

func (s *workspaceService) AttachInfo(ctx context.Context, req *connect.Request[apiv1.AttachInfoRequest]) (*connect.Response[apiv1.AttachInfoResponse], error) {
	w, err := s.lookup(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if s.backend.runFor(w.ID) == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s has no running sandbox", w.ID))
	}
	acc, err := s.backend.sandbox.EnableSsh(ctx, connect.NewRequest(&sandboxv1.EnableSshRequest{Id: w.ID}))
	if err != nil {
		// sandbox serviceのコード（フェイクのUnimplemented等）をそのまま返す。
		var ce *connect.Error
		if errors.As(err, &ce) {
			return nil, connect.NewError(ce.Code(), fmt.Errorf("enabling ssh: %s", ce.Message()))
		}
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	argv := append(append([]string(nil), acc.Msg.SshArgv...), "-t", "tmux attach -t "+guest.TmuxSession)
	return connect.NewResponse(&apiv1.AttachInfoResponse{SshArgv: argv}), nil
}

// recoverInterrupted はserveの起動時に、前のプロセスで動いていたワークスペースをSTOPPEDにする。
// 実行の窓口（MCP・engine）は前のプロセスと共に無くなっているので、そのままでは進めない。
// 自動で再開しないのは、止まっていた間に利用者が対象リポジトリや定義を変えているかもしれず、
// 再開するかは人間が決めることだから。残っているかもしれないsandboxは裏で壊す。
func (b *backend) recoverInterrupted() error {
	ws, err := b.store.List("")
	if err != nil {
		return err
	}
	var leftovers []string
	for _, w := range ws {
		if !active(w.State) {
			continue
		}
		w.State = workspace.StateStopped
		if err := w.Save(); err != nil {
			return err
		}
		leftovers = append(leftovers, w.ID)
	}
	if len(leftovers) > 0 {
		b.goBackground(func(context.Context) {
			for _, id := range leftovers {
				b.destroySandbox(id)
			}
		})
	}
	return nil
}
