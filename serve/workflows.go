package serve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"connectrpc.com/connect"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/pitfalls"
)

// workflowService はWorkflowServiceの実装。対象リポジトリの作業ツリーの`.masuda/`をそのまま
// 読む（写しは取らない）。実行前に定義を確かめるためのもので、Runが使う定義とは別に読み直す。
// repo_rootが空なら同梱の定義だけ。
type workflowService struct {
	apiv1connect.UnimplementedWorkflowServiceHandler
}

// definitionsFor はrepoRoot（空なら同梱だけ）の定義を読み込む。
func definitionsFor(ctx context.Context, repoRoot string) (*engine.Set, error) {
	dir := ""
	if repoRoot != "" {
		if !filepath.IsAbs(repoRoot) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("repo_root must be an absolute path: %q", repoRoot))
		}
		root, err := repoTop(ctx, repoRoot)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		dir = filepath.Join(root, config.DirName)
	}
	set, err := loadDefinitions(dir)
	if err != nil {
		return nil, errLoad{err}
	}
	return set, nil
}

// errLoad は定義の読み込み（ファイル単位の形の検査）の失敗。Checkは問題の1つとして返す。
type errLoad struct{ err error }

func (e errLoad) Error() string { return "loading definitions: " + e.err.Error() }
func (e errLoad) Unwrap() error { return e.err }

func asAPIError(err error) error {
	var le errLoad
	if errors.As(err, &le) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return err
}

func (s *workflowService) List(ctx context.Context, req *connect.Request[apiv1.RepoRequest]) (*connect.Response[apiv1.ListWorkflowsResponse], error) {
	set, err := definitionsFor(ctx, req.Msg.RepoRoot)
	if err != nil {
		return nil, asAPIError(err)
	}
	out := &apiv1.ListWorkflowsResponse{}
	for path, wf := range set.Workflows {
		out.Workflows = append(out.Workflows, &apiv1.WorkflowEntry{
			Path:   path,
			Origin: string(set.Origins[path]),
			Inputs: append([]string(nil), wf.Inputs...),
		})
	}
	sort.Slice(out.Workflows, func(i, j int) bool { return out.Workflows[i].Path < out.Workflows[j].Path })
	return connect.NewResponse(out), nil
}

func (s *workflowService) Show(ctx context.Context, req *connect.Request[apiv1.ShowWorkflowRequest]) (*connect.Response[apiv1.ShowWorkflowResponse], error) {
	if req.Msg.Workflow == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("workflow is required"))
	}
	set, err := definitionsFor(ctx, req.Msg.RepoRoot)
	if err != nil {
		return nil, asAPIError(err)
	}
	if set.Workflows[req.Msg.Workflow] == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow %q is not defined", req.Msg.Workflow))
	}
	mm, err := set.Mermaid(req.Msg.Workflow)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&apiv1.ShowWorkflowResponse{Mermaid: mm}), nil
}

// Check はworkflowをrootとして検査する。workflowが空ならrootのワークフロー（rootWorkflows）を
// それぞれ検査し、同じ問題は1つにまとめる。定義が読み込めないときは、その理由を
// 問題の1つとして返す（何が悪いかを知るための呼び出しなので、エラーにはしない）。
func (s *workflowService) Check(ctx context.Context, req *connect.Request[apiv1.ShowWorkflowRequest]) (*connect.Response[apiv1.CheckWorkflowResponse], error) {
	set, err := definitionsFor(ctx, req.Msg.RepoRoot)
	var le errLoad
	if errors.As(err, &le) {
		problems := append([]*apiv1.Problem{{Message: le.Error()}}, pitfallProblems(ctx, req.Msg.RepoRoot)...)
		return connect.NewResponse(&apiv1.CheckWorkflowResponse{Problems: problems}), nil
	} else if err != nil {
		return nil, err
	}
	var roots []string
	if req.Msg.Workflow != "" {
		if set.Workflows[req.Msg.Workflow] == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow %q is not defined", req.Msg.Workflow))
		}
		roots = []string{req.Msg.Workflow}
	} else {
		roots = rootWorkflows(set)
	}
	out := &apiv1.CheckWorkflowResponse{}
	seen := map[engine.Problem]bool{}
	for _, root := range roots {
		for _, p := range set.Check(root) {
			if seen[p] {
				continue
			}
			seen[p] = true
			out.Problems = append(out.Problems, &apiv1.Problem{Path: p.Path, Node: p.Node, Message: p.Message})
		}
	}
	out.Problems = append(out.Problems, pitfallProblems(ctx, req.Msg.RepoRoot)...)
	return connect.NewResponse(out), nil
}

// pitfallProblems はrepoRootの`.masuda/pitfalls.jsonl`を、Runと同じ検査にかけ、誤りの行ごとに
// 問題を1つ返す。ワークフローに依らないので、どのworkflowを指定しても同じものが出る。
func pitfallProblems(ctx context.Context, repoRoot string) []*apiv1.Problem {
	if repoRoot == "" {
		return nil
	}
	root, err := repoTop(ctx, repoRoot)
	if err != nil {
		return nil // definitionsForが同じ誤りを返している
	}
	b, err := os.ReadFile(filepath.Join(root, config.DirName, pitfalls.FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return []*apiv1.Problem{{Path: pitfalls.FileName, Message: err.Error()}}
	}
	_, err = pitfalls.Parse(b)
	if err == nil {
		return nil
	}
	var out []*apiv1.Problem
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			out = append(out, &apiv1.Problem{Path: pitfalls.FileName, Message: e.Error()})
		}
		return out
	}
	return []*apiv1.Problem{{Path: pitfalls.FileName, Message: err.Error()}}
}

// rootWorkflows は他のどのワークフローからも辿れないワークフロー（engineの仕様でのroot）。
// 部品（`implement/build-step`等）は呼び出し元のデータを前提にするので、単独でrootとして
// 検査すると、呼び出し元が用意するデータの欠落を問題として出してしまう。
func rootWorkflows(set *engine.Set) []string {
	used := map[string]bool{}
	for path := range set.Workflows {
		reach, err := set.Reachable(path)
		if err != nil {
			continue // 辿れない定義はCheckが問題として出す
		}
		for _, p := range reach {
			if p != path {
				used[p] = true
			}
		}
	}
	var roots []string
	for path := range set.Workflows {
		if !used[path] {
			roots = append(roots, path)
		}
	}
	sort.Strings(roots)
	return roots
}
