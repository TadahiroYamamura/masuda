package privileged_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/fakesandbox"
	"github.com/TadahiroYamamura/masuda/internal/privileged"
	"github.com/TadahiroYamamura/masuda/internal/staging"
)

// stub はフェイクのsandboxのRunJobだけを差し替えられるようにしたもの。
type stub struct {
	*fakesandbox.Service
	runJob func(ctx context.Context, req *connect.Request[sandboxv1.RunJobRequest], stream *connect.ServerStream[sandboxv1.RunJobEvent]) error
	got    *sandboxv1.RunJobRequest
}

func (s *stub) RunJob(ctx context.Context, req *connect.Request[sandboxv1.RunJobRequest], stream *connect.ServerStream[sandboxv1.RunJobEvent]) error {
	s.got = req.Msg
	if s.runJob != nil {
		return s.runJob(ctx, req, stream)
	}
	return s.Service.RunJob(ctx, req, stream)
}

type env struct {
	stub     *stub
	client   sandboxv1connect.SandboxServiceClient
	mainRoot string
	opts     privileged.Options
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newEnv はメインのsandbox（/workspaceにtracked.txtと未追跡のbuild/x）と、tracked.txtを含む
// スナップショットのrefを持つstagingを用意する。
func newEnv(t *testing.T, decl config.PrivilegedCommandDecl) *env {
	t.Helper()
	dir := t.TempDir()
	s := &stub{Service: fakesandbox.New(filepath.Join(dir, "fake"))}
	mux := http.NewServeMux()
	mux.Handle(sandboxv1connect.NewSandboxServiceHandler(s))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	client := sandboxv1connect.NewSandboxServiceClient(srv.Client(), srv.URL)
	if _, err := client.CreateSandbox(context.Background(), connect.NewRequest(&sandboxv1.CreateSandboxRequest{Id: "main", DefaultUser: "ubuntu"})); err != nil {
		t.Fatal(err)
	}
	mainRoot := filepath.Join(dir, "fake", "main", "root")
	for rel, content := range map[string]string{"workspace/tracked.txt": "tracked\n", "workspace/build/x": "built\n"} {
		p := filepath.Join(mainRoot, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(dir, "staging")
	git(t, dir, "init", "-q", repo)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "tracked.txt")
	git(t, repo, "commit", "-q", "-m", "snapshot")
	ref := staging.WIPRef("privileged-0001")
	git(t, repo, "update-ref", ref, "HEAD")
	hostDir := filepath.Join(dir, "records", "0001")
	if err := os.MkdirAll(hostDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return &env{stub: s, client: client, mainRoot: mainRoot, opts: privileged.Options{
		Sandbox: client, MainID: "main", RunID: "0001", BuildID: "b1", Egress: []string{"db.example"},
		Decl: decl, Staging: staging.Open(repo), SnapshotRef: ref, HostDir: hostDir,
	}}
}

// canned はRunJobの代わりに決まったイベントを送る。
func canned(evs ...*sandboxv1.RunJobEvent) func(context.Context, *connect.Request[sandboxv1.RunJobRequest], *connect.ServerStream[sandboxv1.RunJobEvent]) error {
	return func(_ context.Context, _ *connect.Request[sandboxv1.RunJobRequest], stream *connect.ServerStream[sandboxv1.RunJobEvent]) error {
		for _, ev := range evs {
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
		return nil
	}
}

func phase(name string) *sandboxv1.RunJobEvent {
	return &sandboxv1.RunJobEvent{Event: &sandboxv1.RunJobEvent_Phase_{Phase: &sandboxv1.RunJobEvent_Phase{Name: name, SandboxId: "job-x"}}}
}
func stdout(s string) *sandboxv1.RunJobEvent {
	return &sandboxv1.RunJobEvent{Event: &sandboxv1.RunJobEvent_Stdout{Stdout: []byte(s)}}
}
func stderr(s string) *sandboxv1.RunJobEvent {
	return &sandboxv1.RunJobEvent{Event: &sandboxv1.RunJobEvent_Stderr{Stderr: []byte(s)}}
}
func finished(f *sandboxv1.RunJobEvent_Finished) *sandboxv1.RunJobEvent {
	return &sandboxv1.RunJobEvent{Event: &sandboxv1.RunJobEvent_Finished_{Finished: f}}
}

var setupOK = &sandboxv1.ExecEvent_Exited{}

func TestRunPassesSnapshotAndInputsThroughRunJob(t *testing.T) {
	e := newEnv(t, config.PrivilegedCommandDecl{
		Command: "id -u > uid.txt; cat tracked.txt build/x > out.txt; echo done",
		Image:   "default", Inputs: []string{"**"}, Outputs: []string{"uid.txt", "out.txt"},
	})
	res, err := privileged.Run(context.Background(), e.opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 && strings.Contains(res.Log, "unshare") {
		t.Skipf("unprivileged user namespaces unavailable here: %s", res.Log)
	}
	t.Run("RunJobにはroot・/workspace・宣言のコマンド・既定の1時間・通信先・メインVMからのinputsを渡す", func(t *testing.T) {
		g := e.stub.got
		if g.User != "root" || g.Cwd != "/workspace" || g.Shell != e.opts.Decl.Command || g.TimeoutMs != 3600_000 || !slices.Equal(g.AllowedHosts, []string{"db.example"}) || g.BuildId != "b1" {
			t.Fatalf("request %v", g)
		}
		from := g.Inputs[1].GetFromSandbox()
		if from.GetId() != "main" || from.Root != "/workspace" || from.DestRoot != "/workspace" || !slices.Equal(from.Patterns, []string{"**"}) {
			t.Fatalf("inputs %v", g.Inputs)
		}
	})
	t.Run("inputsが追跡対象のファイルに当たってもスナップショットを展開してコマンドが動く", func(t *testing.T) {
		if res.ExitCode != 0 || res.Log != "done\n" {
			t.Fatalf("result %+v", res)
		}
	})
	t.Run("outputsはホストのoutputsとメインのゲストの結果の写しの両方に置かれる", func(t *testing.T) {
		if !slices.Equal(res.Outputs, []string{"out.txt", "uid.txt"}) || res.ResultsDir != "/masuda/privileged/0001/" {
			t.Fatalf("result %+v", res)
		}
		host, _ := os.ReadFile(filepath.Join(e.opts.HostDir, "outputs", "out.txt"))
		guest, _ := os.ReadFile(filepath.Join(e.mainRoot, "masuda/privileged/0001/outputs/out.txt"))
		if string(host) != "tracked\nbuilt\n" || string(guest) != string(host) {
			t.Fatalf("host %q guest %q", host, guest)
		}
		code, _ := os.ReadFile(filepath.Join(e.mainRoot, "masuda/privileged/0001/exit-code"))
		hostCode, _ := os.ReadFile(filepath.Join(e.opts.HostDir, "exit-code"))
		if string(code) != "0\n" || string(hostCode) != "0\n" {
			t.Fatalf("exit-code guest %q host %q", code, hostCode)
		}
	})
	t.Run("ホストの一時bundleは終わったら消える", func(t *testing.T) {
		if m, _ := filepath.Glob(filepath.Join(e.opts.HostDir, ".bundle-*")); len(m) != 0 {
			t.Fatalf("left %v", m)
		}
	})
}

func TestRunSetupFailureIsAnError(t *testing.T) {
	e := newEnv(t, config.PrivilegedCommandDecl{Command: "touch /workspace/ran", Image: "default"})
	e.stub.runJob = func(ctx context.Context, req *connect.Request[sandboxv1.RunJobRequest], stream *connect.ServerStream[sandboxv1.RunJobEvent]) error {
		req.Msg.SetupShell = "echo broken-bundle >&2; exit 128"
		return e.stub.Service.RunJob(ctx, req, stream)
	}
	_, err := privileged.Run(context.Background(), e.opts)
	if err == nil || !strings.Contains(err.Error(), "checking out the snapshot") || !strings.Contains(err.Error(), "exit 128") || !strings.Contains(err.Error(), "broken-bundle") {
		t.Fatalf("前処理が失敗したら、その終わり方と出力を添えたエラーを返す: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.opts.HostDir, "exit-code")); err == nil {
		t.Fatal("前処理が失敗したら結果を置かない")
	}
}

func TestRunMapsFinished(t *testing.T) {
	cases := []struct {
		name    string
		outputs []string
		events  []*sandboxv1.RunJobEvent
		check   func(t *testing.T, r *privileged.Result)
	}{
		{
			name:   "setup_shellの出力はログに入れず、shellのstdoutとstderrを届いた順に1本のログにする",
			events: []*sandboxv1.RunJobEvent{phase("setup"), stderr("git-noise"), phase("running"), stdout("a"), stderr("b"), stdout("c"), finished(&sandboxv1.RunJobEvent_Finished{Setup: setupOK, Exited: &sandboxv1.ExecEvent_Exited{ExitCode: 2}})},
			check: func(t *testing.T, r *privileged.Result) {
				if r.Log != "abc" || r.ExitCode != 2 || r.Truncated {
					t.Fatalf("result %+v", r)
				}
			},
		},
		{
			name:   "シグナルで終わり終了コードが0ならexit_codeを-1にする",
			events: []*sandboxv1.RunJobEvent{finished(&sandboxv1.RunJobEvent_Finished{Setup: setupOK, Exited: &sandboxv1.ExecEvent_Exited{Signal: "SIGKILL", TimedOut: true}})},
			check: func(t *testing.T, r *privileged.Result) {
				if r.ExitCode != -1 || r.Signal != "SIGKILL" || !r.TimedOut {
					t.Fatalf("result %+v", r)
				}
			},
		},
		{
			name:    "ジョブ全体の期限が過ぎたらtimed_outにしてexit_codeを-1、回収できなかったoutputsをoutputs_errorに書く",
			outputs: []string{"report.xml"},
			events:  []*sandboxv1.RunJobEvent{finished(&sandboxv1.RunJobEvent_Finished{Setup: setupOK, JobTimedOut: true})},
			check: func(t *testing.T, r *privileged.Result) {
				if r.ExitCode != -1 || !r.TimedOut || !strings.Contains(r.OutputsError, "job deadline") || r.Outputs == nil {
					t.Fatalf("result %+v", r)
				}
			},
		},
		{
			name:    "RunJobのoutputs_errorとoutputsをそのまま返す",
			outputs: []string{"a", "b"},
			events:  []*sandboxv1.RunJobEvent{finished(&sandboxv1.RunJobEvent_Finished{Setup: setupOK, Exited: &sandboxv1.ExecEvent_Exited{}, OutputsError: `no file matched "b"`})},
			check: func(t *testing.T, r *privileged.Result) {
				if r.OutputsError != `no file matched "b"` || r.Outputs == nil || len(r.Outputs) != 0 {
					t.Fatalf("result %+v", r)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, config.PrivilegedCommandDecl{Command: "true", Image: "default", Outputs: c.outputs})
			e.stub.runJob = canned(c.events...)
			r, err := privileged.Run(context.Background(), e.opts)
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, r)
		})
	}
	t.Run("Finishedが届かずにストリームが終わったらエラーにする", func(t *testing.T) {
		e := newEnv(t, config.PrivilegedCommandDecl{Command: "true", Image: "default"})
		e.stub.runJob = canned(phase("running"))
		if _, err := privileged.Run(context.Background(), e.opts); err == nil {
			t.Fatal("no error")
		}
	})
}
