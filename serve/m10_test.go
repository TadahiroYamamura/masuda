package serve

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

type testClients struct {
	srv       *Server
	ws        apiv1connect.WorkspaceServiceClient
	gates     apiv1connect.GateServiceClient
	workflows apiv1connect.WorkflowServiceClient
	staging   apiv1connect.StagingServiceClient
}

func startClients(t *testing.T, dataDir string, opts Options) testClients {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "masuda.sock")
	opts.Socket, opts.DataDir, opts.FakeSandbox = sock, dataDir, true
	srv, err := Start(context.Background(), opts)
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
	const base = "http://masuda"
	return testClients{
		srv:       srv,
		ws:        apiv1connect.NewWorkspaceServiceClient(httpc, base, connect.WithGRPC()),
		gates:     apiv1connect.NewGateServiceClient(httpc, base, connect.WithGRPC()),
		workflows: apiv1connect.NewWorkflowServiceClient(httpc, base, connect.WithGRPC()),
		staging:   apiv1connect.NewStagingServiceClient(httpc, base, connect.WithGRPC()),
	}
}

const brokenWorkflow = `version: 1
start: a
nodes:
  a: {type: agent, role: agents/nobody, next: end}
`

func TestWorkflowServiceListShowCheck(t *testing.T) {
	cl := startClients(t, t.TempDir(), Options{})
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/workflows/ask.yaml", askWorkflow)
	writeRepoFile(t, repo, ".masuda/agents/asker.md", askAgent)
	ctx := context.Background()

	list, err := cl.workflows.List(ctx, connect.NewRequest(&apiv1.RepoRequest{RepoRoot: repo}))
	if err != nil {
		t.Fatal(err)
	}
	origins := map[string]string{}
	invocable := map[string]bool{}
	for _, w := range list.Msg.Workflows {
		origins[w.Path] = w.Origin
		invocable[w.Path] = w.UserInvocable
	}
	if origins["workflows/ask"] != "repo" || origins["workflows/develop"] != "bundled" {
		t.Fatalf("origins: %v", origins)
	}
	// 一覧から外すかどうかは定義のuser_invocable（省略時true）をそのまま返し、外すのはクライアント。
	if !invocable["workflows/ask"] || !invocable["workflows/develop"] || invocable["workflows/implement/build-step"] {
		t.Fatalf("user_invocable: %v", invocable)
	}
	bundled, err := cl.workflows.List(ctx, connect.NewRequest(&apiv1.RepoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range bundled.Msg.Workflows {
		if w.Path == "workflows/ask" || w.Origin != "bundled" {
			t.Fatalf("without repo_root only bundled workflows: %+v", w)
		}
	}

	show, err := cl.workflows.Show(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: repo, Workflow: "workflows/ask"}))
	if err != nil || !strings.Contains(show.Msg.Mermaid, "ask") {
		t.Fatalf("Show: %v %q", err, show.Msg.GetMermaid())
	}
	if _, err := cl.workflows.Show(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: repo, Workflow: "workflows/nope"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Show of an undefined workflow: %v", err)
	}

	check, err := cl.workflows.Check(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: repo, Workflow: "workflows/ask"}))
	if err != nil || len(check.Msg.Problems) != 0 {
		t.Fatalf("Check of a good workflow: %v %v", err, check.Msg.GetProblems())
	}
	writeRepoFile(t, repo, ".masuda/workflows/broken.yaml", brokenWorkflow)
	check, err = cl.workflows.Check(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: repo, Workflow: "workflows/broken"}))
	if err != nil || len(check.Msg.Problems) == 0 {
		t.Fatalf("Check of a broken workflow: %v %v", err, check.Msg.GetProblems())
	}
	all, err := cl.workflows.Check(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: repo}))
	if err != nil || !slices.ContainsFunc(all.Msg.Problems, func(p *apiv1.Problem) bool { return p.Path == "workflows/broken" }) {
		t.Fatalf("Check of every workflow: %v %v", err, all.Msg.GetProblems())
	}
	writeRepoFile(t, repo, ".masuda/workflows/garbage.yaml", "version: [\n")
	bad, err := cl.workflows.Check(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: repo}))
	if err != nil || len(bad.Msg.Problems) == 0 {
		t.Fatalf("a file that does not load is reported as a problem: %v %v", err, bad.Msg.GetProblems())
	}
	if _, err := cl.workflows.List(ctx, connect.NewRequest(&apiv1.RepoRequest{RepoRoot: repo})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("List with a definition that does not load: %v", err)
	}
}

