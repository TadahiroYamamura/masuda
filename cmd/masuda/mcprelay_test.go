package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpserver"
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
	waitForFile(t, socketPath)

	port := freeTCPPort(t)
	relayErr := make(chan error, 1)
	go func() { relayErr <- runMCPRelay(ctx, socketPath, port) }()
	waitForTCPPort(t, port)

	transport := &mcp.StreamableClientTransport{Endpoint: fmt.Sprintf("http://127.0.0.1:%d/", port)}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	done := make(chan struct{})
	var status string
	go func() {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "wait_for_gate_change",
			Arguments: map[string]any{"name": "plan"},
		})
		if err != nil || res.IsError {
			t.Errorf("CallTool(wait_for_gate_change) via relay = (%+v, %v), want success", res, err)
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
		t.Fatal("wait_for_gate_change returned before the gate was resolved")
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
	case <-time.After(2 * time.Second):
		t.Fatal("wait_for_gate_change via relay did not return after the gate was resolved")
	}

	cancel()
	if err := <-serveErr; err != nil && err != context.Canceled {
		t.Errorf("ServeCuratedUDS() error = %v, want nil or context.Canceled", err)
	}
	if err := <-relayErr; err != nil && err != context.Canceled {
		t.Errorf("runMCPRelay() error = %v, want nil or context.Canceled", err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

func waitForTCPPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s", addr)
}
