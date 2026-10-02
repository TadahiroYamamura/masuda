// Package serve は`masuda serve`の本体。公開API（proto/masuda/api/v1）をConnectで
// UDS上に提供する。gRPC・Connect・HTTP+JSONのどれでも呼べる。
package serve

import (
	"cmp"
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
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
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
	// StallAfter は無活動がこれだけ続いたら活動をSTALLEDにするしきい値。0なら対象リポジトリの
	// settings.local.jsonのstallAfter、それも無ければDefaultStallAfter。0でなければすべての
	// ワークスペースでこれを使う（`masuda serve --stall-after`）。
	StallAfter time.Duration
	// DefaultStallAfter はリポジトリが上書きしないときの無活動のしきい値（config.jsonのstallAfter）。
	// 0なら10分。
	DefaultStallAfter time.Duration
	// DiskWarnBytes はワークスペース置き場の使用量の警告のしきい値（config.jsonのdiskWarnBytes）。
	// 0なら既定（20GiB）。
	DiskWarnBytes int64
	// Listen はUDSに加えてConnectを待ち受けるループバックのアドレス（config.jsonのlisten）。
	// 空ならUDSだけ。CORSは任意のオリジンを許す（serve/listen.go）。
	Listen string
}

// Server は起動中の`masuda serve`。
type Server struct {
	opts     Options
	listener net.Listener
	http     *http.Server
	// tcp・tcpLn はconfig.jsonのlistenのループバックの待ち受け（無ければnil）。
	tcp     *http.Server
	tcpLn   net.Listener
	backend *backend
	// served はhttp.Serveが戻ったら閉じる。doneはStopの後始末まで終わったら閉じる。
	served   chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// backend はAPIハンドラが共有するものと、リクエストより長生きする処理（sandboxの
// 起動等）の寿命。Stopでctxを取り消し、wgで終わりを待つ。
type backend struct {
	store   *workspace.Store
	sandbox sandboxv1connect.SandboxServiceClient
	// dataDir はserveのDataDir。fakeはフェイクsandboxを使っているか（MCPポートの公開と
	// tmuxの起動の有無が変わる）。
	dataDir string
	fake    bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	// closeSandbox はsandboxクライアントの後始末（フェイクならプロセス内サーバーの停止）。
	closeSandbox func()

	runsMu sync.Mutex
	runs   map[string]*runCtl // ワークスペースID → 動いている実行
	hookMu sync.Mutex
	// lifeMu はRun・Resume・Stop・Removeを直列にする。「動いている実行が無いことを確かめて
	// 作る」「止めてから消す」の間に別の操作が割り込まないようにするため。
	lifeMu sync.Mutex

	events *eventBus
	acts   *activities
	// stallOverride はserveの--stall-after（0なら各リポジトリのsettings.local.jsonに従う）。
	// stallDefault はリポジトリが上書きしないときのしきい値（config.json、既定10分）。
	stallOverride time.Duration
	stallDefault  time.Duration
	// diskWarn はディスク使用量の警告のしきい値（config.json、既定20GiB）。
	diskWarn int64

	// diskOver は前回の計測でしきい値を超えていたか（超えたときにだけ警告するため）。
	diskMu   sync.Mutex
	diskOver bool
}

func newBackend(store *workspace.Store, sb sandboxv1connect.SandboxServiceClient, closeSandbox func(), opts Options) *backend {
	ctx, cancel := context.WithCancel(context.Background())
	return &backend{
		store: store, sandbox: sb, dataDir: opts.DataDir, fake: opts.FakeSandbox,
		ctx: ctx, cancel: cancel, closeSandbox: closeSandbox, runs: map[string]*runCtl{},
		events: newEventBus(), acts: newActivities(), stallOverride: max(opts.StallAfter, 0),
		stallDefault: cmp.Or(max(opts.DefaultStallAfter, 0), DefaultStallAfter),
		diskWarn:     cmp.Or(max(opts.DiskWarnBytes, 0), config.DefaultDiskWarnBytes),
	}
}

func (b *backend) allRuns() []*runCtl {
	b.runsMu.Lock()
	defer b.runsMu.Unlock()
	out := make([]*runCtl, 0, len(b.runs))
	for _, c := range b.runs {
		out = append(out, c)
	}
	return out
}

func (b *backend) runFor(id string) *runCtl {
	b.runsMu.Lock()
	defer b.runsMu.Unlock()
	return b.runs[id]
}

func (b *backend) addRun(c *runCtl) {
	b.runsMu.Lock()
	defer b.runsMu.Unlock()
	b.runs[c.id] = c
}

func (b *backend) removeRun(id string) {
	b.runsMu.Lock()
	c := b.runs[id]
	delete(b.runs, id)
	b.runsMu.Unlock()
	if c != nil {
		c.close()
	}
}

// goBackground はfをリクエストと切り離して動かす。fに渡すctxはStopで取り消される。
func (b *backend) goBackground(f func(ctx context.Context)) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		f(b.ctx)
	}()
}