func TestAttachInfo(t *testing.T) {
	cl := startClients(t, t.TempDir(), Options{})
	repo := newSmokeRepo(t)
	ctx := context.Background()
	res, err := cl.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/chat", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, cl.ws, res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	_, err = cl.ws.AttachInfo(ctx, connect.NewRequest(&apiv1.AttachInfoRequest{Id: res.Msg.Id}))
	if connect.CodeOf(err) != connect.CodeUnimplemented || !strings.Contains(err.Error(), "fake sandbox") {
		t.Fatalf("AttachInfo on the fake sandbox: %v", err)
	}
}

func TestAttachArgvAndKey(t *testing.T) {
	in := []string{"ssh", "-p", "2222", "-i", "/tmp/sandbox/key", "-o", "IdentitiesOnly=yes", "ubuntu@127.0.0.1"}
	got := attachArgv(in, "/data/ws/ssh/id")
	want := []string{"ssh", "-p", "2222", "-i", "/data/ws/ssh/id", "-o", "IdentitiesOnly=yes", "-t", "ubuntu@127.0.0.1", "tmux", "attach", "-t", "claude-work"}
	if !slices.Equal(got, want) {
		t.Fatalf("attachArgv:\n got %q\nwant %q", got, want)
	}
	if in[4] != "/tmp/sandbox/key" {
		t.Fatal("attachArgv must not modify its input")
	}
	noKey := attachArgv([]string{"ssh", "ubuntu@h"}, "/k")
	if !slices.Equal(noKey[:3], []string{"ssh", "-i", "/k"}) {
		t.Fatalf("-i must be added when missing: %q", noKey)
	}

	w := &workspace.Workspace{Dir: t.TempDir()}
	p, err := writeSSHKey(w, []byte("PEM"))
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(w.Dir, "ssh", "id") {
		t.Fatalf("key path %s", p)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode: %v %v", st.Mode(), err)
	}
	if _, err := writeSSHKey(w, nil); err == nil {
		t.Fatal("an empty key must be refused")
	}
}

