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
	"syscall"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/testutil"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runAsFakeMCPChild is the fork-and-exec trick go-sdk's own mcp/cmd_test.go
// uses: when set, this test binary re-execs itself as a minimal MCP server
// over stdio instead of running go test, so
// TestRunStatedaemonAggregatesApprovedMCPServer can exercise
// runStatedaemon's real --repo-root wiring (statedaemon.go ->
// mcpaggregator.Start -> exec.Command -> CommandTransport) end to end.
const runAsFakeMCPChild = "_MASUDA_TEST_FAKE_MCP_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(runAsFakeMCPChild) != "" {
		runFakeMCPChildServer()
		return
	}
	os.Exit(m.Run())
}

func runFakeMCPChildServer() {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-child", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "ping", Description: "fake ping"}, func(
		_ context.Context, _ *mcp.CallToolRequest, _ struct{},
	) (*mcp.CallToolResult, any, error) {
		return nil, map[string]any{"pong": true}, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		panic(err)
	}
}

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
	go func() { serveErr <- runStatedaemon(ctx, stateDir, "", "") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(testutil.DefaultTimeout):
			t.Error("runStatedaemon did not stop after context cancellation")
		}
	})

	socketPath := statedaemon.SocketPath(stateDir)
	if err := testutil.WaitForUDS(socketPath, serveErr); err != nil {
		t.Fatal(err)
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
	go func() { serveErr <- runStatedaemon(ctx, stateDir, "", "") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(testutil.DefaultTimeout):
			t.Error("runStatedaemon did not stop after context cancellation")
		}
	})

	trustedSocket := statedaemon.SocketPath(stateDir)
	curatedSocket := statedaemon.CuratedSocketPath(stateDir)
	if err := testutil.WaitForUDS(trustedSocket, serveErr); err != nil {
		t.Fatal(err)
	}
	if err := testutil.WaitForUDS(curatedSocket, serveErr); err != nil {
		t.Fatal(err)
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
	// wait_for_gate_resolution blocks until a human resolves the gate, so a
	// failed assertion below would otherwise leave this call in flight --
	// and closing a connection that still has an outstanding call waits for
	// that call to finish, i.e. forever. Registered *after* dial()'s
	// session-closing cleanups so that t.Cleanup's LIFO order runs this
	// first, turning a one-line failure back into a one-line failure instead
	// of a ten-minute package timeout that takes the other tests' results
	// with it.
	waitCtx, cancelWait := context.WithCancel(context.Background())
	waitReturned := make(chan struct{})
	t.Cleanup(func() {
		cancelWait()
		select {
		case <-waitReturned:
		case <-time.After(testutil.DefaultTimeout):
			t.Error("wait_for_gate_resolution did not return after its context was cancelled")
		}
	})

	done := make(chan waitResult, 1)
	go func() {
		defer close(waitReturned)
		res, err := curated.CallTool(waitCtx, &mcp.CallToolParams{
			Name:      "wait_for_gate_resolution",
			Arguments: map[string]any{"name": "plan"},
		})
		if waitCtx.Err() != nil {
			// Cancelled by the cleanup above: the real failure is already
			// recorded, and t.Errorf here would race the test finishing.
			return
		}
		if err != nil || res.IsError {
			t.Errorf("CallTool(wait_for_gate_resolution) = (%+v, %v), want success", res, err)
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
			t.Fatalf("wait_for_gate_resolution result = %+v, want status=approved", out)
		}
	case <-time.After(testutil.DefaultTimeout):
		t.Fatal("wait_for_gate_resolution did not return after the trusted socket's state_put")
	}
}

