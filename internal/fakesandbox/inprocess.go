package fakesandbox

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
)

// InProcess はServiceをプロセス内のHTTP/2サーバーに載せ、それに繋がるConnect
// クライアントを提供する。ソケットファイルを使わずnet.Pipeで繋ぐのは、DataDirが
// 深い場所にあるとUDSのパス長の上限（108バイト）を超えうるため。
type InProcess struct {
	Service *Service
	Client  sandboxv1connect.SandboxServiceClient

	ln   *pipeListener
	srv  *http.Server
	done chan struct{}
}

// StartInProcess はdir（`<DataDir>/fake`）を使うフェイクを起動する。
func StartInProcess(dir string) *InProcess {
	svc := New(dir)
	mux := http.NewServeMux()
	mux.Handle(sandboxv1connect.NewSandboxServiceHandler(svc))
	ln := newPipeListener()
	p := &InProcess{
		Service: svc,
		ln:      ln,
		srv:     &http.Server{Handler: h2c.NewHandler(mux, &http2.Server{})},
		done:    make(chan struct{}),
	}
	go func() {
		defer close(p.done)
		_ = p.srv.Serve(ln)
	}()
	httpc := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
			return ln.DialContext(ctx)
		},
	}}
	p.Client = sandboxv1connect.NewSandboxServiceClient(httpc, "http://fake-sandbox")
	return p
}

// Close はサーバーを止める。WatchEvents等の開いたストリームは切れる。
func (p *InProcess) Close() {
	_ = p.srv.Close()
	<-p.done
}

type pipeListener struct {
	conns     chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

func (l *pipeListener) DialContext(ctx context.Context) (net.Conn, error) {
	server, client := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.closed:
		server.Close()
		client.Close()
		return nil, errors.New("fake sandbox is closed")
	case <-ctx.Done():
		server.Close()
		client.Close()
		return nil, ctx.Err()
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "fake-sandbox" }
