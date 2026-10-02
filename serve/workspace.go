package serve

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
)

// workspaceService はWorkspaceServiceの実装。ワークスペースの作成（Run）はM2以降で足すので、
// 今はどのワークスペースも存在しない。
type workspaceService struct {
	apiv1connect.UnimplementedWorkspaceServiceHandler
}

func (*workspaceService) List(context.Context, *connect.Request[apiv1.ListWorkspacesRequest]) (*connect.Response[apiv1.ListWorkspacesResponse], error) {
	return connect.NewResponse(&apiv1.ListWorkspacesResponse{}), nil
}

func (*workspaceService) Get(_ context.Context, req *connect.Request[apiv1.GetWorkspaceRequest]) (*connect.Response[apiv1.Workspace], error) {
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace %q not found", req.Msg.Id))
}
