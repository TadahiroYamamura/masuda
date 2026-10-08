package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/fakesandbox"
	"github.com/TadahiroYamamura/masuda/internal/privileged"
)

// recordingSandbox はフェイクのsandboxで、受けたRunJobの要求を残す。
type recordingSandbox struct {
	*fakesandbox.Service
	mu   sync.Mutex
	jobs []*sandboxv1.RunJobRequest
}

func (s *recordingSandbox) RunJob(ctx context.Context, req *connect.Request[sandboxv1.RunJobRequest], stream *connect.ServerStream[sandboxv1.RunJobEvent]) error {
	s.mu.Lock()
	s.jobs = append(s.jobs, req.Msg)
	s.mu.Unlock()
	return s.Service.RunJob(ctx, req, stream)
}

func (s *recordingSandbox) lastJob(t *testing.T) *sandboxv1.RunJobRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) == 0 {
		t.Fatal("no RunJob")
	}
	return s.jobs[len(s.jobs)-1]
}

// startFakeSandbox はフェイクのsandboxをUnixソケットで待ち受け、そのパスを返す。
func startFakeSandbox(t *testing.T) (*recordingSandbox, string) {
	t.Helper()
	sb := &recordingSandbox{Service: fakesandbox.New(t.TempDir())}
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

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// privilegedRepo はdeclsの特権コマンドを宣言したリポジトリを作り、approveの名前を承認しておく。
func privilegedRepo(t *testing.T, decls string, approve ...string) string {
	t.Helper()
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(repo, ".gitignore"), "build/\nsecret.key\n", 0o644)
	writeFile(t, filepath.Join(repo, "tracked.txt"), "committed\n", 0o644)
	writeFile(t, filepath.Join(repo, ".masuda", "images", "default", "Dockerfile"), "FROM ubuntu:24.04\n", 0o644)
	writeFile(t, filepath.Join(repo, ".masuda", "settings.json"), `{"privilegedCommands": {`+decls+`}}`, 0o644)
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-qm", "init")
	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	local := config.LocalSettings{PrivilegedCommandsApproved: map[string]config.PrivilegedCommandApproval{}}
	for _, name := range approve {
		hash, err := config.DeclHash(cfg.PrivilegedCommands[name])
		if err != nil {
			t.Fatal(err)
		}
		local.PrivilegedCommandsApproved[name] = config.PrivilegedCommandApproval{DeclHash: hash}
	}
	if err := config.SaveLocal(repo, local); err != nil {
		t.Fatal(err)
	}
	return repo
}

type runResult struct {
	code           int
	err            error
	stdout, stderr string
}

func runPrivileged(t *testing.T, socket string, o privilegedRunOptions) runResult {
	t.Helper()
	o.socket = socket
	if o.dataDir == "" {
		o.dataDir = t.TempDir()
	}
	if o.out == "" {
		o.out = t.TempDir()
	}
	var stdout, stderr bytes.Buffer
	code, err := runPrivilegedJob(context.Background(), o, &stdout, &stderr)
	return runResult{code, err, stdout.String(), stderr.String()}
}

const showDecl = `"show": {"image": "default", "command": "cat tracked.txt; cat build/in.txt; stat -c %a build/in.txt; test -e secret.key || echo no-secret; test -e build/link || echo no-link; echo to-stderr >&2; exit 3", "inputs": ["build/**"]}`

