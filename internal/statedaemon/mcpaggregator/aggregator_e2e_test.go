package mcpaggregator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runAsFakeChild is the fork-and-exec trick go-sdk's own mcp/cmd_test.go
// uses (createServerCommand/TestMain there): when set, this test binary
// re-execs itself as a minimal MCP server over stdio instead of running
// go test, so Aggregator.startChild's real exec.Command + CommandTransport
// path is exercised end to end without needing an actual npx-distributed
// package available in CI.
const runAsFakeChild = "_MASUDA_TEST_FAKE_MCP_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(runAsFakeChild) != "" {
		runFakeChildServer()
		return
	}
	os.Exit(m.Run())
}

func runFakeChildServer() {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-child", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "get_issue", Description: "fake issue lookup"}, func(
		_ context.Context, _ *mcp.CallToolRequest, in struct {
			Number int `json:"number"`
		},
	) (*mcp.CallToolResult, any, error) {
		return nil, map[string]any{"title": "issue #" + strconv.Itoa(in.Number)}, nil
	})
	// Deliberately not allowlisted by the test's MCPServerDecl.Tools --
	// proves registerProxy's default-deny actually filters, not just that
	// it registers everything it sees.
	mcp.AddTool(server, &mcp.Tool{Name: "not_allowlisted", Description: "should never be proxied"}, func(
		_ context.Context, _ *mcp.CallToolRequest, _ struct{},
	) (*mcp.CallToolResult, any, error) {
		return nil, map[string]any{}, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		panic(err)
	}
}

func TestAggregatorStartsApprovedChildAndProxiesAllowlistedTool(t *testing.T) {
	repoRoot := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	decl := config.MCPServerDecl{
		Command: exe,
		Env:     []string{runAsFakeChild},
		Tools:   []string{"get_issue"}, // not_allowlisted deliberately excluded
	}
	hash, err := config.DeclHash(decl)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, config.SettingsPath(repoRoot), config.Config{
		MCPServers: map[string]config.MCPServerDecl{"fake": decl},
	})
	writeJSON(t, config.SettingsLocalPath(repoRoot), config.LocalSettings{
		MCPServers: map[string]config.MCPServerApproval{
			"fake": {Approved: true, DeclHash: hash, Env: map[string]string{runAsFakeChild: "1"}},
		},
	})

	curated := mcp.NewServer(&mcp.Implementation{Name: "test-curated", Version: "0.0.1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	agg := Start(ctx, repoRoot, curated, t.TempDir())
	t.Cleanup(agg.Close)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go curated.Run(ctx, serverTransport)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	tools := waitForTools(t, ctx, session, "fake__get_issue")

	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["fake__get_issue"] {
		t.Fatalf("tools = %v, want fake__get_issue present", names)
	}
	if names["fake__not_allowlisted"] {
		t.Fatalf("tools = %v, want fake__not_allowlisted absent (not in decl.Tools)", names)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "fake__get_issue",
		Arguments: map[string]any{"number": 42},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v, want nil", err)
	}
	if res.IsError {
		t.Fatalf("CallTool() result is an error: %+v", res.Content)
	}
	var out struct {
		Title string `json:"title"`
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Title != "issue #42" {
		t.Fatalf("title = %q, want %q", out.Title, "issue #42")
	}
}

func TestAggregatorClosesChildProcess(t *testing.T) {
	repoRoot := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	decl := config.MCPServerDecl{Command: exe, Env: []string{runAsFakeChild}, Tools: []string{"get_issue"}}
	hash, err := config.DeclHash(decl)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, config.SettingsPath(repoRoot), config.Config{MCPServers: map[string]config.MCPServerDecl{"fake": decl}})
	writeJSON(t, config.SettingsLocalPath(repoRoot), config.LocalSettings{
		MCPServers: map[string]config.MCPServerApproval{
			"fake": {Approved: true, DeclHash: hash, Env: map[string]string{runAsFakeChild: "1"}},
		},
	})

	curated := mcp.NewServer(&mcp.Implementation{Name: "test-curated", Version: "0.0.1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	agg := Start(ctx, repoRoot, curated, t.TempDir())

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go curated.Run(ctx, serverTransport)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForTools(t, ctx, session, "fake__get_issue")
	session.Close()

	agg.mu.Lock()
	_, ok := agg.children["fake"]
	agg.mu.Unlock()
	if !ok {
		t.Fatal("child \"fake\" never registered")
	}

	// mcp.CommandTransport.Close (closing the child's session, which
	// Aggregator.Close does for every registered child) blocks until the
	// child process actually exits, forcing SIGTERM/SIGKILL if it doesn't
	// exit on its own -- so a bounded Close() call here is itself the
	// assertion that the child process was reaped, not left running.
	done := make(chan struct{})
	go func() { agg.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Aggregator.Close() did not return -- child process may not have been terminated")
	}
}

func waitForTools(t *testing.T, ctx context.Context, session *mcp.ClientSession, want string) []*mcp.Tool {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		res, err := session.ListTools(ctx, nil)
		if err == nil {
			for _, tool := range res.Tools {
				if tool.Name == want {
					return res.Tools
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("tool %q never appeared (last err = %v)", want, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func writeJSON(t *testing.T, path string, v any) {
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
