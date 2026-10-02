package serve

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"path/filepath"

	"golang.org/x/net/http2"

	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/fakesandbox"
)

// FakeDir はフェイクsandboxがゲストrootを置くディレクトリ（`<DataDir>/fake`）。
// 契約テストはここから`<id>/root/`と`<id>/mcp.port`を引く。
func FakeDir(dataDir string) string { return filepath.Join(dataDir, "fake") }

// connectSandbox はsandbox serviceのクライアントを作る。実物もフェイクも同じ
// Connectクライアントのインターフェースで返し、違いはトランスポートだけにする。
// フェイクをハンドラとして直接呼ばずにConnectを通すのは、ストリームの終わり方や
// エラーコードの写り方まで実物と同じ経路で確かめるため。
func connectSandbox(opts Options) (sandboxv1connect.SandboxServiceClient, func(), error) {
	if opts.FakeSandbox {
		p := fakesandbox.StartInProcess(FakeDir(opts.DataDir))
		return p.Client, p.Close, nil
	}
	if opts.SandboxSocket == "" {
		return nil, nil, errors.New("serve: SandboxSocket is required unless FakeSandbox is set")
	}
	sock := opts.SandboxSocket
	httpc := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	return sandboxv1connect.NewSandboxServiceClient(httpc, "http://masuda-sandbox"), func() {}, nil
}