func TestRunStatedaemonAggregatesApprovedMCPServer(t *testing.T) {
	id := newTestWorkspace(t)
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	decl := config.MCPServerDecl{Command: exe, Env: []string{runAsFakeMCPChild}, Tools: []string{"ping"}}
	hash, err := config.DeclHash(decl)
	if err != nil {
		t.Fatal(err)
	}
	writeConfigJSON(t, config.SettingsPath(repoRoot), config.Config{
		MCPServers: map[string]config.MCPServerDecl{"fake": decl},
	})
	writeConfigJSON(t, config.SettingsLocalPath(repoRoot), config.LocalSettings{
		MCPServers: map[string]config.MCPServerApproval{
			"fake": {Approved: true, DeclHash: hash, Env: map[string]string{runAsFakeMCPChild: "1"}},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- runStatedaemon(ctx, stateDir, repoRoot, "") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(testutil.DefaultTimeout):
			t.Error("runStatedaemon did not stop after context cancellation")
		}
	})

	curatedSocket := statedaemon.CuratedSocketPath(stateDir)
	if err := testutil.WaitForUDS(curatedSocket, serveErr); err != nil {
		t.Fatal(err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", curatedSocket)
			},
		},
	}
	transport := &mcp.StreamableClientTransport{Endpoint: "http://unix/", HTTPClient: httpClient}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	// The aggregated child starts asynchronously (internal/statedaemon/
	// mcpaggregator.Start never blocks its caller) -- poll for the proxied
	// tool to appear rather than assuming it's ready the instant the
	// curated socket itself is up.
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "fake__ping"})
		if err == nil && !res.IsError {
			return // success
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("fake__ping never became callable over the curated socket, last error: %v", lastErr)
}

func writeConfigJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// stubEnsureDaemon makes ensureDaemon a no-op for one test and records the
// workspace IDs it was asked to start. Tests that drive a command which now
// ensures the daemon (ensureGateWorkspace, `sandbox start`) need this: the
// real startDaemon re-execs os.Executable(), which under `go test` is the
// test binary itself.
func stubEnsureDaemon(t *testing.T) *[]string {
	t.Helper()
	var called []string
	orig := ensureDaemon
	ensureDaemon = func(id string) error {
		called = append(called, id)
		return nil
	}
	t.Cleanup(func() { ensureDaemon = orig })
	return &called
}

func TestStopDaemonWithoutPIDFileIsNoOp(t *testing.T) {
	id := newTestWorkspace(t)
	// No daemon.pid was ever written -- must succeed anyway (a workspace
	// predating this feature has no PID file at all).
	if err := stopDaemon(id); err != nil {
		t.Fatalf("stopDaemon() error = %v, want nil", err)
	}
}

// newShortStateDir is a state directory under /tmp rather than t.TempDir(),
// whose longer paths risk exceeding AF_UNIX's ~108 byte sun_path limit once
// daemon.sock's own name is appended -- the same constraint newTestWorkspace
// works around.
func newShortStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// writePID records pid as stateDir's daemon, the way startDaemon does.
func writePID(t *testing.T, stateDir string, pid int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(stateDir, daemonPIDName), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeDaemonProcess starts a long-running process whose argv carries the two
// markers isDaemonProcess looks for, so a test can exercise the "this really
// is our daemon" path without running a real one. `sh -c <script> <args...>`
// puts the trailing arguments in $0/$1/... -- they never reach the script,
// but they do land in /proc/<pid>/cmdline, which is what matters here.
func fakeDaemonProcess(t *testing.T, stateDir string) *exec.Cmd {
	t.Helper()
	c := exec.Command("sh", "-c", "sleep 30", "internal", "statedaemon", "--state-dir", stateDir)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Process.Kill()
		_ = c.Wait()
	})
	return c
}

func TestDaemonServingWithoutSocket(t *testing.T) {
	if daemonServing(t.TempDir()) {
		t.Fatal("daemonServing() = true with no socket, want false")
	}
}

