package serve

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
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

func newTestAPI(t *testing.T) (apiv1connect.WorkspaceServiceClient, apiv1connect.StagingServiceClient, string) {
	t.Helper()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte(strings.Repeat("line\n", 50000)), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "init")
	dataDir := t.TempDir()
	fake := fakesandbox.StartInProcess(FakeDir(dataDir))
	b := newBackend(workspace.NewStore(dataDir), fake.Client, fake.Close)
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
	cases := []struct {
		req  *apiv1.RunRequest
		code connect.Code
	}{
		{&apiv1.RunRequest{RepoRoot: "rel", Workflow: "w", Branch: "b"}, connect.CodeInvalidArgument},
		{&apiv1.RunRequest{RepoRoot: repo, Workflow: "w"}, connect.CodeInvalidArgument},
		{&apiv1.RunRequest{RepoRoot: filepath.Join(repo, "sub"), Workflow: "w", Branch: "b"}, connect.CodeInvalidArgument},
		{&apiv1.RunRequest{RepoRoot: repo, Workflow: "w", Branch: "b", Base: "nope"}, connect.CodeInvalidArgument},
		{&apiv1.RunRequest{RepoRoot: repo, Workflow: "w", Branch: "bad..name"}, connect.CodeInvalidArgument},
		{&apiv1.RunRequest{RepoRoot: repo, Workflow: "w", Branch: "taken"}, connect.CodeAlreadyExists},
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
	run, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "w", Branch: "feat/x"}))
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

func TestRunBootFailureBlocksWorkspace(t *testing.T) {
	ws, _, repo := newTestAPI(t)
	ctx := context.Background()
	_ = os.MkdirAll(filepath.Join(repo, ".masuda/agents"), 0o755)
	if err := os.WriteFile(filepath.Join(repo, ".masuda/agents/.hidden.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "bad agent")
	res, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "w", Branch: "feat/x"}))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, err := ws.Get(ctx, connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: res.Msg.Id}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Msg.State == apiv1.WorkspaceState_WORKSPACE_STATE_BLOCKED {
			if !strings.Contains(got.Msg.Reason, ".hidden.md") {
				t.Fatalf("reason %q", got.Msg.Reason)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("workspace did not become BLOCKED")
}
