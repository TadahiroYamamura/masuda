package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	go func() { serveErr <- runStatedaemon(ctx, stateDir) }()
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

func TestRunStatedaemonServesCuratedSocketToo(t *testing.T) {
	id := newTestWorkspace(t)
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- runStatedaemon(ctx, stateDir) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
			t.Error("runStatedaemon did not stop after context cancellation")
		}
	})

	trustedSocket := statedaemon.SocketPath(stateDir)
	curatedSocket := statedaemon.CuratedSocketPath(stateDir)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, err1 := os.Stat(trustedSocket)
		_, err2 := os.Stat(curatedSocket)
		if err1 == nil && err2 == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	dial := func(socketPath string) *mcp.ClientSession {
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

	trusted := dial(trustedSocket)
	curated := dial(curatedSocket)

	type waitResult struct {
		Status string `json:"status"`
	}
	done := make(chan waitResult, 1)
	go func() {
		res, err := curated.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "wait_for_gate_change",
			Arguments: map[string]any{"name": "plan"},
		})
		if err != nil || res.IsError {
			t.Errorf("CallTool(wait_for_gate_change) = (%+v, %v), want success", res, err)
			done <- waitResult{}
			return
		}
		data, _ := json.Marshal(res.StructuredContent)
		var out waitResult
		json.Unmarshal(data, &out)
		done <- out
	}()

	// Prove both sockets share the same underlying Store: the write goes
	// through the trusted socket, the wait is unblocked on the curated one.
	if _, err := trusted.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "state_put",
		Arguments: map[string]any{"key": "gate:plan", "value": `{"status":"approved"}`},
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case out := <-done:
		if out.Status != "approved" {
			t.Fatalf("wait_for_gate_change result = %+v, want status=approved", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait_for_gate_change did not return after the trusted socket's state_put")
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

func TestDaemonAliveMissingPIDFile(t *testing.T) {
	stateDir := t.TempDir()
	if daemonAlive(stateDir) {
		t.Fatal("daemonAlive() = true with no daemon.pid, want false")
	}
}

func TestDaemonAliveLiveProcess(t *testing.T) {
	stateDir := t.TempDir()
	// The test binary itself is definitely alive.
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(filepath.Join(stateDir, daemonPIDName), []byte(pid), 0o644); err != nil {
		t.Fatal(err)
	}
	if !daemonAlive(stateDir) {
		t.Fatal("daemonAlive() = false for this test process's own PID, want true")
	}
}

func TestDaemonAliveDeadProcess(t *testing.T) {
	stateDir := t.TempDir()
	// Spawn and immediately wait out a short-lived process to get a PID
	// that's guaranteed to be free again (no PID reuse race within a single
	// test process's lifetime on Linux).
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	deadPID := strconv.Itoa(c.Process.Pid)
	if err := os.WriteFile(filepath.Join(stateDir, daemonPIDName), []byte(deadPID), 0o644); err != nil {
		t.Fatal(err)
	}
	if daemonAlive(stateDir) {
		t.Fatal("daemonAlive() = true for an already-exited process's PID, want false")
	}
}
