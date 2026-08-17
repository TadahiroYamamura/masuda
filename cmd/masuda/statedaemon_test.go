package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newTestWorkspace points internal/workspace's XDG data dir at a short-lived
// directory under /tmp (not t.TempDir(), whose longer paths risk exceeding
// AF_UNIX's ~108 byte sun_path limit once daemon.sock's own path is appended
// -- internal/statedaemon/mcpserver's uds_test.go hit the same constraint)
// and creates a workspace, returning its ID.
func newTestWorkspace(t *testing.T) string {
	t.Helper()
	dataHome, err := os.MkdirTemp("", "sdws")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dataHome) })
	t.Setenv("XDG_DATA_HOME", dataHome)

	id, err := workspace.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Create("/repo", id, "feature-branch", "develop", ""); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRunStatedaemonServesWorkspaceStore(t *testing.T) {
	id := newTestWorkspace(t)
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- runStatedaemon(ctx, id) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
			t.Error("runStatedaemon did not stop after context cancellation")
		}
	})

	socketPath := statedaemon.SocketPath(stateDir)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

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
	defer session.Close()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "state_put",
		Arguments: map[string]any{"key": "gate:plan", "value": "v"},
	})
	if err != nil || res.IsError {
		t.Fatalf("CallTool(state_put) = (%v, error=%v), want success", res, err)
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
	var out struct {
		Value string `json:"value"`
		Found bool   `json:"found"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Found || out.Value != "v" {
		t.Fatalf("state_get via runStatedaemon = %+v, want found=true value=%q", out, "v")
	}
}

func TestStopDaemonWithoutPIDFileIsNoOp(t *testing.T) {
	id := newTestWorkspace(t)
	// No daemon.pid was ever written -- must succeed anyway (a workspace
	// predating this feature has no PID file at all).
	if err := stopDaemon(id); err != nil {
		t.Fatalf("stopDaemon() error = %v, want nil", err)
	}
}
