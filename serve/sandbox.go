package serve

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/fakesandbox"
	"github.com/TadahiroYamamura/masuda/internal/sandboxcontract"
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

// sandboxInfoTimeout はGetServerInfoの待ち時間。sandbox serviceが起動していれば即座に返るので、
// 届かないときにRunやdoctorを長く待たせない長さにする。
const sandboxInfoTimeout = 5 * time.Second

// DialSandbox はsocketで待ち受けるsandbox serviceのクライアントを作る（`masuda version`・`masuda doctor`用）。
func DialSandbox(socket string) sandboxv1connect.SandboxServiceClient {
	c, _, _ := connectSandbox(Options{SandboxSocket: socket})
	return c
}

// SandboxInfo はsandbox serviceのGetServerInfoを呼ぶ。届かなければUnavailable、GetServerInfoを
// 持たない古いsandboxならFailedPreconditionのconnectエラーを返す。
func SandboxInfo(ctx context.Context, sb sandboxv1connect.SandboxServiceClient) (*sandboxv1.ServerInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, sandboxInfoTimeout)
	defer cancel()
	res, err := sb.GetServerInfo(ctx, connect.NewRequest(&sandboxv1.GetServerInfoRequest{}))
	switch {
	case err == nil:
		return res.Msg, nil
	case connect.CodeOf(err) == connect.CodeUnimplemented:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("masuda-sandbox does not implement GetServerInfo; it is older than this masuda. Install the masuda-sandbox release with the same version as masuda"))
	default:
		msg := err.Error()
		var ce *connect.Error
		if errors.As(err, &ce) {
			msg = ce.Message()
		}
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("sandbox service is not reachable: %s", msg))
	}
}

// CheckContract はinfoのsandboxがmasudaと同じsandbox.protoから作られたかを確かめる。違えば
// FailedPreconditionで、両方のバージョンを理由に含める。
func CheckContract(info *sandboxv1.ServerInfo, masudaVersion string) error {
	if info.ContractSha256 == sandboxcontract.SHA256 {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"masuda-sandbox %s (contract_sha256 %s) was built from a different sandbox.proto than masuda %s (contract_sha256 %s). Install the masuda-sandbox release with the same version as masuda",
		cmp.Or(info.Version, "(unknown version)"), cmp.Or(info.ContractSha256, "(empty)"), cmp.Or(masudaVersion, "dev"), sandboxcontract.SHA256))
}

// checkSandbox はワークスペースの起動前に、sandbox serviceに届き、契約が同じかを確かめる。
func (b *backend) checkSandbox(ctx context.Context) error {
	info, err := SandboxInfo(ctx, b.sandbox)
	if err != nil {
		return err
	}
	return CheckContract(info, b.version)
}
