package fakesandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
)

// streamWriter はstdout/stderrを1本のサーバーストリームへ流す。ServerStream.Sendは
// 並行呼び出しに対応していないので、2つのパイプからの書き込みをmuで直列化する。
type streamWriter struct {
	mu     *sync.Mutex
	stream *connect.ServerStream[sandboxv1.ExecEvent]
	stderr bool
}

func (w *streamWriter) Write(p []byte) (int, error) {
	data := append([]byte(nil), p...)
	ev := &sandboxv1.ExecEvent{Event: &sandboxv1.ExecEvent_Stdout{Stdout: data}}
	if w.stderr {
		ev = &sandboxv1.ExecEvent{Event: &sandboxv1.ExecEvent_Stderr{Stderr: data}}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.stream.Send(ev); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Exec はコマンドをホストで動かす。userは見ず、どのユーザー指定でもこのプロセスと
// 同じホストユーザーで動く。HOMEとcwdだけをゲストroot下へ写像する。
func (s *Service) Exec(ctx context.Context, req *connect.Request[sandboxv1.ExecRequest], stream *connect.ServerStream[sandboxv1.ExecEvent]) error {
	m := req.Msg
	root, defaultUser, sbEnv, err := s.sandboxFor(m.Id)
	if err != nil {
		return err
	}
	var argv []string
	switch {
	case len(m.Argv) > 0 && m.Shell != "":
		return connect.NewError(connect.CodeInvalidArgument, errors.New("exactly one of argv and shell"))
	case m.Shell != "":
		argv = []string{"/bin/sh", "-c", m.Shell}
	case len(m.Argv) > 0:
		if !filepath.IsAbs(m.Argv[0]) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("argv[0] must be absolute: %q", m.Argv[0]))
		}
		argv = m.Argv
	default:
		return connect.NewError(connect.CodeInvalidArgument, errors.New("exactly one of argv and shell"))
	}
	user := m.User
	if user == "" {
		user = defaultUser
	}
	home, err := hostPath(root, homeOf(user))
	if err != nil {
		return err
	}
	cwd := home
	if m.Cwd != "" {
		if cwd, err = hostPath(root, m.Cwd); err != nil {
			return err
		}
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cwd %q is not a directory in the guest", m.Cwd))
	}

	runCtx := ctx
	if m.TimeoutMs > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(m.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = execEnv(home, user, sbEnv, m.Env)
	// sh -cの子孫まで止めるため、プロセスグループごとkillする。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// 子孫がパイプを握ったまま残っても、Waitが戻らなくならないように打ち切る。
	cmd.WaitDelay = 2 * time.Second
	if len(m.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(m.Stdin)
	}
	mu := &sync.Mutex{}
	cmd.Stdout = &streamWriter{mu: mu, stream: stream}
	// ptyは割り当てないが、実物と同じく出力を1本（stdout）にまとめる。
	cmd.Stderr = &streamWriter{mu: mu, stream: stream, stderr: !m.Pty}

	if err := stream.Send(&sandboxv1.ExecEvent{Event: &sandboxv1.ExecEvent_Started_{Started: &sandboxv1.ExecEvent_Started{}}}); err != nil {
		return err
	}
	runErr := cmd.Run()
	exited := &sandboxv1.ExecEvent_Exited{}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		exited.TimedOut = true
	}
	if ctx.Err() != nil {
		return connect.NewError(connect.CodeCanceled, ctx.Err())
	}
	if cmd.ProcessState == nil {
		return connect.NewError(connect.CodeFailedPrecondition, runErr)
	}
	ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
	switch {
	case ws.Signaled():
		exited.ExitCode = -1
		exited.Signal = signalName(ws.Signal())
	default:
		exited.ExitCode = int32(cmd.ProcessState.ExitCode())
	}
	mu.Lock()
	defer mu.Unlock()
	return stream.Send(&sandboxv1.ExecEvent{Event: &sandboxv1.ExecEvent_Exited_{Exited: exited}})
}

// execEnv はホストの環境を引き継がずに組み立てる。ホストのHOMEやトークン類が
// ゲストのつもりのコマンドへ漏れないよう、PATHだけをホストから取る。
func execEnv(home, user string, layers ...map[string]string) []string {
	env := map[string]string{
		"PATH": os.Getenv("PATH"),
		"HOME": home,
		"USER": user,
	}
	for _, l := range layers {
		for k, v := range l {
			env[k] = v
		}
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	default:
		return fmt.Sprintf("signal %d", int(sig))
	}
}
