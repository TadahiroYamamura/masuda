package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/serve"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	socket := fs.String("socket", filepath.Join(runtimeDir(), "masuda.sock"), "Unix socket to serve the public API on")
	dataDir := fs.String("data-dir", defaultDataDir(), "directory for the state")
	fake := fs.Bool("fake-sandbox", false, "use an in-process fake instead of the sandbox service (for development and tests)")
	sandboxSocket := fs.String("sandbox-socket", defaultSandboxSocket(), "Unix socket of masuda-sandbox serve")
	stallAfter := fs.Duration("stall-after", 0, "mark the activity stalled after this much inactivity (0: stallAfter of the repository's settings.local.json, else of config.json, default 10m)")
	configPath := fs.String("config", config.ServeConfigPath(), "serve-wide config file (listen, sandboxSocket, stallAfter, diskWarnBytes); all defaults when missing")
	logFile := fs.String("log-file", "", "log file (default <data-dir>/logs/masuda-serve.log; the previous one is kept as .1 on each start); - for stderr")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	cfg, err := config.LoadServe(*configPath)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	// 明示したフラグはconfig.jsonより優先する。
	if cfg.SandboxSocket != "" && !set["sandbox-socket"] {
		*sandboxSocket = cfg.SandboxSocket
	}
	defaultStall, _ := cfg.StallAfterDuration() // LoadServeが検査済み

	// 端末には起動したことと、ログの置き場所だけを出す。serveの中には標準エラー出力へ直接書く所もあるので、
	// 標準のロガーとos.Stderrの両方をログのファイルに向ける。
	terminal := os.Stderr
	logPath, logF := openServeLog(*logFile, *dataDir, warnTo(terminal))
	if logF != nil {
		defer logF.Close()
		log.SetOutput(logF)
		os.Stderr = logF
		// 起動に失敗したときのエラーは、mainが標準エラー出力に書く。端末で見えるよう戻してから返す。
		defer func() {
			os.Stderr = terminal
			log.SetOutput(terminal)
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv, err := serve.Start(ctx, serve.Options{
		Socket:            *socket,
		DataDir:           *dataDir,
		FakeSandbox:       *fake,
		SandboxSocket:     *sandboxSocket,
		StallAfter:        *stallAfter,
		DefaultStallAfter: defaultStall,
		DiskWarnBytes:     cfg.DiskWarnBytes,
		Listen:            cfg.Listen,
		Version:           version,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(terminal, "masuda: serving on %s\n", *socket)
	if a := srv.ListenAddr(); a != "" {
		fmt.Fprintf(terminal, "masuda: also serving on http://%s (loopback, CORS: any origin)\n", a)
	}
	if logPath != "" {
		fmt.Fprintf(terminal, "masuda: logs: %s\n", logPath)
	}
	<-srv.Done()
	return nil
}

// runtimeDir はXDG_RUNTIME_DIRを返す。WSL2等で未設定の環境もあるので、そのときは
// 一時ディレクトリ配下のユーザー別ディレクトリへ落とす（ソケットは同じユーザーの
// プロセスだけが信頼の範囲なので、ユーザーごとに分ける）。
func runtimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("masuda-%d", os.Getuid()))
}

func defaultDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "masuda")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), fmt.Sprintf("masuda-%d", os.Getuid()), "data")
	}
	return filepath.Join(home, ".local", "share", "masuda")
}
