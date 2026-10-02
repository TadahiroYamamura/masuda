package serve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	"github.com/TadahiroYamamura/masuda/internal/staging"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// workspaceService はWorkspaceServiceの実装。Runは定義を読み込んで検査し、ワークスペースと
// stagingを作り、sandboxの起動・ゲストの初期配置・engineの開始・メインセッションの起動を
// バックグラウンドで行う。
type workspaceService struct {
	apiv1connect.UnimplementedWorkspaceServiceHandler
	store   *workspace.Store
	backend *backend
}

func (s *workspaceService) Run(ctx context.Context, req *connect.Request[apiv1.RunRequest]) (*connect.Response[apiv1.Workspace], error) {
	m := req.Msg
	if m.RepoRoot == "" || !filepath.IsAbs(m.RepoRoot) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("repo_root must be an absolute path: %q", m.RepoRoot))
	}
	if m.Workflow == "" || m.Branch == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("workflow and branch are required"))
	}
	repoRoot, err := repoTop(ctx, m.RepoRoot)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// 定義は先に写しを取ってから読み込み・検査する。検査した定義と実行する定義が
	// 同じものであるように（検査の後に作業ツリーが書き換わっても食い違わないように）。
	defs, err := os.MkdirTemp(s.backend.dataDir, ".definitions-")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer os.RemoveAll(defs)
	if err := copyDefinitions(repoRoot, defs); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reading .masuda: %w", err))
	}
	set, err := checkDefinitions(defs, m.Workflow, m.Inputs)
	if err != nil {
		return nil, err
	}
	plan, err := s.backend.planBoot(defs, repoRoot, set, m.Workflow, m.Image)
	if err != nil {
		return nil, err
	}

	s.backend.lifeMu.Lock()
	defer s.backend.lifeMu.Unlock()
	w, err := s.store.Create(workspace.Meta{
		RepoRoot: repoRoot,
		Branch:   m.Branch,
		Base:     m.Base,
		Workflow: m.Workflow,
		Image:    plan.image,
		State:    workspace.StateStarting,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	res, err := staging.Create(ctx, staging.CreateOptions{
		RepoRoot: repoRoot,
		Dir:      w.StagingDir(),
		Branch:   m.Branch,
		Base:     m.Base,
	})
	if err != nil {
		_ = s.store.Remove(w.ID)
		if errors.Is(err, staging.ErrNotFound) {
			// Runにとって見つからないのは要求で指定された分岐元なので、引数の誤りとして返す
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, stagingError(err)
	}
	w.Base = res.Base
	w.BaseCommit = res.BaseCommit
	if err := w.Save(); err != nil {
		_ = s.store.Remove(w.ID)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := os.Rename(defs, w.DefinitionsDir()); err != nil {
		_ = s.store.Remove(w.ID)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	c, err := s.backend.newRunCtl(w, set, plan)
	if err != nil {
		_ = s.store.Remove(w.ID)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// 入力の保存と実行の開始はホストの記録だけで済むので、ここで同期で行う。sandboxの起動前に
	// Stopされても、Resumeは記録から同じ位置で始められる。
	if err := startEngine(ctx, c, m.Inputs, w.Workflow); err != nil {
		s.backend.removeRun(w.ID)
		_ = s.store.Remove(w.ID)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.backend.statusChanged(w.ID)
	// VMの起動は秒単位かかるので、RunはSTARTINGで先に返し、起動はリクエストと
	// 切り離して進める。結果は状態（RUNNING/BLOCKED）としてGetに現れる。
	s.backend.goBackground(func(context.Context) { s.backend.boot(c, false) })
	return connect.NewResponse(s.backend.toProto(w)), nil
}

// startEngine は実行開始時の入力を保存してengineの実行を始める。
func startEngine(ctx context.Context, c *runCtl, inputs map[string][]byte, workflow string) error {
	names := make([]string, 0, len(inputs))
	for name, content := range inputs {
		if err := c.runner.PutData(ctx, c.run(), engine.DataRef{Name: name}, content); err != nil {
			return fmt.Errorf("input %s: %w", name, err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eng.Start(ctx, c.run(), workflow, names)
}

// checkDefinitions は定義を読み込み、workflowをrootとして検査する。問題があれば
// ワークスペースを作る前に、問題の一覧をInvalidArgumentで返す。
func checkDefinitions(dir, workflow string, inputs map[string][]byte) (*engine.Set, error) {
	set, err := loadDefinitions(dir)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("loading definitions: %w", err))
	}
	wf := set.Workflows[workflow]
	if wf == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow %q is not defined", workflow))
	}
	if problems := set.Check(workflow); len(problems) > 0 {
		var lines []string
		for _, p := range problems {
			loc := p.Path
			if p.Node != "" {
				loc += " (node " + p.Node + ")"
			}
			lines = append(lines, loc+": "+p.Message)
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow %s has problems:\n%s", workflow, strings.Join(lines, "\n")))
	}
	var missing []string
	for _, in := range wf.Inputs {
		if _, ok := inputs[in]; !ok {
			missing = append(missing, in)
		}
	}
	if len(missing) > 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow %s needs inputs %v", workflow, missing))
	}
	return set, nil
}

// repoTop はrepo_rootが作業ツリーのトップそのものであることを確かめ、正規化したパスを返す。
// サブディレクトリを渡されたときに黙ってトップへ読み替えると、.masuda/の置き場所の
// 取り違えに気づけないので拒否する。
func repoTop(ctx context.Context, repoRoot string) (string, error) {
	top, err := staging.TopLevel(ctx, repoRoot)
	if err != nil {
		return "", err
	}
	want, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return "", err
	}
	got, err := filepath.EvalSymlinks(top)
	if err != nil {
		return "", err
	}
	if filepath.Clean(want) != filepath.Clean(got) {
		return "", fmt.Errorf("repo_root %s is not the top of its work tree (%s)", repoRoot, top)
	}
	return filepath.Clean(repoRoot), nil
}

func (s *workspaceService) List(_ context.Context, req *connect.Request[apiv1.ListWorkspacesRequest]) (*connect.Response[apiv1.ListWorkspacesResponse], error) {
	ws, err := s.store.List(req.Msg.RepoRoot)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := &apiv1.ListWorkspacesResponse{}
	for _, w := range ws {
		out.Workspaces = append(out.Workspaces, s.backend.toProto(w))
	}
	return connect.NewResponse(out), nil
}

func (s *workspaceService) Get(_ context.Context, req *connect.Request[apiv1.GetWorkspaceRequest]) (*connect.Response[apiv1.Workspace], error) {
	w, err := s.lookup(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(s.backend.toProto(w)), nil
}

func (s *workspaceService) lookup(id string) (*workspace.Workspace, error) {
	return lookupWorkspace(s.store, id)
}

func lookupWorkspace(store *workspace.Store, id string) (*workspace.Workspace, error) {
	w, err := store.Get(id)
	if errors.Is(err, workspace.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace %q not found", id))
	} else if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return w, nil
}

var stateToProto = map[workspace.State]apiv1.WorkspaceState{
	workspace.StateStarting:        apiv1.WorkspaceState_WORKSPACE_STATE_STARTING,
	workspace.StateRunning:         apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING,
	workspace.StateWaitingGate:     apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE,
	workspace.StateWaitingQuestion: apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_QUESTION,
	workspace.StateStopped:         apiv1.WorkspaceState_WORKSPACE_STATE_STOPPED,
	workspace.StateDone:            apiv1.WorkspaceState_WORKSPACE_STATE_DONE,
	workspace.StateBlocked:         apiv1.WorkspaceState_WORKSPACE_STATE_BLOCKED,
}

// toProto はワークスペースを公開APIの形にする。活動はメモリの観測から、開いたゲート・質問は記録から埋める。
func (b *backend) toProto(w *workspace.Workspace) *apiv1.Workspace {
	var openGates, openQuestions []string
	if gs, err := w.OpenGates(); err == nil {
		for _, g := range gs {
			openGates = append(openGates, g.Gate)
		}
	}
	if qs, err := w.OpenQuestions(); err == nil {
		for _, q := range qs {
			openQuestions = append(openQuestions, q.Occurrence)
		}
	}
	return &apiv1.Workspace{
		Id:            w.ID,
		RepoRoot:      w.RepoRoot,
		Branch:        w.Branch,
		Base:          w.Base,
		Workflow:      w.Workflow,
		State:         stateToProto[w.State],
		Activity:      b.acts.compute(w, b.stallFor(w), time.Now().UTC()),
		Position:      w.Position,
		CreatedAt:     timestamppb.New(w.CreatedAt),
		UpdatedAt:     timestamppb.New(w.UpdatedAt),
		Outcome:       w.Outcome,
		Reason:        w.Reason,
		OpenGates:     openGates,
		OpenQuestions: openQuestions,
	}
}

// stagingError はinternal/stagingのエラーを公開APIのコードへ写す。
func stagingError(err error) error {
	switch {
	case errors.Is(err, staging.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, staging.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, staging.ErrBranchExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeCanceled, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
