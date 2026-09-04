package hostloop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpserver"
	"github.com/TadahiroYamamura/masuda/internal/testutil"
)

// newTestDaemon starts a fresh Store-backed daemon over a short-lived state
// directory under /tmp (not t.TempDir(), whose longer paths risk exceeding
// AF_UNIX's ~108 byte sun_path limit -- see internal/statedaemon/mcpserver's
// uds_test.go) and returns the state directory this package's functions
// expect (they derive the socket path from it via statedaemon.SocketPath).
func newTestDaemon(t *testing.T) string {
	t.Helper()
	stateDir, err := os.MkdirTemp("", "hl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(stateDir) })

	store, err := statedaemon.Open(filepath.Join(stateDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- mcpserver.ServeUDS(ctx, store, statedaemon.SocketPath(stateDir)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
			t.Error("ServeUDS did not stop after context cancellation")
		}
	})

	if err := testutil.WaitForUDS(statedaemon.SocketPath(stateDir), serveErr); err != nil {
		t.Fatal(err)
	}
	return stateDir
}

func TestWriteTaskBriefRoundTrips(t *testing.T) {
	stateDir := newTestDaemon(t)
	if err := WriteTaskBrief(stateDir, "タスクの説明"); err != nil {
		t.Fatalf("WriteTaskBrief() error = %v, want nil", err)
	}

	c, err := dial(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	value, found, err := c.Get(context.Background(), taskBriefKey)
	if err != nil {
		t.Fatal(err)
	}
	if !found || value != "タスクの説明" {
		t.Fatalf("Get(taskBriefKey) = (%q, %v), want (%q, true)", value, found, "タスクの説明")
	}
}

func TestWriteInstructionsWritesAPlainFile(t *testing.T) {
	// Deliberately not daemon-backed -- see WriteInstructions' doc comment:
	// the investigator subagent opens INSTRUCTIONS.md itself by path, so it
	// must stay a real file no daemon is involved in reading. A bare
	// t.TempDir() is fine here (no socket path involved, unlike
	// newTestDaemon's other callers).
	stateDir := t.TempDir()
	if err := WriteInstructions(stateDir, []byte("事前調査メモ")); err != nil {
		t.Fatalf("WriteInstructions() error = %v, want nil", err)
	}

	data, err := os.ReadFile(filepath.Join(stateDir, "INSTRUCTIONS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "事前調査メモ" {
		t.Fatalf("INSTRUCTIONS.md content = %q, want %q", data, "事前調査メモ")
	}
}

func TestMCPConfigJSONIsValidAndPointsAtTheGivenPort(t *testing.T) {
	raw := mcpConfigJSON(54321)
	var parsed struct {
		MCPServers map[string]struct {
			Type    string `json:"type"`
			URL     string `json:"url"`
			Timeout int64  `json:"timeout"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("mcpConfigJSON(54321) = %q is not valid JSON: %v", raw, err)
	}
	server, ok := parsed.MCPServers[mcpGateServerName]
	if !ok {
		t.Fatalf("mcpConfigJSON(54321) = %q has no %q entry", raw, mcpGateServerName)
	}
	if server.Type != "http" || server.URL != "http://127.0.0.1:54321/" {
		t.Fatalf("mcpConfigJSON(54321) server entry = %+v, want type=http url=http://127.0.0.1:54321/", server)
	}
	// Confirmed live: without a generous per-server timeout override,
	// Claude Code aborts a wait_for_gate_resolution call on its own hard
	// wall-clock MCP tool timeout well under a minute -- long before any
	// real human gets around to approving a gate.
	if server.Timeout < 60*60*1000 {
		t.Fatalf("mcpConfigJSON(54321) server timeout = %dms, want at least an hour -- too short and real gate waits will be silently aborted by Claude Code's own MCP tool timeout", server.Timeout)
	}
}

func TestStartMCPRelayBridgesCuratedSocketToATCPPort(t *testing.T) {
	stateDir := newTestDaemon(t)

	port, err := startMCPRelay(stateDir)
	if err != nil {
		t.Fatalf("startMCPRelay() error = %v, want nil", err)
	}

	// The relay is a detached subprocess of the *test binary*, not the real
	// masuda CLI (os.Executable() resolves to whatever is running this test)
	// -- so it can't actually understand "internal mcp-relay" as a
	// subcommand. This only proves startMCPRelay picks a free port and
	// attempts to launch something there; the relay's own byte-proxying
	// behavior is covered by cmd/masuda's TestMCPRelayProxiesCallsToCuratedSocket,
	// which runs against the real subcommand function directly.
	if port <= 0 {
		t.Fatalf("startMCPRelay() port = %d, want a positive port number", port)
	}
}
