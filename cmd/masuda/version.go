package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/sandboxcontract"
	"github.com/TadahiroYamamura/masuda/serve"
)

// sandboxSocketFlags は`masuda serve`と同じ決め方（--sandbox-socket > config.jsonのsandboxSocket >
// 既定）でsandbox serviceのソケットを引くためのフラグ。version・doctorが使う。
type sandboxSocketFlags struct {
	fs     *flag.FlagSet
	socket *string
	config *string
}

func addSandboxSocketFlags(fs *flag.FlagSet) *sandboxSocketFlags {
	return &sandboxSocketFlags{
		fs:     fs,
		socket: fs.String("sandbox-socket", defaultSandboxSocket(), "masuda-sandbox serveのUDSのパス（明示しなければconfig.jsonのsandboxSocket）"),
		config: fs.String("config", config.ServeConfigPath(), "serve全体の設定ファイル"),
	}
}

func defaultSandboxSocket() string { return filepath.Join(runtimeDir(), "masuda-sandbox.sock") }

// resolve はconfig.jsonを読み、ソケットのパスを決める。config.jsonが読めなければそのエラーも返す
// （ソケットは既定かフラグのものを返す）。
func (f *sandboxSocketFlags) resolve() (string, config.ServeConfig, error) {
	explicit := false
	f.fs.Visit(func(fl *flag.Flag) { explicit = explicit || fl.Name == "sandbox-socket" })
	cfg, err := config.LoadServe(*f.config)
	if err != nil {
		return *f.socket, cfg, err
	}
	if cfg.SandboxSocket != "" && !explicit {
		return cfg.SandboxSocket, cfg, nil
	}
	return *f.socket, cfg, nil
}

func runVersion(args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	sf := addSandboxSocketFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	socket, _, cfgErr := sf.resolve()
	printVersion(os.Stdout, socket, cfgErr)
	return nil
}

func printVersion(w io.Writer, socket string, cfgErr error) {
	fmt.Fprintf(w, "masuda %s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(w, "  sandbox contract sha256: %s\n", sandboxcontract.SHA256)
	if cfgErr != nil {
		fmt.Fprintf(w, "  (config.json: %v)\n", cfgErr)
	}
	info, err := serve.SandboxInfo(context.Background(), serve.DialSandbox(socket))
	if err != nil {
		fmt.Fprintf(w, "masuda-sandbox at %s: %v\n", socket, unwrapConnect(err))
		return
	}
	printServerInfo(w, info)
	if err := serve.CheckContract(info, version); err != nil {
		fmt.Fprintf(w, "  contract: MISMATCH: %v\n", unwrapConnect(err))
	} else {
		fmt.Fprintln(w, "  contract: ok")
	}
}

func printServerInfo(w io.Writer, info *sandboxv1.ServerInfo) {
	fmt.Fprintf(w, "masuda-sandbox %s (%s, gondolin %s)\n", info.Version, info.Platform, info.GondolinVersion)
	fmt.Fprintf(w, "  sandbox contract sha256: %s\n", info.ContractSha256)
}

// unwrapConnect はconnectエラーの「failed_precondition: 」等の頭を落とした理由だけを返す。
func unwrapConnect(err error) error {
	var ce interface{ Message() string }
	if errors.As(err, &ce) {
		return errors.New(ce.Message())
	}
	return err
}