func TestDaemonServingWhenSocketAccepts(t *testing.T) {
	stateDir := newShortStateDir(t)
	l, err := net.Listen("unix", statedaemon.SocketPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if !daemonServing(stateDir) {
		t.Fatal("daemonServing() = false against a listening socket, want true")
	}
}

// TestDaemonServingIgnoresLivePIDWithoutSocket is Issue #52's reboot case in
// miniature: daemon.pid names a live process (here the test binary, standing
// in for a PID the kernel handed to something else after a reboot) but
// nothing answers on the socket. The old PID-based check called that alive,
// so startDaemon skipped and every gate command then failed with "connect:
// connection refused".
func TestDaemonServingIgnoresLivePIDWithoutSocket(t *testing.T) {
	stateDir := newShortStateDir(t)
	writePID(t, stateDir, os.Getpid())
	if daemonServing(stateDir) {
		t.Fatal("daemonServing() = true for a live PID with no listener, want false")
	}
}

// TestIsDaemonProcessRejectsUnrelatedProcess covers the other half: this
// test binary is alive and its PID is recorded, but it is not a state
// daemon, so nothing may signal it.
func TestIsDaemonProcessRejectsUnrelatedProcess(t *testing.T) {
	stateDir := newShortStateDir(t)
	if isDaemonProcess(os.Getpid(), stateDir) {
		t.Fatal("isDaemonProcess() = true for the test binary, want false")
	}
}

func TestIsDaemonProcessRejectsDaemonForAnotherWorkspace(t *testing.T) {
	mine, theirs := newShortStateDir(t), newShortStateDir(t)
	c := fakeDaemonProcess(t, theirs)
	if isDaemonProcess(c.Process.Pid, mine) {
		t.Fatal("isDaemonProcess() = true for another workspace's daemon, want false")
	}
}

func TestReclaimStaleDaemonStopsOurOwnDaemon(t *testing.T) {
	stateDir := newShortStateDir(t)
	c := fakeDaemonProcess(t, stateDir)
	writePID(t, stateDir, c.Process.Pid)

	if err := reclaimStaleDaemon(stateDir); err != nil {
		t.Fatalf("reclaimStaleDaemon() error = %v", err)
	}
	if isDaemonProcess(c.Process.Pid, stateDir) {
		t.Error("the recorded daemon is still running after reclaimStaleDaemon()")
	}
}

// TestReclaimStaleDaemonLeavesARecycledPIDAlone is the reason isDaemonProcess
// exists at all: after a host reboot the recorded PID may belong to something
// else entirely, and starting a replacement daemon must not cost that process
// a SIGTERM.
func TestReclaimStaleDaemonLeavesARecycledPIDAlone(t *testing.T) {
	stateDir := newShortStateDir(t)
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	writePID(t, stateDir, c.Process.Pid)

	if err := reclaimStaleDaemon(stateDir); err != nil {
		t.Fatalf("reclaimStaleDaemon() error = %v", err)
	}
	if err := c.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("the unrelated process was signalled: %v", err)
	}
}

// TestStopDaemonLeavesARecycledPIDAlone pins the same guard on the teardown
// path, which now runs on every `review approve` (ADR-0005) as well as
// `workspace remove`.
func TestStopDaemonLeavesARecycledPIDAlone(t *testing.T) {
	id := newTestWorkspace(t)
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	writePID(t, stateDir, c.Process.Pid)

	if err := stopDaemon(id); err != nil {
		t.Fatalf("stopDaemon() error = %v", err)
	}
	if err := c.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("the unrelated process was signalled: %v", err)
	}
}

func TestStopDaemonStopsOurOwnDaemon(t *testing.T) {
	id := newTestWorkspace(t)
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		t.Fatal(err)
	}
	c := fakeDaemonProcess(t, stateDir)
	writePID(t, stateDir, c.Process.Pid)

	if err := stopDaemon(id); err != nil {
		t.Fatalf("stopDaemon() error = %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && isDaemonProcess(c.Process.Pid, stateDir) {
		time.Sleep(20 * time.Millisecond)
	}
	if isDaemonProcess(c.Process.Pid, stateDir) {
		t.Error("the recorded daemon is still running after stopDaemon()")
	}
}
