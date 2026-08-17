package mcpserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectUDS starts ServeUDS backed by a fresh Store over a short-lived Unix
// socket (under /tmp directly rather than t.TempDir(), which can produce
// paths too long for the ~108 byte sun_path limit) and returns a connected
// client session. The server is stopped and the socket cleaned up on test
// cleanup.
func connectUDS(t *testing.T) *mcp.ClientSession {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "sdmcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	socketPath := filepath.Join(sockDir, "daemon.sock")

	store, err := statedaemon.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- ServeUDS(ctx, store, socketPath) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
			t.Error("ServeUDS did not stop after context cancellation")
		}
	})

	waitForSocket(t, socketPath)

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	transport := &mcp.StreamableClientTransport{Endpoint: "http://unix/", HTTPClient: httpClient}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("socket %s never appeared", path)
}

func TestServeUDSRoundTrip(t *testing.T) {
	session := connectUDS(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "state_put",
		Arguments: map[string]any{"key": "gate:plan", "value": "v"},
	})
	if err != nil {
		t.Fatalf("CallTool(state_put) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("state_put returned a tool error: %+v", res.Content)
	}

	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "state_get",
		Arguments: map[string]any{"key": "gate:plan"},
	})
	if err != nil {
		t.Fatalf("CallTool(state_get) error = %v", err)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var getOut struct {
		Value string `json:"value"`
		Found bool   `json:"found"`
	}
	if err := json.Unmarshal(data, &getOut); err != nil {
		t.Fatal(err)
	}
	if !getOut.Found || getOut.Value != "v" {
		t.Fatalf("state_get over UDS = %+v, want found=true value=%q", getOut, "v")
	}
}

func TestServeUDSRemovesStaleSocket(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "sdmcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	socketPath := filepath.Join(sockDir, "daemon.sock")

	// Simulate a stale socket file left behind by a previous, uncleanly
	// terminated daemon process.
	if err := os.WriteFile(socketPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := statedaemon.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- ServeUDS(ctx, store, socketPath) }()

	waitForSocket(t, socketPath)

	cancel()
	select {
	case err := <-serveErr:
		if err != context.Canceled {
			t.Fatalf("ServeUDS() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeUDS did not stop after context cancellation")
	}
}