func TestExportTranscripts(t *testing.T) {
	dataDir := t.TempDir()
	cl := startClients(t, dataDir, Options{})
	repo := newSmokeRepo(t)
	ctx := context.Background()
	res, err := cl.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/tr", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, cl.ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	home := filepath.Join(FakeDir(dataDir), id, "root", "home", "ubuntu", ".claude", "projects")
	writeRepoFile(t, home, "-workspace/s1.jsonl", "{\"a\":1}\n")
	writeRepoFile(t, home, "-workspace/s1/subagents/agent-x.jsonl", "{\"b\":2}\n")
	writeRepoFile(t, home, "-workspace/notes.txt", "not a transcript\n")
	writeRepoFile(t, home, "-workspace/locked.jsonl", "secret\n")
	if err := os.Chmod(filepath.Join(home, "-workspace", "locked.jsonl"), 0); err != nil {
		t.Fatal(err)
	}

	r := cl.srv.backend.runFor(id).runner
	r.ExportTranscripts(ctx)
	if err := r.ExportLog(); err != nil {
		t.Fatal(err)
	}
	exports := filepath.Join(dataDir, "workspaces", id, "exports", "transcripts")
	for rel, want := range map[string]string{"-workspace/s1.jsonl": "{\"a\":1}\n", "-workspace/s1/subagents/agent-x.jsonl": "{\"b\":2}\n"} {
		if b, err := os.ReadFile(filepath.Join(exports, rel)); err != nil || string(b) != want {
			t.Fatalf("transcript %s: %q %v", rel, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(exports, "-workspace", "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("only *.jsonl is exported: %v", err)
	}
	if os.Getuid() != 0 {
		log, _ := os.ReadFile(filepath.Join(dataDir, "workspaces", id, "exports", "execution-log.jsonl"))
		if !strings.Contains(string(log), "export-warning") || !strings.Contains(string(log), "locked.jsonl") {
			t.Fatalf("an unreadable transcript must be recorded in the execution log:\n%s", log)
		}
	}
}

func TestStallAfterFromLocalSettings(t *testing.T) {
	cl := startClients(t, t.TempDir(), Options{})
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.local.json", `{"stallAfter":"300ms"}`)
	ctx := context.Background()
	res, err := cl.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/st", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, cl.ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := cl.ws.Get(ctx, connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: id}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Msg.Activity.GetKind() == apiv1.ActivityKind_ACTIVITY_KIND_STALLED {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("stallAfter in settings.local.json must make a silent run STALLED")
}

func TestStallAfterFlagOverridesLocalSettings(t *testing.T) {
	cl := startClients(t, t.TempDir(), Options{StallAfter: time.Hour})
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.local.json", `{"stallAfter":"1ms"}`)
	ctx := context.Background()
	res, err := cl.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/st", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, cl.ws, res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	time.Sleep(200 * time.Millisecond)
	got, err := cl.ws.Get(ctx, connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: res.Msg.Id}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.Activity.GetKind() == apiv1.ActivityKind_ACTIVITY_KIND_STALLED {
		t.Fatal("--stall-after must override settings.local.json")
	}
}

func TestInvalidStallAfterRefusesRun(t *testing.T) {
	cl := startClients(t, t.TempDir(), Options{})
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.local.json", `{"stallAfter":"soon"}`)
	_, err := cl.ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/st", Inputs: smokeInputs}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "stallAfter") {
		t.Fatalf("Run with an unreadable stallAfter: %v", err)
	}
}

func TestDiskWarning(t *testing.T) {
	dataDir := t.TempDir()
	cl := startClients(t, dataDir, Options{})
	repo := newSmokeRepo(t)
	ctx := context.Background()
	res, err := cl.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/disk", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, cl.ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	if err := os.WriteFile(filepath.Join(dataDir, "workspaces", id, "exports", "big"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	b := cl.srv.backend
	head := b.events.head()
	// statusイベント（活動の変化等）が混ざっても数えないよう、警告だけを拾う。
	warnings := func(id string) []*apiv1.WorkspaceEvent {
		all, _, _ := b.events.since(head, id)
		var out []*apiv1.WorkspaceEvent
		for _, ev := range all {
			if ev.GetNotice().GetKind() == diskWarningKind {
				out = append(out, ev)
			}
		}
		return out
	}
	b.checkDisk()
	if evs := warnings(id); len(evs) != 0 {
		t.Fatalf("under the default threshold nothing is reported: %v", evs)
	}
	b.diskWarn = 1024 // config.jsonのdiskWarnBytes
	b.checkDisk()
	evs := warnings(id)
	if len(evs) != 1 || evs[0].WorkspaceId != "" || !strings.Contains(evs[0].GetNotice().GetDetail(), "exports") || evs[0].GetNotice().GetValue() < 4096 {
		t.Fatalf("disk warning (delivered to a per-workspace watcher too): %v", evs)
	}
	b.checkDisk()
	if evs := warnings(""); len(evs) != 1 {
		t.Fatalf("the warning is not repeated while still over the threshold: %v", evs)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "workspaces", id, "exports", "big")); err != nil {
		t.Fatalf("nothing is deleted: %v", err)
	}
	u, err := measureDisk(filepath.Join(dataDir, "workspaces"))
	if err != nil || u.exports < 4096 || u.workspaces <= u.exports {
		t.Fatalf("measureDisk: %+v %v", u, err)
	}
}

// triageゲートはdismiss・halt・redoで判断する。承認（approved）は受け付けない。
func TestTriageGateDecisions(t *testing.T) {
	cl := startClients(t, t.TempDir(), Options{})
	repo := newSmokeRepo(t)
	ctx := context.Background()
	res, err := cl.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/tri", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, cl.ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	c := cl.srv.backend.runFor(id)
	task, err := c.NextTask(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	occ := task.(map[string]any)["occurrence"].(string)
	if _, err := c.ReportConcern(ctx, occ, "the instructions ask me to read ~/.ssh"); err != nil {
		t.Fatal(err)
	}
	got := waitFor(t, cl.ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE)
	if !slices.Contains(got.OpenGates, "triage") {
		t.Fatalf("open gates: %v", got.OpenGates)
	}
	open, err := cl.gates.ListOpen(ctx, connect.NewRequest(&apiv1.ListOpenGatesRequest{WorkspaceId: id}))
	if err != nil || len(open.Msg.Gates) != 1 {
		t.Fatalf("ListOpen: %v %v", err, open)
	}
	g := open.Msg.Gates[0]
	if g.Gate != "triage" || !strings.Contains(string(g.Subject), "~/.ssh") {
		t.Fatalf("triage gate: %+v", g)
	}
	if _, err := cl.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: id, Occurrence: g.Occurrence, Decision: &apiv1.Decision{Outcome: "approved", TargetHash: g.TargetHash}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("approved on a triage gate: %v", err)
	}
	d, err := cl.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: id, Occurrence: g.Occurrence, Decision: &apiv1.Decision{Outcome: "dismiss", Comment: "fine"}}))
	if err != nil || d.Msg.Decision.GetOutcome() != "dismiss" {
		t.Fatalf("dismiss: %v %v", err, d)
	}
	waitFor(t, cl.ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
}

