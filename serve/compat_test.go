package serve

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/fakesandbox"
	"github.com/TadahiroYamamura/masuda/internal/sandboxcontract"
	"github.com/TadahiroYamamura/masuda/internal/secrets"
)

// skewedSandbox はフェイクのsandboxで、GetServerInfoのcontract_sha256だけを差し替えられる。
type skewedSandbox struct {
	*fakesandbox.Service
	sha atomic.Value // string
}

func (s *skewedSandbox) GetServerInfo(context.Context, *connect.Request[sandboxv1.GetServerInfoRequest]) (*connect.Response[sandboxv1.ServerInfo], error) {
	return connect.NewResponse(&sandboxv1.ServerInfo{Version: "9.9.9", ContractSha256: s.sha.Load().(string)}), nil
}

func startSkewedSandbox(t *testing.T, sha string) (*skewedSandbox, string) {
	t.Helper()
	sb := &skewedSandbox{Service: fakesandbox.New(t.TempDir())}
	sb.sha.Store(sha)
	sock := filepath.Join(t.TempDir(), "sb.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(sandboxv1connect.NewSandboxServiceHandler(sb))
	srv := &http.Server{Handler: h2c.NewHandler(mux, &http2.Server{})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sb, sock
}

func startServeWith(t *testing.T, opts Options) (*Server, error) {
	t.Helper()
	opts.Socket = filepath.Join(t.TempDir(), "masuda.sock")
	opts.DataDir = t.TempDir()
	// 実物のsandboxを相手にするとトークンが要る（フェイクでは要らない）。契約の確認より前に断られないよう置く。
	if err := secrets.New(opts.DataDir).Set("", config.ReservedSecret, "tok"); err != nil {
		t.Fatal(err)
	}
	srv, err := Start(context.Background(), opts)
	if err == nil {
		t.Cleanup(srv.Stop)
	}
	return srv, err
}

func workspaceClient(srv *Server) apiv1connect.WorkspaceServiceClient {
	return apiv1connect.NewWorkspaceServiceClient(&http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", srv.opts.Socket)
		},
	}}, "http://masuda", connect.WithGRPC())
}

// 契約の違うsandboxでは、serveは起動しない。理由に両方のバージョンが入る。
func TestStartRefusesContractMismatch(t *testing.T) {
	_, sock := startSkewedSandbox(t, "deadbeef")
	_, err := startServeWith(t, Options{SandboxSocket: sock, Version: "0.1.0"})
	if err == nil {
		t.Fatal("Start should fail when the sandbox contract differs")
	}
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "9.9.9") || !strings.Contains(err.Error(), "0.1.0") {
		t.Fatalf("err = %v", err)
	}
}

// 起動後にsandboxが入れ替わって契約が違えば、Runはワークスペースを作る前にFailedPreconditionで断る。
// sandboxに届かなければUnavailable。
func TestRunChecksSandboxContract(t *testing.T) {
	sb, sock := startSkewedSandbox(t, sandboxcontract.SHA256)
	srv, err := startServeWith(t, Options{SandboxSocket: sock})
	if err != nil {
		t.Fatal(err)
	}
	ws := workspaceClient(srv)
	repo := newSmokeRepo(t)
	run := func() error {
		_, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/c", Inputs: smokeInputs}))
		return err
	}
	sb.sha.Store("deadbeef")
	if err := run(); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "contract_sha256") {
		t.Fatalf("Run with a mismatched sandbox: %v", err)
	}
	list, err := ws.List(context.Background(), connect.NewRequest(&apiv1.ListWorkspacesRequest{}))
	if err != nil || len(list.Msg.Workspaces) != 0 {
		t.Fatalf("no workspace should be created: %v %v", list, err)
	}
}

func TestStartToleratesUnreachableSandbox(t *testing.T) {
	srv, err := startServeWith(t, Options{SandboxSocket: filepath.Join(t.TempDir(), "absent.sock")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = workspaceClient(srv).Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: newSmokeRepo(t), Workflow: "workflows/smoke", Branch: "feat/u", Inputs: smokeInputs}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("Run without a sandbox: %v", err)
	}
}
