package serve

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/fakesandbox"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// smokeInputs は同梱のworkflows/smokeが求める入力。
var smokeInputs = map[string][]byte{"instructions": []byte("x")}

// writeDockerfile はRunが要るイメージのエントリ（.masuda/images/default/Dockerfile）を置く。
func writeDockerfile(t *testing.T, repo string) {
	t.Helper()
	dir := filepath.Join(repo, ".masuda", "images", "default")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM ubuntu:24.04\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestAPI(t *testing.T) (apiv1connect.WorkspaceServiceClient, apiv1connect.StagingServiceClient, string) {
	t.Helper()
	return newTestAPIWith(t, nil)
}

// newTestAPIWith はwrapでsandboxクライアントを差し替えられるnewTestAPI。
func newTestAPIWith(t *testing.T, wrap func(sandboxv1connect.SandboxServiceClient) sandboxv1connect.SandboxServiceClient) (apiv1connect.WorkspaceServiceClient, apiv1connect.StagingServiceClient, string) {
	t.Helper()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte(strings.Repeat("line\n", 50000)), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDockerfile(t, repo)
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "init")
	dataDir := t.TempDir()
	fake := fakesandbox.StartInProcess(FakeDir(dataDir))
	var client sandboxv1connect.SandboxServiceClient = fake.Client
	if wrap != nil {
		client = wrap(client)
	}
	b := newBackend(workspace.NewStore(dataDir), client, fake.Close, Options{DataDir: dataDir, FakeSandbox: true})
	srv := httptest.NewServer(newMux(b))
	t.Cleanup(func() {
		srv.Close()
		b.close()
	})
	return apiv1connect.NewWorkspaceServiceClient(srv.Client(), srv.URL),
		apiv1connect.NewStagingServiceClient(srv.Client(), srv.URL), repo
}

func TestRunRejectsBadRequests(t *testing.T) {
	ws, _, repo := newTestAPI(t)
	ctx := context.Background()
	gitT(t, repo, "branch", "taken")
	_ = os.Mkdir(filepath.Join(repo, "sub"), 0o755)
	pub := "version: 1\ninputs: [instructions]\nstart: echo\nnodes:\n" +
		"  echo: {type: agent, role: agents/echo, inputs: [instructions], outputs: [echo], next: done}\n" +
		"  done: {type: publish, target: local, next: end}\n"
	if err := os.MkdirAll(filepath.Join(repo, ".masuda", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".masuda", "workflows", "pub.yaml"), []byte(pub), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		req  *apiv1.RunRequest
		code connect.Code
	}{
		{&apiv1.RunRequest{RepoRoot: "rel", Workflow: "workflows/smoke", Branch: "b", Inputs: smokeInputs}, connect.CodeInvalidArgument},
		{&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke"}, connect.CodeInvalidArgument},
		{&apiv1.RunRequest{RepoRoot: filepath.Join(repo, "sub"), Workflow: "workflows/smoke", Branch: "b", Inputs: smokeInputs}, connect.CodeInvalidArgument},
		{&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "b", Base: "nope", Inputs: smokeInputs}, connect.CodeInvalidArgument},
		{&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "bad..name", Inputs: smokeInputs}, connect.CodeInvalidArgument},
		// publishするワークフローだけが既存のブランチを断る（smokeはdiscardで終わるので通る）。
		{&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/pub", Branch: "taken", Inputs: smokeInputs}, connect.CodeAlreadyExists},
		{&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/nope", Branch: "b", Inputs: smokeInputs}, connect.CodeInvalidArgument},
		{&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "b"}, connect.CodeInvalidArgument},
	}
	for _, c := range cases {
		if _, err := ws.Run(ctx, connect.NewRequest(c.req)); connect.CodeOf(err) != c.code {
			t.Errorf("Run(%+v) = %v, want %v", c.req, err, c.code)
		}
	}
	// 失敗したRunはワークスペースを残さない。
	l, err := ws.List(ctx, connect.NewRequest(&apiv1.ListWorkspacesRequest{}))
	if err != nil || len(l.Msg.Workspaces) != 0 {
		t.Fatalf("List after failed runs: %v %+v", err, l)
	}
}