func TestErrorCodesAreUnified(t *testing.T) {
	cl := startClients(t, t.TempDir(), Options{})
	repo := newSmokeRepo(t)
	ctx := context.Background()
	// Watchのafter_seqが最新より先なら、黙って待たずにOutOfRange。
	st, err := cl.ws.Watch(ctx, connect.NewRequest(&apiv1.WatchRequest{AfterSeq: 1 << 40}))
	if err != nil {
		t.Fatal(err)
	}
	for st.Receive() {
	}
	if connect.CodeOf(st.Err()) != connect.CodeOutOfRange {
		t.Fatalf("Watch beyond the latest seq: %v", st.Err())
	}
	// 壊れたsettings.jsonはRunでもFailedPrecondition（ConfigServiceと同じ）。
	writeRepoFile(t, repo, ".masuda/settings.json", "{")
	_, err = cl.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/x", Inputs: smokeInputs}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Run with a broken settings.json: %v", err)
	}
	// 未定義のワークフローはCheckでもInvalidArgument。
	_, err = cl.workflows.Check(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{Workflow: "workflows/nope"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Check of an undefined workflow: %v", err)
	}
}

// Watchのafter_seqの次のイベントが再送バッファから落ちていればOutOfRangeで断り、
// バッファに残っていれば続きから流す。after_seq: 0はバッファが溢れた後でも通る。
func TestWatchAfterSeqOlderThanBuffer(t *testing.T) {
	cl := startClients(t, t.TempDir(), Options{})
	notice := func() *apiv1.WorkspaceEvent {
		return &apiv1.WorkspaceEvent{Event: &apiv1.WorkspaceEvent_Notice{Notice: &apiv1.ServeNotice{Kind: "test"}}}
	}
	for range eventBufferSize + 5 {
		cl.srv.backend.events.publish(notice())
	}
	oldest, _ := cl.srv.backend.events.window()
	if oldest < 3 {
		t.Fatalf("buffer did not overflow: oldest %d", oldest)
	}
	firstSeq := func(t *testing.T, after uint64) uint64 {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		st, err := cl.ws.Watch(ctx, connect.NewRequest(&apiv1.WatchRequest{AfterSeq: after}))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if !st.Receive() {
			t.Fatalf("Watch after_seq %d ended without an event: %v", after, st.Err())
		}
		return st.Msg().Seq
	}
	t.Run("最古より2つ前のafter_seqはOutOfRangeで終わり、理由に最古のseqが入る", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		st, err := cl.ws.Watch(ctx, connect.NewRequest(&apiv1.WatchRequest{AfterSeq: oldest - 2}))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if st.Receive() {
			t.Fatalf("Watch older than the buffer sent an event: %v", st.Msg())
		}
		if connect.CodeOf(st.Err()) != connect.CodeOutOfRange {
			t.Fatalf("Watch older than the buffer: %v", st.Err())
		}
		if !strings.Contains(st.Err().Error(), fmt.Sprint(oldest)) {
			t.Fatalf("error does not mention the oldest seq %d: %v", oldest, st.Err())
		}
	})
	t.Run("最古ちょうどのafter_seqは通り、その次のイベントから届く", func(t *testing.T) {
		if got := firstSeq(t, oldest); got != oldest+1 {
			t.Fatalf("first seq after %d: got %d", oldest, got)
		}
	})
	t.Run("最古の1つ前のafter_seqは続きがバッファにあるので通り、最古のイベントから届く", func(t *testing.T) {
		if got := firstSeq(t, oldest-1); got != oldest {
			t.Fatalf("first seq after %d: got %d", oldest-1, got)
		}
	})
	t.Run("after_seq 0はバッファが溢れた後でも通る", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		go func() {
			for ctx.Err() == nil {
				cl.srv.backend.events.publish(notice())
				time.Sleep(20 * time.Millisecond)
			}
		}()
		st, err := cl.ws.Watch(ctx, connect.NewRequest(&apiv1.WatchRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if !st.Receive() {
			t.Fatalf("Watch after_seq 0 ended without an event: %v", st.Err())
		}
	})
}

