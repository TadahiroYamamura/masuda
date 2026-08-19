package egressproxy

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestProxy starts a Proxy on a fresh loopback listener and returns its
// address plus a cleanup-registered shutdown.
func newTestProxy(t *testing.T, allowed AllowlistFunc, destPort string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	p := &Proxy{Allowed: allowed, DestPort: destPort}
	go p.Serve(ln)
	return ln.Addr().String()
}

// TestProxyForwardsAllowedConnection confirms the whole round trip: a real
// TLS client completes its handshake through the proxy against a real TLS
// server, and an HTTP request/response actually flows -- the proxy isn't
// just approving the SNI, the connection genuinely works end to end.
func TestProxyForwardsAllowedConnection(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello from upstream"))
	}))
	defer upstream.Close()

	_, upstreamPort, err := net.SplitHostPort(upstream.Listener.Addr().String())
	if err != nil {
		t.Fatalf("splitting upstream address: %v", err)
	}

	proxyAddr := newTestProxy(t, func(_ net.IP, hostname string) bool { return hostname == "localhost" }, upstreamPort)

	client := &http.Client{
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return tls.Dial(network, proxyAddr, &tls.Config{ServerName: "localhost", InsecureSkipVerify: true})
			},
		},
	}
	resp, err := client.Get("https://localhost/")
	if err != nil {
		t.Fatalf("client.Get() error = %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	if string(body) != "hello from upstream" {
		t.Errorf("body = %q, want %q", body, "hello from upstream")
	}
}

// TestProxyDeniesDisallowedHostname confirms a hostname Allowed rejects
// never reaches a real destination -- the client's handshake must fail,
// not just "succeed against nothing".
func TestProxyDeniesDisallowedHostname(t *testing.T) {
	proxyAddr := newTestProxy(t, func(net.IP, string) bool { return false }, "443")

	conn, err := tls.Dial("tcp", proxyAddr, &tls.Config{ServerName: "example.com", InsecureSkipVerify: true})
	if err == nil {
		conn.Close()
		t.Fatal("tls.Dial() succeeded, want a handshake failure (proxy should have closed the connection)")
	}
}

// TestProxyClosesNonTLSConnections confirms default-deny extends to
// traffic that isn't TLS at all, not just to disallowed hostnames.
func TestProxyClosesNonTLSConnections(t *testing.T) {
	proxyAddr := newTestProxy(t, func(net.IP, string) bool { return true }, "443")

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("net.Dial() error = %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")); err != nil {
		t.Fatalf("writing plaintext request: %v", err)
	}
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err == nil {
		t.Errorf("Read() = %d bytes, %v; want the proxy to close the connection instead of responding", n, err)
	}
}
