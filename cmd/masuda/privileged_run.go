package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/privileged"
	"github.com/TadahiroYamamura/masuda/internal/staging"
	"github.com/TadahiroYamamura/masuda/serve"
)

const privilegedRunUsage = "privileged-command run <name> [--repo <dir>] [--ref <branch|commit>] [--out <dir>] [--sandbox-socket <path>] [--config <path>] [--data-dir <dir>]"

// exitCodeError はmasudaをこの終了コードで終わらせる。説明は出力済みなので、mainはメッセージを出さない。
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return "exit status " + strconv.Itoa(e.code) }

// 特権コマンドの時間切れの終了コード。timeout(1)と`masuda-sandbox run`に合わせる。
const exitTimedOut = 124

type privilegedRunOptions struct {
	name    string
	repo    string
	ref     string
	out     string
	socket  string
	dataDir string
}

// privilegedRun は`masuda privileged-command run`。masuda serveを通さず、作業ツリーの宣言と承認を
// 照らして、sandbox serviceのRunJobで特権コマンドを1回動かす。終了コードは特権コマンドのもの
// （シグナルなら128+番号、時間切れは124）。masuda自体の失敗は1、使い方の誤りは2。
func privilegedRun(args []string) error {
	fs := flag.NewFlagSet("privileged-command run", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: masuda %s\n", privilegedRunUsage)
		fs.PrintDefaults()
	}
	sf := addSandboxSocketFlags(fs)
	o := privilegedRunOptions{}
	fs.StringVar(&o.repo, "repo", "", "対象リポジトリ（作業ツリーのトップ。省略時は今いる作業ツリーのトップ）")
	fs.StringVar(&o.ref, "ref", "", "作業ツリーの今の状態の代わりに渡すコミット（ブランチ名・コミット）")
	fs.StringVar(&o.out, "out", "", "結果（exit-code・log・outputs/）を置くディレクトリ（既定は新しい一時ディレクトリ）")
	fs.StringVar(&o.dataDir, "data-dir", defaultDataDir(), "masuda serveの状態を置くディレクトリ（イメージのビルドの記録を書く）")
	// 位置引数の後ろにもフラグを書けるよう、clientのparseと同じく位置引数を1つ取るたびに解析し直す。
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return err
			}
			return errUsage
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
	if len(pos) != 1 {
		fs.Usage()
		return errUsage
	}
	o.name = pos[0]
	socket, _, err := sf.resolve()
	if err != nil {
		return err
	}
	o.socket = socket
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code, err := runPrivilegedJob(ctx, o, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return exitCodeError{code}
	}
	return nil
}

