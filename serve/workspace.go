package serve

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

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
	set, err := checkDefinitions(repoRoot, m.Workflow, m.Inputs)
	if err != nil {
		return nil, err
	}

	w, err := s.store.Create(workspace.Meta{
		RepoRoot: repoRoot,
		Branch:   m.Branch,
		Base:     m.Base,
		Workflow: m.Workflow,
		Image:    m.Image,
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
	// VMの起動は秒単位かかるので、RunはSTARTINGで先に返し、起動はリクエストと
	// 切り離して進める。結果は状態（RUNNING/BLOCKED）としてGetに現れる。
	id := w.ID
	start := startRequest{set: set, inputs: m.Inputs}
	s.backend.goBackground(func(ctx context.Context) { s.backend.boot(ctx, id, start) })
	return connect.NewResponse(toProto(w)), nil
}

// checkDefinitions は定義を読み込み、workflowをrootとして検査する。問題があれば
// ワークスペースを作る前に、問題の一覧をInvalidArgumentで返す。
func checkDefinitions(repoRoot, workflow string, inputs map[string][]byte) (*engine.Set, error) {
	set, _, err := loadDefinitions(repoRoot)
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
		out.Workspaces = append(out.Workspaces, toProto(w))
	}
	return connect.NewResponse(out), nil
}

func (s *workspaceService) Get(_ context.Context, req *connect.Request[apiv1.GetWorkspaceRequest]) (*connect.Response[apiv1.Workspace], error) {
	w, err := s.lookup(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProto(w)), nil
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

func toProto(w *workspace.Workspace) *apiv1.Workspace {
	var openGates []string
	if gs, err := w.OpenGates(); err == nil {
		for _, g := range gs {
			openGates = append(openGates, g.Gate)
		}
	}
	return &apiv1.Workspace{
		OpenGates: openGates,
		Id:        w.ID,
		RepoRoot:  w.RepoRoot,
		Branch:    w.Branch,
		Base:      w.Base,
		Workflow:  w.Workflow,
		State:     stateToProto[w.State],
		CreatedAt: timestamppb.New(w.CreatedAt),
		UpdatedAt: timestamppb.New(w.UpdatedAt),
		Outcome:   w.Outcome,
		Reason:    w.Reason,
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