func TestStagingRPCs(t *testing.T) {
	ws, st, repo := newTestAPI(t)
	ctx := context.Background()
	run, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/x", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := run.Msg.Id
	if run.Msg.Base != "main" || run.Msg.State != apiv1.WorkspaceState_WORKSPACE_STATE_STARTING {
		t.Fatalf("Run: %+v", run.Msg)
	}
	l, _ := ws.List(ctx, connect.NewRequest(&apiv1.ListWorkspacesRequest{RepoRoot: repo}))
	if len(l.Msg.Workspaces) != 1 || l.Msg.Workspaces[0].Id != id {
		t.Fatalf("List: %+v", l.Msg)
	}
	if l, _ := ws.List(ctx, connect.NewRequest(&apiv1.ListWorkspacesRequest{RepoRoot: "/elsewhere"})); len(l.Msg.Workspaces) != 0 {
		t.Fatalf("List for another repo: %+v", l.Msg)
	}

	// GetBlobは複数チャンクに分けて全体を返す。
	stream, err := st.GetBlob(ctx, connect.NewRequest(&apiv1.GetBlobRequest{WorkspaceId: id, Rev: "refs/masuda/base", Path: "README.md"}))
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	chunks := 0
	for stream.Receive() {
		got = append(got, stream.Msg().Data...)
		chunks++
	}
	if stream.Err() != nil || string(got) != strings.Repeat("line\n", 50000) || chunks < 2 {
		t.Fatalf("GetBlob: err=%v len=%d chunks=%d", stream.Err(), len(got), chunks)
	}
	stream, _ = st.GetBlob(ctx, connect.NewRequest(&apiv1.GetBlobRequest{WorkspaceId: id, Rev: "refs/masuda/base", Path: "nope"}))
	for stream.Receive() {
	}
	if connect.CodeOf(stream.Err()) != connect.CodeNotFound {
		t.Fatalf("GetBlob missing: %v", stream.Err())
	}

	d, err := st.Diff(ctx, connect.NewRequest(&apiv1.DiffRequest{WorkspaceId: id, To: "refs/masuda/base"}))
	if err != nil || !strings.Contains(d.Msg.Unified, "+line") {
		t.Fatalf("Diff of root commit: %v", err)
	}
	if _, err := st.GetCommit(ctx, connect.NewRequest(&apiv1.GetCommitRequest{WorkspaceId: id, Rev: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetCommit(nope): %v", err)
	}
	if _, err := st.ListRefs(ctx, connect.NewRequest(&apiv1.ListRefsRequest{WorkspaceId: "../x"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("ListRefs bad id: %v", err)
	}

	// コメントはrefで付けてもハッシュで保存され、そのハッシュで引ける。
	c, err := st.AddComment(ctx, connect.NewRequest(&apiv1.AddCommentRequest{WorkspaceId: id, Commit: "refs/heads/feat/x", Path: "README.md", Line: 2, Body: "why?"}))
	if err != nil {
		t.Fatal(err)
	}
	base := gitT(t, repo, "rev-parse", "main")
	if c.Msg.Commit != base || c.Msg.Author != "human" || c.Msg.Id == "" {
		t.Fatalf("AddComment: %+v", c.Msg)
	}
	cs, err := st.ListComments(ctx, connect.NewRequest(&apiv1.ListCommentsRequest{WorkspaceId: id, Commit: "refs/masuda/base"}))
	if err != nil || len(cs.Msg.Comments) != 1 || cs.Msg.Comments[0].Body != "why?" {
		t.Fatalf("ListComments: %v %+v", err, cs)
	}
	if _, err := st.AddComment(ctx, connect.NewRequest(&apiv1.AddCommentRequest{WorkspaceId: id, Commit: "refs/masuda/base"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty comment: %v", err)
	}
}

// failingCreate はCreateSandboxだけが失敗するsandboxクライアント。
type failingCreate struct {
	sandboxv1connect.SandboxServiceClient
}

func (failingCreate) CreateSandbox(context.Context, *connect.Request[sandboxv1.CreateSandboxRequest]) (*connect.Response[sandboxv1.Sandbox], error) {
	return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("no room for another VM"))
}

func TestRunBootFailureSuspendsWorkspace(t *testing.T) {
	ws, _, repo := newTestAPIWith(t, func(c sandboxv1connect.SandboxServiceClient) sandboxv1connect.SandboxServiceClient {
		return failingCreate{c}
	})
	ctx := context.Background()
	res, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/x", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, err := ws.Get(ctx, connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: res.Msg.Id}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Msg.State == apiv1.WorkspaceState_WORKSPACE_STATE_SUSPENDED {
			if !strings.Contains(got.Msg.Reason, "no room for another VM") {
				t.Fatalf("reason %q", got.Msg.Reason)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("workspace did not become SUSPENDED")
}

// failingFirstCreate は最初のCreateSandboxだけが失敗するsandboxクライアント。
type failingFirstCreate struct {
	sandboxv1connect.SandboxServiceClient
	calls *atomic.Int32
}

func (f failingFirstCreate) CreateSandbox(ctx context.Context, req *connect.Request[sandboxv1.CreateSandboxRequest]) (*connect.Response[sandboxv1.Sandbox], error) {
	if f.calls.Add(1) == 1 {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("no room for another VM"))
	}
	return f.SandboxServiceClient.CreateSandbox(ctx, req)
}

// 起動に失敗してSUSPENDEDになったワークスペースは、Stopを挟まずにResumeできる。
func TestResumeAfterBootFailureWithoutStop(t *testing.T) {
	var calls atomic.Int32
	ws, _, repo := newTestAPIWith(t, func(c sandboxv1connect.SandboxServiceClient) sandboxv1connect.SandboxServiceClient {
		return failingFirstCreate{c, &calls}
	})
	ctx := context.Background()
	res, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/x", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_SUSPENDED)
	if _, err := ws.Resume(ctx, connect.NewRequest(&apiv1.ResumeRequest{Id: id})); err != nil {
		t.Fatalf("Resume of a workspace whose boot failed: %v", err)
	}
	got := waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	if got.Reason != "" {
		t.Fatalf("reason must be cleared after a successful resume: %q", got.Reason)
	}
}
