package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/testutil"
)

// fakeAnthropic records the Authorization header and anthropic-beta each
// request arrived with, standing in for api.anthropic.com.
func fakeAnthropic(t *testing.T) (*httptest.Server, <-chan http.Header) {
	t.Helper()
	seen := make(chan http.Header, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		_, _ = io.WriteString(w, "upstream:"+r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func writeToken(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func gatewayRequest(t *testing.T, h http.Handler, auth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAPIGatewaySwapsPlaceholderForRealToken(t *testing.T) {
	upstream, seen := fakeAnthropic(t)
	u, _ := url.Parse(upstream.URL)
	h := newAPIGatewayHandler(writeToken(t, "real-token"), u)

	rec := gatewayRequest(t, h, "Bearer "+guestPlaceholderToken)
	if rec.Code != http.StatusOK || rec.Body.String() != "upstream:/v1/messages" {
		t.Fatalf("response = %d %q, want the upstream's 200", rec.Code, rec.Body.String())
	}
	got := <-seen
	if auth := got.Get("Authorization"); auth != "Bearer real-token" {
		t.Errorf("upstream saw Authorization %q, want the real token", auth)
	}
	// The subscription only applies if the OAuth capability reaches the API.
	if beta := got.Get("anthropic-beta"); beta != "oauth-2025-04-20" {
		t.Errorf("upstream saw anthropic-beta %q, want it forwarded untouched", beta)
	}
}

func TestAPIGatewayRefusesAnyOtherCredential(t *testing.T) {
	upstream, seen := fakeAnthropic(t)
	u, _ := url.Parse(upstream.URL)
	h := newAPIGatewayHandler(writeToken(t, "real-token"), u)

	for _, auth := range []string{"", "Bearer someone-elses-token", guestPlaceholderToken} {
		if rec := gatewayRequest(t, h, auth); rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d, want 401", auth, rec.Code)
		}
	}
	select {
	case h := <-seen:
		t.Errorf("a refused request reached the upstream with Authorization %q", h.Get("Authorization"))
	default:
	}
}

func TestAPIGatewayWithoutRegisteredToken(t *testing.T) {
	upstream, seen := fakeAnthropic(t)
	u, _ := url.Parse(upstream.URL)
	h := newAPIGatewayHandler(filepath.Join(t.TempDir(), "missing"), u)

	if rec := gatewayRequest(t, h, "Bearer "+guestPlaceholderToken); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503 when no token is registered", rec.Code)
	}
	select {
	case <-seen:
		t.Error("a request reached the upstream without a real token to send")
	default:
	}
}

// TestAPIGatewayRefusedClientIsDroppedBeforeHTTP: a connection allow rejects
// must never be served, so it gets closed without a response.
func TestAPIGatewayRefusedClientIsDroppedBeforeHTTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	served := make(chan struct{}, 1)
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served <- struct{}{} })
	port := freeTCPPort(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	errc := make(chan error, 1)
	go func() { errc <- runAPIGateway(ctx, addr, h, func(net.Addr) bool { return false }) }()
	if err := testutil.WaitForTCP(addr, errc); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get("http://" + addr + "/v1/messages"); err == nil {
		resp.Body.Close()
		t.Errorf("a refused client got an HTTP response (%d)", resp.StatusCode)
	}
	select {
	case <-served:
		t.Error("a refused client's request reached the handler")
	default:
	}
}
