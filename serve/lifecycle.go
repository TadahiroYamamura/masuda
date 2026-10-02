package serve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	"github.com/TadahiroYamamura/masuda-engine/engine"

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
	// sandboxと共に使えなくなる鍵を残さない。
	if w, err := b.store.Get(id); err == nil {
		_ = os.RemoveAll(filepath.Dir(sshKeyFile(w)))
	}
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

func (s *workspaceService) Resume(ctx context.Context, req *connect.Request[apiv1.ResumeRequest]) (*connect.Response[apiv1.Workspace], error) {
	b := s.backend
	b.lifeMu.Lock()
	defer b.lifeMu.Unlock()
	w, err := s.lookup(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if !resumable(w) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s is %s; only a stopped workspace (or one whose sandbox failed to boot) can be resumed", w.ID, w.State))
	}
	if b.runFor(w.ID) != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s is already running", w.ID))
	}
	if _, err := os.Stat(w.DefinitionsDir()); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s has no snapshot of its definitions: %w", w.ID, err))
	}
	set, err := loadDefinitions(w.DefinitionsDir())
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("loading the workspace's definitions: %w", err))
	}
	// 承認・秘密の値はRunの後に変わりうる（取り消し・値の入れ替え）ので、再開のたびに読み直す。
	plan, err := b.planBoot(w.DefinitionsDir(), w.RepoRoot, set, w.Workflow, w.Image)
	if err != nil {
		return nil, err
	}
	prevState, prevReason := w.State, w.Reason
	w.State = workspace.StateStarting
	w.Reason = ""
	if err := w.Save(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	c, err := b.newRunCtl(w, set, plan)
	if err != nil {
		w.State, w.Reason = prevState, prevReason
		_ = w.Save()
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := c.discardAskedQuestions(ctx); err != nil {
		b.removeRun(w.ID)
		w.State, w.Reason = prevState, prevReason
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
	c := s.backend.runFor(w.ID)
	if c == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s has no running sandbox (state %s)", w.ID, w.State))
	}
	if !c.isBooted() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace %s is still starting its sandbox", w.ID))
	}
	acc, err := s.backend.sandbox.EnableSsh(ctx, connect.NewRequest(&sandboxv1.EnableSshRequest{Id: w.ID, User: guest.User}))
	if err != nil {
		var ce *connect.Error
		if errors.As(err, &ce) {
			if ce.Code() == connect.CodeUnimplemented {
				return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("the sandbox service does not provide ssh (masuda serve is using the fake sandbox?): %s", ce.Message()))
			}
			return nil, connect.NewError(ce.Code(), fmt.Errorf("enabling ssh: %s", ce.Message()))
		}
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	key, err := writeSSHKey(w, acc.Msg.PrivateKeyPem)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&apiv1.AttachInfoResponse{SshArgv: attachArgv(acc.Msg.SshArgv, key)}), nil
}

// sshKeyFile はEnableSshが返した秘密鍵を置く場所（`<DataDir>/workspaces/<id>/ssh/`）。
// sandbox serviceが書いた鍵ファイルを使わないのは、その置き場所がsandbox側の都合で決まり、
// 寿命（sandboxの破棄で消える等）もmasudaから見えないため。
func sshKeyFile(w *workspace.Workspace) string { return filepath.Join(w.Dir, "ssh", "id") }

// writeSSHKey はpemを0600で原子的に書き、そのパスを返す。EnableSshのたびに鍵は替わるので毎回書き直す。
func writeSSHKey(w *workspace.Workspace, pem []byte) (string, error) {
	if len(pem) == 0 {
		return "", errors.New("the sandbox service returned no private key")
	}
	p := sshKeyFile(w)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".id-")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(pem); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return p, nil
}

// attachArgv はsandboxが返したssh_argv（最後が接続先、リモートコマンド無し）を、keyの鍵で
// tmuxのメインセッションへアタッチするコマンドにする。`-i`の値はkeyへ差し替え（無ければ足し）、
// 端末を割り当てる`-t`を接続先の前に、リモートコマンドを後ろに置く。
func attachArgv(argv []string, key string) []string {
	if len(argv) < 2 {
		return argv
	}
	opts := append([]string(nil), argv[:len(argv)-1]...)
	dest := argv[len(argv)-1]
	replaced := false
	for i := 1; i+1 < len(opts); i++ {
		if opts[i] == "-i" {
			opts[i+1] = key
			replaced = true
			break
		}
	}
	if !replaced {
		opts = append([]string{opts[0], "-i", key}, opts[1:]...)
	}
	out := append(opts, "-t", dest, "tmux", "attach", "-t", guest.TmuxSession)
	return out
}

// questionDiscardReason は再開で閉じたask_humanの質問の記録に残す理由。
const questionDiscardReason = "再開で破棄"

// discardAskedQuestions は再開の前に開いていたask_humanの質問を、記録に理由を書いて閉じる。
// 聞いていたサブエージェントは前のVMと共に無くなっており、再開後はengineが同じ出現の
// タスクを渡し直すので、新しいエージェントが改めて聞く。前の質問に答えても受け取る者がいない。
// 固定の質問（questionsを書いたquestionノード）はengine自身が待っているので閉じない。
func (c *runCtl) discardAskedQuestions(ctx context.Context) error {
	st, err := c.status(ctx)
	if err != nil {
		return err
	}
	w, err := c.b.store.Get(c.id)
	if err != nil {
		return err
	}
	qs, err := w.OpenQuestions()
	if err != nil {
		return err
	}
	for _, q := range qs {
		if st.Kind == engine.StatusQuestion && st.Occurrence == q.Occurrence {
			continue
		}
		now := time.Now().UTC()
		q.DiscardedAt, q.DiscardReason = &now, questionDiscardReason
		if err := w.SaveQuestion(q); err != nil {
			return err
		}
	}
	return nil
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
