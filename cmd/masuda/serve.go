package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/TadahiroYamamura/masuda/serve"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	socket := fs.String("socket", filepath.Join(runtimeDir(), "masuda.sock"), "公開APIを待ち受けるUDSのパス")
	dataDir := fs.String("data-dir", defaultDataDir(), "状態を置くディレクトリ")
	fake := fs.Bool("fake-sandbox", false, "sandbox serviceの代わりにプロセス内のフェイクを使う（開発・テスト用）")
	sandboxSocket := fs.String("sandbox-socket", filepath.Join(runtimeDir(), "masuda-sandbox.sock"), "masuda-sandbox serveのUDSのパス")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv, err := serve.Start(ctx, serve.Options{
		Socket:        *socket,
		DataDir:       *dataDir,
		FakeSandbox:   *fake,
		SandboxSocket: *sandboxSocket,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "masuda: serving on %s\n", *socket)
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
