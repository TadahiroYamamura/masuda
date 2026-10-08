package serve

import (
	"context"
	"errors"
	"io"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	"github.com/TadahiroYamamura/masuda/internal/staging"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// stagingService はStagingServiceの実装。ワークスペースのstaging.gitを読むのと、
// records/のコメントを読み書きするだけで、refは動かさない。
type stagingService struct {
	apiv1connect.UnimplementedStagingServiceHandler
	store *workspace.Store
}

// blobChunkSize はGetBlobの1メッセージの大きさ。gRPCの既定の上限（4MiB）より十分小さくする。
const blobChunkSize = 64 * 1024

func (s *stagingService) open(id string) (*workspace.Workspace, *staging.Repo, error) {
	w, err := lookupWorkspace(s.store, id)
	if err != nil {
		return nil, nil, err
	}
	return w, staging.Open(w.StagingDir()), nil
}

func (s *stagingService) ListRefs(ctx context.Context, req *connect.Request[apiv1.ListRefsRequest]) (*connect.Response[apiv1.ListRefsResponse], error) {
	_, repo, err := s.open(req.Msg.WorkspaceId)
	if err != nil {
		return nil, err
	}
	refs, err := repo.ListRefs(ctx)
	if err != nil {
		return nil, stagingError(err)
	}
	out := &apiv1.ListRefsResponse{}
	for _, r := range refs {
		out.Refs = append(out.Refs, &apiv1.Ref{Name: r.Name, Commit: r.Commit})
	}
	return connect.NewResponse(out), nil
}

func (s *stagingService) GetCommit(ctx context.Context, req *connect.Request[apiv1.GetCommitRequest]) (*connect.Response[apiv1.Commit], error) {
	_, repo, err := s.open(req.Msg.WorkspaceId)
	if err != nil {
		return nil, err
	}
	c, err := repo.GetCommit(ctx, req.Msg.Rev)
	if err != nil {
		return nil, stagingError(err)
	}
	return connect.NewResponse(&apiv1.Commit{
		Hash:    c.Hash,
		Parents: c.Parents,
		Author:  c.Author,
		Time:    timestamppb.New(c.Time),
		Message: c.Message,
		Files:   c.Files,
	}), nil
}

func (s *stagingService) Diff(ctx context.Context, req *connect.Request[apiv1.DiffRequest]) (*connect.Response[apiv1.DiffResponse], error) {
	_, repo, err := s.open(req.Msg.WorkspaceId)
	if err != nil {
		return nil, err
	}
	d, err := repo.Diff(ctx, req.Msg.From, req.Msg.To, req.Msg.Paths)
	if err != nil {
		return nil, stagingError(err)
	}
	return connect.NewResponse(&apiv1.DiffResponse{Unified: d}), nil
}

func (s *stagingService) GetBlob(ctx context.Context, req *connect.Request[apiv1.GetBlobRequest], stream *connect.ServerStream[apiv1.BlobChunk]) error {
	_, repo, err := s.open(req.Msg.WorkspaceId)
	if err != nil {
		return err
	}
	rc, err := repo.Blob(ctx, req.Msg.Rev, req.Msg.Path)
	if err != nil {
		return stagingError(err)
	}
	defer rc.Close()
	buf := make([]byte, blobChunkSize)
	for {
		n, rerr := rc.Read(buf)
		if n > 0 {
			if err := stream.Send(&apiv1.BlobChunk{Data: append([]byte(nil), buf[:n]...)}); err != nil {
				return err
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return connect.NewError(connect.CodeInternal, rerr)
		}
	}
	if err := rc.Close(); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return nil
}

func (s *stagingService) ListComments(ctx context.Context, req *connect.Request[apiv1.ListCommentsRequest]) (*connect.Response[apiv1.ListCommentsResponse], error) {
	w, repo, err := s.open(req.Msg.WorkspaceId)
	if err != nil {
		return nil, err
	}
	commit := ""
	if req.Msg.Commit != "" {
		if commit, err = repo.ResolveCommit(ctx, req.Msg.Commit); err != nil {
			return nil, stagingError(err)
		}
	}
	cs, err := w.Comments(commit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := &apiv1.ListCommentsResponse{}
	for _, c := range cs {
		out.Comments = append(out.Comments, commentToProto(w.ID, c))
	}
	return connect.NewResponse(out), nil
}

func (s *stagingService) AddComment(ctx context.Context, req *connect.Request[apiv1.AddCommentRequest]) (*connect.Response[apiv1.Comment], error) {
	w, repo, err := s.open(req.Msg.WorkspaceId)
	if err != nil {
		return nil, err
	}
	if req.Msg.Body == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("comment body is required"))
	}
	// refで指されたコメントもハッシュで保存する。refは後で動くので、名前のままだと
	// 別のコミットへの指摘に化ける。
	commit, err := repo.ResolveCommit(ctx, req.Msg.Commit)
	if err != nil {
		return nil, stagingError(err)
	}
	c, err := w.AddComment(workspace.Comment{
		Commit: commit,
		Path:   req.Msg.Path,
		Line:   req.Msg.Line,
		Author: "human",
		Body:   req.Msg.Body,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(commentToProto(w.ID, c)), nil
}

func commentToProto(wsID string, c workspace.Comment) *apiv1.Comment {
	return &apiv1.Comment{
		Id:          c.ID,
		WorkspaceId: wsID,
		Commit:      c.Commit,
		Path:        c.Path,
		Line:        c.Line,
		Author:      c.Author,
		Body:        c.Body,
		Severity:    c.Severity,
		Time:        timestamppb.New(c.Time),
	}
}