// 引数なしのCheckはrootのワークフローだけを検査する。同梱の部品（implement/build-step等）を
// 単独で検査すると、呼び出し元が用意するデータの欠落が問題として出てしまうため。
func TestCheckWithoutWorkflowChecksRootsOnly(t *testing.T) {
	cl := startClients(t, t.TempDir(), Options{})
	res, err := cl.workflows.Check(context.Background(), connect.NewRequest(&apiv1.ShowWorkflowRequest{}))
	if err != nil || len(res.Msg.Problems) != 0 {
		t.Fatalf("bundled workflows checked as roots: %v %v", err, res.Msg.GetProblems())
	}
}

// engineが記録したBLOCKED（triageのhalt）はStopできるが、Stopの後もResumeは断る。
func TestEngineBlockedCannotBeResumedAfterStop(t *testing.T) {
	cl := startClients(t, t.TempDir(), Options{})
	repo := newSmokeRepo(t)
	ctx := context.Background()
	res, err := cl.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/halt", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, cl.ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	c := cl.srv.backend.runFor(id)
	task, err := c.NextTask(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	occ := task.(map[string]any)["occurrence"].(string)
	if _, err := c.ReportConcern(ctx, occ, "suspicious"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, cl.ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE)
	if _, err := cl.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: id, Occurrence: occ, Decision: &apiv1.Decision{Outcome: "halt"}})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, cl.ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_BLOCKED)
	st, err := cl.ws.Stop(ctx, connect.NewRequest(&apiv1.StopRequest{Id: id}))
	if err != nil || st.Msg.State != apiv1.WorkspaceState_WORKSPACE_STATE_BLOCKED {
		t.Fatalf("Stop of an engine-blocked workspace: %v %v", err, st)
	}
	if _, err := cl.ws.Resume(ctx, connect.NewRequest(&apiv1.ResumeRequest{Id: id})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Resume after Stop of an engine-blocked workspace: %v", err)
	}
}

// config.jsonのlistenがあれば、ループバックでもHTTP+JSONで呼べて、CORSは任意のオリジンを許す。
func TestLoopbackListenWithCORS(t *testing.T) {
	cl := startClients(t, t.TempDir(), Options{Listen: "127.0.0.1:0"})
	addr := cl.srv.ListenAddr()
	if addr == "" {
		t.Fatal("no loopback listener")
	}
	url := "http://" + addr + "/masuda.api.v1.WorkflowService/List"
	pre, _ := http.NewRequest(http.MethodOptions, url, nil)
	pre.Header.Set("Origin", "http://localhost:5173")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	pre.Header.Set("Access-Control-Request-Headers", "content-type,connect-protocol-version")
	res, err := http.DefaultClient.Do(pre)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" ||
		!strings.Contains(res.Header.Get("Access-Control-Allow-Headers"), "connect-protocol-version") {
		t.Fatalf("preflight: %d %v", res.StatusCode, res.Header)
	}
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:5173")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "workflows/develop") || res.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Fatalf("List over loopback: %d %s", res.StatusCode, body)
	}
	// DNS rebindingで別の名前から来た要求は断る。
	req, _ = http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "evil.example:80"
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-loopback Host: %d", res.StatusCode)
	}
	if _, err := Start(context.Background(), Options{Socket: filepath.Join(t.TempDir(), "s.sock"), DataDir: t.TempDir(), FakeSandbox: true, Listen: "0.0.0.0:0"}); err == nil {
		t.Fatal("a non-loopback listen address must be refused")
	}
}
