package serve

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

func startServe(t *testing.T, dataDir string) (*Server, apiv1connect.WorkspaceServiceClient) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "masuda.sock")
	srv, err := Start(context.Background(), Options{Socket: sock, DataDir: dataDir, FakeSandbox: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	httpc := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	return srv, apiv1connect.NewWorkspaceServiceClient(httpc, "http://masuda", connect.WithGRPC())
}

func waitFor(t *testing.T, ws apiv1connect.WorkspaceServiceClient, id string, want apiv1.WorkspaceState) *apiv1.Workspace {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last *apiv1.Workspace
	for time.Now().Before(deadline) {
		res, err := ws.Get(context.Background(), connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: id}))
		if err != nil {
			t.Fatal(err)
		}
		if last = res.Msg; last.State == want {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("workspace %s did not reach %v; last %v", id, want, last)
	return nil
}

func newSmokeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDockerfile(t, repo)
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "init")
	return repo
}

// serveを再起動すると、動いていたワークスペースはSTOPPEDとして一覧に出て、Resumeで続けられる。
func TestRestartListsRunningAsStoppedAndResumes(t *testing.T) {
	dataDir := t.TempDir()
	repo := newSmokeRepo(t)
	srv, ws := startServe(t, dataDir)
	res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/r", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	srv.Stop()

	_, ws = startServe(t, dataDir)
	list, err := ws.List(context.Background(), connect.NewRequest(&apiv1.ListWorkspacesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Workspaces) != 1 || list.Msg.Workspaces[0].State != apiv1.WorkspaceState_WORKSPACE_STATE_STOPPED {
		t.Fatalf("after restart: %v", list.Msg.Workspaces)
	}
	if _, err := ws.Resume(context.Background(), connect.NewRequest(&apiv1.ResumeRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	got := waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	if got.Position == "" {
		t.Fatalf("Resume should report the engine position: %v", got)
	}
}

// Removeはexports以外を消す。動いている間はforce無しでは消さない。
func TestRemoveKeepsExports(t *testing.T) {
	dataDir := t.TempDir()
	repo := newSmokeRepo(t)
	_, ws := startServe(t, dataDir)
	res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/x", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	if _, err := ws.Remove(context.Background(), connect.NewRequest(&apiv1.RemoveRequest{Id: id})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Remove of a running workspace without force: %v", err)
	}
	exported := filepath.Join(dataDir, "workspaces", id, "exports", "note")
	if err := os.WriteFile(exported, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Remove(context.Background(), connect.NewRequest(&apiv1.RemoveRequest{Id: id, Force: true})); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Get(context.Background(), connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: id})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("Get after Remove: %v", err)
	}
	if _, err := os.Stat(exported); err != nil {
		t.Fatalf("exports must be kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "workspaces", id, "staging.git")); !os.IsNotExist(err) {
		t.Fatalf("staging must be removed: %v", err)
	}
}

func TestActivityKinds(t *testing.T) {
	a := newActivities()
	w := &workspace.Workspace{Meta: workspace.Meta{ID: "w", State: workspace.StateRunning}}
	now := time.Now().UTC()
	kind := func(at time.Time) apiv1.ActivityKind { return a.compute(w, 10*time.Minute, at).Kind }

	a.reset("w")
	if k := kind(now); k != apiv1.ActivityKind_ACTIVITY_KIND_WORKING {
		t.Fatalf("fresh run: %v", k)
	}
	a.update("w", func(act *activity) { act.inputWait = "idle" })
	if k := kind(now); k != apiv1.ActivityKind_ACTIVITY_KIND_WAITING_INPUT {
		t.Fatalf("idle prompt: %v", k)
	}
	a.update("w", func(act *activity) {
		act.inflight[1] = &inflightReq{http: &apiv1.HttpActivity{}, started: now}
		act.touch("")
	})
	if k := kind(now.Add(time.Minute)); k != apiv1.ActivityKind_ACTIVITY_KIND_WORKING {
		t.Fatalf("request in flight: %v", k)
	}
	// 終わりの来ないリクエスト（応答前にクライアントが切ったもの）は進行中に数えない。
	a.update("w", func(act *activity) { act.inputWait = "idle" })
	if k := kind(now.Add(inflightStale + time.Second)); k != apiv1.ActivityKind_ACTIVITY_KIND_WAITING_INPUT {
		t.Fatalf("a request without its finish must not hide the input wait: %v", k)
	}
	a.update("w", func(act *activity) { act.inputWait = "" })
	if k := kind(time.Now().Add(11 * time.Minute)); k != apiv1.ActivityKind_ACTIVITY_KIND_STALLED {
		t.Fatalf("silent past the threshold: %v", k)
	}
	a.update("w", func(act *activity) { act.dead = true })
	if k := kind(now); k != apiv1.ActivityKind_ACTIVITY_KIND_DEAD {
		t.Fatalf("dead: %v", k)
	}
	w.State = workspace.StateWaitingGate
	if k := kind(now); k != apiv1.ActivityKind_ACTIVITY_KIND_WAITING_GATE {
		t.Fatalf("gate: %v", k)
	}
	w.State = workspace.StateStopped
	if k := kind(now); k != apiv1.ActivityKind_ACTIVITY_KIND_IDLE {
		t.Fatalf("stopped: %v", k)
	}
}