func TestPrivilegedCommandRun(t *testing.T) {
	sb, socket := startFakeSandbox(t)

	t.Run("承認されていなければsandboxにジョブを出さずに断り、承認のコマンドを案内する", func(t *testing.T) {
		repo := privilegedRepo(t, showDecl)
		r := runPrivileged(t, socket, privilegedRunOptions{name: "show", repo: repo})
		if r.err == nil || !strings.Contains(r.err.Error(), "masuda privileged-command approve show") {
			t.Fatalf("err = %v", r.err)
		}
		if len(sb.jobs) != 0 {
			t.Fatalf("RunJob was called %d times", len(sb.jobs))
		}
	})

	t.Run("--repoで作業ツリーのトップ以外を明示されたらsandboxにジョブを出さずに断る", func(t *testing.T) {
		repo := privilegedRepo(t, showDecl, "show")
		sub := filepath.Join(repo, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		r := runPrivileged(t, socket, privilegedRunOptions{name: "show", repo: sub})
		if r.err == nil || !strings.Contains(r.err.Error(), "is not the top of its work tree") {
			t.Fatalf("err = %v", r.err)
		}
		if len(sb.jobs) != 0 {
			t.Fatalf("RunJob was called %d times", len(sb.jobs))
		}
	})

	repo := privilegedRepo(t, showDecl, "show")
	// 作業ツリー: 追跡しているファイルの未コミットの変更、gitignoreされたinputsのファイル（実行可能）、
	// inputsのglobに当たらないgitignoreされたファイル、globに当たるシンボリックリンク。
	writeFile(t, filepath.Join(repo, "tracked.txt"), "uncommitted\n", 0o644)
	writeFile(t, filepath.Join(repo, "build", "in.txt"), "ignored input\n", 0o750)
	writeFile(t, filepath.Join(repo, "secret.key"), "s\n", 0o600)
	if err := os.Symlink(filepath.Join(repo, "secret.key"), filepath.Join(repo, "build", "link")); err != nil {
		t.Fatal(err)
	}
	refsBefore := gitIn(t, repo, "for-each-ref")
	objectsBefore := gitIn(t, repo, "count-objects", "-v")

	t.Run("作業ツリーの今の状態とgitignoreされたinputsが渡り、出力を流して終了コードを返す", func(t *testing.T) {
		dataDir := t.TempDir()
		out := t.TempDir()
		r := runPrivileged(t, socket, privilegedRunOptions{name: "show", repo: repo, out: out, dataDir: dataDir})
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.code != 3 {
			t.Errorf("code = %d, want 3", r.code)
		}
		if want := "uncommitted\nignored input\n750\nno-secret\nno-link\n"; r.stdout != want {
			t.Errorf("stdout = %q, want %q", r.stdout, want)
		}
		if !strings.Contains(r.stderr, "to-stderr") || !strings.Contains(r.stderr, "results in "+out) {
			t.Errorf("stderr = %q", r.stderr)
		}
		if b, _ := os.ReadFile(filepath.Join(out, "exit-code")); string(b) != "3\n" {
			t.Errorf("exit-code file %q", b)
		}
		var hostInputs []string
		for _, in := range sb.lastJob(t).Inputs {
			if in.GetFromSandbox() != nil {
				t.Errorf("FromSandbox input without a main sandbox: %v", in)
			}
			if hf := in.GetHostFile(); hf != nil && strings.HasPrefix(hf.GuestPath, "/workspace/") {
				hostInputs = append(hostInputs, hf.GuestPath)
			}
		}
		if len(hostInputs) != 1 || hostInputs[0] != "/workspace/build/in.txt" {
			t.Errorf("inputs = %v", hostInputs)
		}
		if m, _ := filepath.Glob(filepath.Join(dataDir, "images", "*", "default.json")); len(m) != 1 {
			t.Errorf("image record in --data-dir: %v", m)
		}
	})

	t.Run("--refを指定すればそのコミットのツリーを渡す", func(t *testing.T) {
		r := runPrivileged(t, socket, privilegedRunOptions{name: "show", repo: repo, ref: "main"})
		if r.err != nil {
			t.Fatal(r.err)
		}
		if !strings.HasPrefix(r.stdout, "committed\n") {
			t.Errorf("stdout = %q", r.stdout)
		}
	})

	t.Run("利用者のリポジトリにrefもオブジェクトも増えない", func(t *testing.T) {
		if got := gitIn(t, repo, "for-each-ref"); got != refsBefore {
			t.Errorf("refs changed:\n%s\n---\n%s", refsBefore, got)
		}
		if got := gitIn(t, repo, "count-objects", "-v"); got != objectsBefore {
			t.Errorf("objects changed:\n%s\n---\n%s", objectsBefore, got)
		}
	})
}

func TestPrivilegedCommandRunExitCodes(t *testing.T) {
	_, socket := startFakeSandbox(t)
	repo := privilegedRepo(t, `"killed": {"image": "default", "command": "kill -KILL $$"},
		"slow": {"image": "default", "command": "sleep 10", "timeoutSeconds": 1}`, "killed", "slow")

	t.Run("シグナルで終わったコマンドは128+番号を返す", func(t *testing.T) {
		r := runPrivileged(t, socket, privilegedRunOptions{name: "killed", repo: repo})
		if r.err != nil || r.code != 128+9 {
			t.Fatalf("code = %d, err = %v, stderr = %q", r.code, r.err, r.stderr)
		}
	})
	t.Run("時間切れは124を返す", func(t *testing.T) {
		r := runPrivileged(t, socket, privilegedRunOptions{name: "slow", repo: repo})
		if r.err != nil || r.code != 124 {
			t.Fatalf("code = %d, err = %v, stderr = %q", r.code, r.err, r.stderr)
		}
	})
}

func TestExitCodeOf(t *testing.T) {
	for _, c := range []struct {
		name string
		res  privileged.Result
		want int
	}{
		{"0で終われば0", privileged.Result{ExitCode: 0}, 0},
		{"0以外で終わればその値", privileged.Result{ExitCode: 5}, 5},
		{"時間切れは終了コードに関わらず124", privileged.Result{ExitCode: -1, TimedOut: true, Signal: "SIGKILL"}, 124},
		{"実物のsandboxのシグナル名は128+Linuxの番号", privileged.Result{ExitCode: -1, Signal: "SIGTERM"}, 143},
		{"フェイクのsignal Nの表記も128+N", privileged.Result{ExitCode: -1, Signal: "signal 10"}, 138},
		{"読めないシグナルは128", privileged.Result{ExitCode: -1, Signal: "SIGWHAT"}, 128},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := exitCodeOf(&c.res); got != c.want {
				t.Fatalf("exitCodeOf = %d, want %d", got, c.want)
			}
		})
	}
}
