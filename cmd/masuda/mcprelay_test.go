package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpserver"
	"github.com/TadahiroYamamura/masuda/internal/testutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// freeTCPPort asks the OS for an unused TCP port on 127.0.0.1, the same
// TOCTOU-accepting pattern internal/sandbox.freePort uses for a
// per-invocation port that gets rebound almost immediately after.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestMCPRelayProxiesCallsToCuratedSocket(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "relay")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	socketPath := filepath.Join(sockDir, "daemon-curated.sock")

	store, err := statedaemon.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serveErr := make(chan error, 1)
	go func() { serveErr <- mcpserver.ServeCuratedUDS(ctx, store, socketPath) }()
	if err := testutil.WaitForUDS(socketPath, serveErr); err != nil {
		t.Fatal(err)
	}

	port := freeTCPPort(t)
	relayErr := make(chan error, 1)
	go func() { relayErr <- runMCPRelay(ctx, socketPath, "127.0.0.1", port) }()

	// Retried rather than probed-then-connected. A TCP probe cannot tell the
	// relay's listener from freeTCPPort's own: the socket freeTCPPort just
	// closed stays in LISTEN for a moment after Close() returns, so the
	// probe connects to that leftover, calls the relay ready, and the real
	// connection then lands in the gap before the relay has bound anything.
	// Confirmed in /proc/net/tcp -- the socket the probe reached carried the
	// same inode as the one freeTCPPort had already closed, and the relay's
	// listener appeared under a different inode only afterwards. What this
	// test needs is an MCP session, so it asks for one until it gets it.
	session := connectMCPOverTCP(t, port, relayErr, serveErr)
	defer session.Close()

	// Deferred after session.Close() so it runs before it (defers are LIFO):
	// wait_for_gate_resolution only returns when a gate resolves, so a failed
	// assertion below would leave the call in flight, and closing a
	// connection with an outstanding call waits for that call forever. See
	// statedaemon_test.go's equivalent for the full reasoning.
	waitCtx, cancelWait := context.WithCancel(context.Background())
	waitReturned := make(chan struct{})
	defer func() {
		cancelWait()
		select {
		case <-waitReturned:
		case <-time.After(testutil.DefaultTimeout):
			t.Error("wait_for_gate_resolution did not return after its context was cancelled")
		}
	}()

	done := make(chan struct{})
	var status string
	go func() {
		defer close(waitReturned)
		res, err := session.CallTool(waitCtx, &mcp.CallToolParams{
			Name:      "wait_for_gate_resolution",
			Arguments: map[string]any{"name": "plan"},
		})
		if waitCtx.Err() != nil {
			return // cancelled by the deferred cleanup; the real failure is already recorded
		}
		if err != nil || res.IsError {
			t.Errorf("CallTool(wait_for_gate_resolution) via relay = (%+v, %v), want success", res, err)
			close(done)
			return
		}
		data, _ := json.Marshal(res.StructuredContent)
		var out struct {
			Status string `json:"status"`
		}
		json.Unmarshal(data, &out)
		status = out.Status
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("wait_for_gate_resolution returned before the gate was resolved")
	case <-time.After(100 * time.Millisecond):
	}

	if err := store.Put("gate:plan", []byte(`{"status":"approved"}`)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
		if status != "approved" {
			t.Fatalf("status via relay = %q, want %q", status, "approved")
		}
	case <-time.After(testutil.DefaultTimeout):
		t.Fatal("wait_for_gate_resolution via relay did not return after the gate was resolved")
	}

	cancel()
	if err := <-serveErr; err != nil && err != context.Canceled {
		t.Errorf("ServeCuratedUDS() error = %v, want nil or context.Canceled", err)
	}
	if err := <-relayErr; err != nil && err != context.Canceled {
		t.Errorf("runMCPRelay() error = %v, want nil or context.Canceled", err)
	}
}

// TestMCPRelayRespectsBindAddress confirms --bind actually changes which
// address the relay listens on (not just accepted-but-ignored), using
// 127.0.0.2 rather than 127.0.0.1 -- still loopback, so it needs no host
// network setup, but a genuinely different address from the default,
// proving the parameter is threaded through rather than silently dropped.
// The VM path (Issue #31 M5-4) needs a real non-loopback bridge address in
// practice, but that's a deployment detail this doesn't need real VM/bridge
// infrastructure to verify.
func TestMCPRelayRespectsBindAddress(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "relay")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	socketPath := filepath.Join(sockDir, "daemon-curated.sock")

	store, err := statedaemon.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serveErr := make(chan error, 1)
	go func() { serveErr <- mcpserver.ServeCuratedUDS(ctx, store, socketPath) }()
	if err := testutil.WaitForUDS(socketPath, serveErr); err != nil {
		t.Fatal(err)
	}

	const bind = "127.0.0.2"
	port := freeTCPPort(t)
	relayErr := make(chan error, 1)
	go func() { relayErr <- runMCPRelay(ctx, socketPath, bind, port) }()

	// Reaching here at all is the assertion: waitForTCPAccepting fails the
	// test if nothing ever accepts on the non-default bind address.
	if err := testutil.WaitForTCP(net.JoinHostPort(bind, strconv.Itoa(port)), relayErr); err != nil {
		t.Fatal(err)
	}
}

// connectMCPOverTCP opens an MCP session against the relay on port, retrying
// until it succeeds or testutil.DefaultTimeout passes. On giving up it says which of
// the two processes behind the port had stopped, if either had -- a bare
// "connection refused" here is what Issue #42 spent a long time being.
func connectMCPOverTCP(t *testing.T, port int, relayErr, serveErr <-chan error) *mcp.ClientSession {
	t.Helper()
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/", port)
	deadline := time.Now().Add(testutil.DefaultTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		transport := &mcp.StreamableClientTransport{Endpoint: endpoint}
		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
		session, err := client.Connect(context.Background(), transport, nil)
		if err == nil {
			return session
		}
		lastErr = err
		select {
		case relayStopped := <-relayErr:
			t.Fatalf("connecting to the relay failed (%v) because the relay had stopped: %v", err, relayStopped)
		case serveStopped := <-serveErr:
			t.Fatalf("connecting to the relay failed (%v) because the curated socket had stopped: %v", err, serveStopped)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never opened an MCP session against %s while the relay and the curated socket both stayed up: %v", endpoint, lastErr)
	return nil // unreachable: t.Fatalf ends the test goroutine
}