// runPrivilegedJob は特権コマンドを動かし、その終了コードを返す。コマンドの出力はstdout・stderrへ
// そのまま流し、イメージのビルドのログと結果の場所はstderrへ出す。
func runPrivilegedJob(ctx context.Context, o privilegedRunOptions, stdout, stderr io.Writer) (int, error) {
	root, err := absRepo(o.repo)
	if err != nil {
		return 0, err
	}
	if err := requireWorkTreeTop(ctx, root); err != nil {
		return 0, err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return 0, err
	}
	local, err := config.LoadLocal(root)
	if err != nil {
		return 0, err
	}
	imagesDir := filepath.Join(root, config.DirName, "images")
	cmd, err := privileged.Resolve(cfg, local, o.name, func(entry string) bool {
		st, err := os.Stat(filepath.Join(imagesDir, entry, "Dockerfile"))
		return err == nil && st.Mode().IsRegular()
	})
	if err != nil {
		return 0, err
	}

	sb := serve.DialSandbox(o.socket)
	info, err := serve.SandboxInfo(ctx, sb)
	if err != nil {
		return 0, fmt.Errorf("masuda-sandbox at %s: %w", o.socket, unwrapConnect(err))
	}
	if err := serve.CheckContract(info, version); err != nil {
		return 0, unwrapConnect(err)
	}

	tmp, err := os.MkdirTemp("", "masuda-privileged-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	snap, err := staging.SnapshotWorktree(ctx, root, filepath.Join(tmp, "snapshot.git"), o.ref)
	if err != nil {
		return 0, fmt.Errorf("taking the tree to pass: %w", err)
	}
	inputs, err := privileged.WorktreeInputs(ctx, root, cmd.Decl.Inputs)
	if err != nil {
		return 0, fmt.Errorf("collecting inputs: %w", err)
	}

	buildID, err := serve.BuildImage(ctx, sb, o.dataDir, root, cmd.Decl.Image, filepath.Join(imagesDir, cmd.Decl.Image), func(line string) error {
		_, err := fmt.Fprintln(stderr, line)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("building image %s: %w", cmd.Decl.Image, unwrapConnect(err))
	}

	out := o.out
	if out == "" {
		if out, err = os.MkdirTemp("", "masuda-privileged-"+o.name+"-"); err != nil {
			return 0, err
		}
	} else if err := os.MkdirAll(out, 0o700); err != nil {
		return 0, err
	}
	res, err := privileged.Run(ctx, privileged.Options{
		Sandbox:     sb,
		BuildID:     buildID,
		DiskMiB:     cfg.DiskMiB(cmd.Decl.Image),
		MemoryMiB:   cfg.MemoryMiB(cmd.Decl.Image),
		CPUs:        cfg.CPUs(cmd.Decl.Image),
		Egress:      cmd.Egress,
		Decl:        cmd.Decl,
		Staging:     snap,
		SnapshotRef: staging.WorktreeSnapshotRef,
		HostDir:     out,
		HostInputs:  inputs,
		Stdout:      stdout,
		Stderr:      stderr,
	})
	if err != nil {
		return 0, unwrapConnect(err)
	}
	if res.OutputsError != "" {
		fmt.Fprintf(stderr, "masuda: outputs: %s\n", res.OutputsError)
	}
	if res.TimedOut {
		fmt.Fprintf(stderr, "masuda: %s timed out\n", o.name)
	} else if res.Signal != "" {
		fmt.Fprintf(stderr, "masuda: %s was killed by %s\n", o.name, res.Signal)
	}
	if len(res.DeniedHosts) > 0 {
		fmt.Fprintln(stderr, "masuda: denied hosts (not in the approved egress of .masuda/settings.json):")
		for _, d := range res.DeniedHosts {
			fmt.Fprintf(stderr, "  %s (%s) x%d\n", d.Host, d.Reason, d.Count)
		}
	}
	fmt.Fprintf(stderr, "masuda: results in %s\n", out)
	return exitCodeOf(res), nil
}

// exitCodeOf は特権コマンドの終わり方を、シェルと同じ形の終了コードにする。
func exitCodeOf(res *privileged.Result) int {
	switch {
	case res.TimedOut:
		return exitTimedOut
	case res.Signal != "":
		return 128 + guestSignalNumber(res.Signal)
	}
	return res.ExitCode
}

// guestSignals はゲスト（Linux）のシグナルの番号。ホストのsyscallの定数を使わないのは、
// ホストがmacOSのとき番号が違うため。
var guestSignals = map[string]int{
	"SIGHUP": 1, "SIGINT": 2, "SIGQUIT": 3, "SIGILL": 4, "SIGTRAP": 5, "SIGABRT": 6, "SIGBUS": 7,
	"SIGFPE": 8, "SIGKILL": 9, "SIGUSR1": 10, "SIGSEGV": 11, "SIGUSR2": 12, "SIGPIPE": 13,
	"SIGALRM": 14, "SIGTERM": 15, "SIGSTKFLT": 16, "SIGCHLD": 17, "SIGCONT": 18, "SIGSTOP": 19,
	"SIGTSTP": 20, "SIGTTIN": 21, "SIGTTOU": 22, "SIGURG": 23, "SIGXCPU": 24, "SIGXFSZ": 25,
	"SIGVTALRM": 26, "SIGPROF": 27, "SIGWINCH": 28, "SIGIO": 29, "SIGPWR": 30, "SIGSYS": 31,
}

// guestSignalNumber はsandboxが返すシグナルの表記（"SIGKILL"、フェイクの"signal 9"）を番号にする。
// 読めなければ0（終了コードは128）。
func guestSignalNumber(s string) int {
	if n, ok := guestSignals[s]; ok {
		return n
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(s, "signal ")); err == nil && n > 0 {
		return n
	}
	return 0
}
