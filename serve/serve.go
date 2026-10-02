// Package serve は`masuda serve`の本体。公開API（proto/masuda/api/v1）をConnectで
// UDS上に提供する。gRPC・Connect・HTTP+JSONのどれでも呼べる。
package serve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
)

// Options は`masuda serve`の起動設定。
type Options struct {
	// Socket は公開APIを待ち受けるUDSのパス。
	Socket string
	// DataDir はワークスペース等の状態を置くディレクトリ（既定は$XDG_DATA_HOME/masuda）。
	DataDir string
	// FakeSandbox はsandbox serviceの代わりにプロセス内のフェイクを使う（契約テスト用）。
	FakeSandbox bool
	// SandboxSocket は`masuda-sandbox serve`のUDSのパス。FakeSandboxのときは使わない。
	SandboxSocket string
}

// Server は起動中の`masuda serve`。
type Server struct {
	opts     Options
	listener net.Listener
	http     *http.Server
	done     chan struct{}
	stopOnce sync.Once
}

// Start はOptions.Socketで待ち受けを始めて戻る。ctxが終わるとStopする。
func Start(ctx context.Context, opts Options) (*Server, error) {
	if opts.Socket == "" {
		return nil, errors.New("serve: Socket is required")
	}
	if opts.DataDir == "" {
		return nil, errors.New("serve: DataDir is required")
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("serve: creating data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(opts.Socket), 0o700); err != nil {
		return nil, fmt.Errorf("serve: creating socket dir: %w", err)
	}
	if err := removeStaleSocket(opts.Socket); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", opts.Socket)
	if err != nil {
		return nil, fmt.Errorf("serve: listening on %s: %w", opts.Socket, err)
	}

	// UDS上ではTLSが無いので、クライアント・サーバー両方向のストリーミングに要る
	// HTTP/2を平文（h2c）で受ける。HTTP/1.1のHTTP+JSONも同じハンドラで受ける。
	s := &Server{
		opts:     opts,
		listener: ln,
		http:     &http.Server{Handler: h2c.NewHandler(newMux(), &http2.Server{})},
		done:     make(chan struct{}),
	}
	go func() {
		defer close(s.done)
		_ = s.http.Serve(ln)
	}()
	go func() {
		select {
		case <-ctx.Done():
			s.Stop()
		case <-s.done:
		}
	}()
	return s, nil
}

// Stop は待ち受けを止め、処理中のリクエストを待ってからソケットを消す。何度呼んでもよい。
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.http.Shutdown(ctx); err != nil {
			_ = s.http.Close()
		}
		<-s.done
		_ = os.Remove(s.opts.Socket)
	})
}

// Done は待ち受けが終わったら閉じる。
func (s *Server) Done() <-chan struct{} { return s.done }

// removeStaleSocket は前回のプロセスが残したソケットファイルを消す。誰かが
// まだ待ち受けているなら、別の`masuda serve`のソケットを奪わないようエラーにする。
func removeStaleSocket(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		conn.Close()
		return fmt.Errorf("serve: %s is already being served", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("serve: removing stale socket %s: %w", path, err)
	}
	return nil
}

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(apiv1connect.NewWorkspaceServiceHandler(&workspaceService{}))
	mux.Handle(apiv1connect.NewGateServiceHandler(apiv1connect.UnimplementedGateServiceHandler{}))
	mux.Handle(apiv1connect.NewQuestionServiceHandler(apiv1connect.UnimplementedQuestionServiceHandler{}))
	mux.Handle(apiv1connect.NewStagingServiceHandler(apiv1connect.UnimplementedStagingServiceHandler{}))
	mux.Handle(apiv1connect.NewConfigServiceHandler(apiv1connect.UnimplementedConfigServiceHandler{}))
	mux.Handle(apiv1connect.NewWorkflowServiceHandler(apiv1connect.UnimplementedWorkflowServiceHandler{}))
	return mux
}