// close はバックグラウンド処理を取り消して待ち、sandboxクライアントを閉じる。
func (b *backend) close() {
	b.cancel()
	b.runsMu.Lock()
	runs := b.runs
	b.runs = map[string]*runCtl{}
	b.runsMu.Unlock()
	for _, c := range runs {
		c.close()
	}
	b.wg.Wait()
	if b.closeSandbox != nil {
		b.closeSandbox()
	}
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
	sb, closeSandbox, err := connectSandbox(opts)
	if err != nil {
		return nil, err
	}
	var tcpLn net.Listener
	if opts.Listen != "" {
		if err := config.CheckLoopback(opts.Listen); err != nil {
			closeSandbox()
			return nil, fmt.Errorf("serve: listen: %w", err)
		}
		if tcpLn, err = net.Listen("tcp", opts.Listen); err != nil {
			closeSandbox()
			return nil, fmt.Errorf("serve: listening on %s: %w", opts.Listen, err)
		}
	}
	ln, err := net.Listen("unix", opts.Socket)
	if err != nil {
		if tcpLn != nil {
			tcpLn.Close()
		}
		closeSandbox()
		return nil, fmt.Errorf("serve: listening on %s: %w", opts.Socket, err)
	}
	b := newBackend(workspace.NewStore(opts.DataDir), sb, closeSandbox, opts)
	if err := b.recoverInterrupted(); err != nil {
		ln.Close()
		if tcpLn != nil {
			tcpLn.Close()
		}
		closeSandbox()
		return nil, err
	}
	b.goBackground(b.patrol)
	b.goBackground(b.watchDisk)

	// UDS上ではTLSが無いので、クライアント・サーバー両方向のストリーミングに要る
	// HTTP/2を平文（h2c）で受ける。HTTP/1.1のHTTP+JSONも同じハンドラで受ける。
	handler := h2c.NewHandler(newMux(b), &http2.Server{})
	s := &Server{
		opts:     opts,
		listener: ln,
		http:     &http.Server{Handler: handler},
		backend:  b,
		served:   make(chan struct{}),
		done:     make(chan struct{}),
	}
	if tcpLn != nil {
		// ブラウザはHTTP/1.1（HTTP+JSON・gRPC-Web）で来るので、UDSと同じh2cのハンドラで両方を受ける。
		s.tcp, s.tcpLn = &http.Server{Handler: loopbackHandler(handler)}, tcpLn
		go func() { _ = s.tcp.Serve(tcpLn) }()
	}
	go func() {
		defer close(s.served)
		_ = s.http.Serve(ln)
	}()
	go func() {
		select {
		case <-ctx.Done():
		case <-s.served:
		}
		s.Stop()
	}()
	return s, nil
}

// Stop は待ち受けを止め、処理中のリクエストを待ってからソケットを消す。何度呼んでもよい。
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if s.tcp != nil {
			if err := s.tcp.Shutdown(ctx); err != nil {
				_ = s.tcp.Close()
			}
		}
		if err := s.http.Shutdown(ctx); err != nil {
			_ = s.http.Close()
		}
		<-s.served
		s.backend.close()
		_ = os.Remove(s.opts.Socket)
		close(s.done)
	})
}

// ListenAddr はループバックの待ち受けの実際のアドレス（ポート0を渡したときに使う）。無ければ空。
func (s *Server) ListenAddr() string {
	if s.tcpLn == nil {
		return ""
	}
	return s.tcpLn.Addr().String()
}

// Done は待ち受けが終わり、Stopの後始末まで済んだら閉じる。
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

func newMux(b *backend) *http.ServeMux {
	store := b.store
	mux := http.NewServeMux()
	mux.Handle(apiv1connect.NewWorkspaceServiceHandler(&workspaceService{store: store, backend: b}))
	mux.Handle(apiv1connect.NewGateServiceHandler(&gateService{store: store, backend: b}))
	mux.Handle(apiv1connect.NewQuestionServiceHandler(&questionService{store: store, backend: b}))
	mux.Handle(apiv1connect.NewStagingServiceHandler(&stagingService{store: store}))
	mux.Handle(apiv1connect.NewConfigServiceHandler(&configService{backend: b}))
	mux.Handle(apiv1connect.NewWorkflowServiceHandler(&workflowService{}))
	return mux
}
