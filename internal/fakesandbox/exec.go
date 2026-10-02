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

// Exec はコマンドをホストで動かす。root以外のユーザー指定はこのプロセスと同じホストユーザーで
// 動き、HOMEとcwdだけをゲストroot下へ写像する。rootはasRootでゲストrootへchrootして動かす。
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
	env := execEnv(home, user, sbEnv, m.Env)
	if user == "root" {
		guestCwd := homeOf(user)
		if m.Cwd != "" {
			guestCwd = m.Cwd
		}
		if argv, err = asRoot(root, guestCwd, argv); err != nil {
			return err
		}
		cwd = root
		env = execEnv(homeOf(user), user, sbEnv, m.Env)
	}

	runCtx := ctx
	if m.TimeoutMs > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(m.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = env
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

// rootExecScript はユーザー名前空間・マウント名前空間の中で、ホストの/usr・/etc等をゲストrootへ
// bindしてからchrootし、ゲストのcwdでargvを動かす。$1がゲストroot、$2がゲストのcwd。
//
// rootの指定（特権サンドボックス）だけをこう動かすのは、特権コマンドが宣言のコマンド文字列に
// `/workspace/...`のような絶対パスを書き、`id -u`が0であることを期待するため。cwdの写像だけでは
// どちらも満たせない。ゲストrootの下に作るマウントポイント（空のディレクトリ・シンボリックリンク）は
// 名前空間が消えれば中身が見えなくなるだけで、ホストに残っても害は無い。
const rootExecScript = `set -e
r="$1"; cwd="$2"; shift 2
for n in usr etc dev opt var run; do
  [ -d "/$n" ] || continue
  mkdir -p "$r/$n"
  mount --rbind "/$n" "$r/$n"
done
for n in bin sbin lib lib32 lib64 libx32; do
  if [ -L "/$n" ]; then
    [ -e "$r/$n" ] || [ -L "$r/$n" ] || ln -s "$(readlink "/$n")" "$r/$n"
  elif [ -d "/$n" ]; then
    mkdir -p "$r/$n"
    mount --rbind "/$n" "$r/$n"
  fi
done
exec chroot "$r" /bin/sh -c 'cd "$1" && shift && exec "$@"' sh "$cwd" "$@"
`

// asRoot はargvを`unshare -Urm`（名前空間の中でuid 0）経由でゲストrootにchrootして動かす形にする。
// unshareが無い・非特権のユーザー名前空間が禁止されているホストでは、実行時にエラーになる。
func asRoot(root, guestCwd string, argv []string) ([]string, error) {
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("fake sandbox: running as root needs unshare(1): %w", err))
	}
	return append([]string{unshare, "-Urm", "/bin/sh", "-c", rootExecScript, "sh", root, guestCwd}, argv...), nil
}
