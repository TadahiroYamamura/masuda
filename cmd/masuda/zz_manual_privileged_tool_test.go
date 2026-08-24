package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	masuda "github.com/TadahiroYamamura/masuda"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestManualRunPrivilegedCommandTool is the real-machine check of the path
// an AI session actually takes (ADR-0053): a curated MCP tool call, over the
// running state daemon's socket, naming a declared and approved command --
// and a disposable VM at the other end of it.
//
// internal/sandbox's own manual test covers the VM itself; what this adds is
// everything between the tool call and that VM: approval resolution, the
// runner wiring runStatedaemon builds, and the shape of what comes back.
func TestManualRunPrivilegedCommandTool(t *testing.T) {
	if os.Getenv("MASUDA_MANUAL_VM_TEST") == "" {
		t.Skip("set MASUDA_MANUAL_VM_TEST=1 to run this real-machine VM test")
	}

	repoRoot := t.TempDir()
	entry := "docker"
	if err := os.MkdirAll(config.ImageDir(repoRoot, entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ImageDockerfilePath(repoRoot, entry), masuda.DockerTemplate, 0o644); err != nil {
		t.Fatal(err)
	}
	tag, err := config.ImageTag(repoRoot, entry)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "build", "-f", config.ImageDockerfilePath(repoRoot, entry), "-t", tag, repoRoot).CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })

	decl := config.PrivilegedCommandDecl{
		Command:        "id -u && docker info --format '{{.ServerVersion}}'",
		Image:          entry,
		TimeoutSeconds: 600,
	}
	writeConfigJSON(t, config.SettingsPath(repoRoot), config.Config{
		PrivilegedCommands: map[string]config.PrivilegedCommandDecl{"e2e": decl},
	})

	// The tool must refuse before a human approves -- and the refusal has to
	// be legible enough for a session to relay it.
	session := startDaemonForPrivilegedTool(t, repoRoot)
	if _, err := callRunPrivileged(t, session, "e2e"); err == nil {
		t.Fatal("run_privileged_command succeeded before approval, want a refusal")
	} else if !strings.Contains(err.Error(), "not approved") {
		t.Errorf("refusal = %v, want it to say the command is not approved", err)
	}

	hash, err := config.PrivilegedCommandHash(repoRoot, decl)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveLocal(repoRoot, config.LocalSettings{
		PrivilegedCommands: map[string]config.PrivilegedCommandApproval{"e2e": {Approved: true, DeclHash: hash}},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := callRunPrivileged(t, session, "e2e")
	if err != nil {
		t.Fatalf("run_privileged_command after approval: %v", err)
	}
	t.Logf("tool result: %+v", out)
	if out.ExitCode != 0 {
		t.Errorf("exitCode = %d, want 0", out.ExitCode)
	}
	if !strings.Contains(out.Log, "29.") && !strings.Contains(out.Log, "Docker") {
		t.Errorf("log does not show a Docker server version:\n%s", out.Log)
	}
	// The path handed back has to be the one this session could open, i.e.
	// the guest's view of the state directory.
	if !strings.HasPrefix(out.ResultsDir, "/masuda-state/") {
		t.Errorf("resultsDir = %q, want the path as the session sees it", out.ResultsDir)
	}
}

type runPrivilegedToolOutput struct {
	ExitCode   int      `json:"exitCode"`
	Log        string   `json:"log"`
	ResultsDir string   `json:"resultsDir"`
	Outputs    []string `json:"outputs"`
	TimedOut   bool     `json:"timedOut"`
}

func callRunPrivileged(t *testing.T, session *mcp.ClientSession, name string) (runPrivilegedToolOutput, error) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_privileged_command",
		Arguments: map[string]any{"name": name},
	})
	if err != nil {
		return runPrivilegedToolOutput{}, err
	}
	if res.IsError {
		return runPrivilegedToolOutput{}, toolError(res)
	}
	var out runPrivilegedToolOutput
	data, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(data, &out); err != nil {
		return runPrivilegedToolOutput{}, err
	}
	return out, nil
}

func toolError(res *mcp.CallToolResult) error {
	var sb strings.Builder
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			sb.WriteString(text.Text)
		}
	}
	return &toolCallError{msg: sb.String()}
}

type toolCallError struct{ msg string }

func (e *toolCallError) Error() string { return e.msg }

// startDaemonForPrivilegedTool runs a real state daemon for a real workspace
// and returns a client session on its curated socket -- the same socket the
// sandbox's mcp-relay exposes to Claude.
func startDaemonForPrivilegedTool(t *testing.T, repoRoot string) *mcp.ClientSession {
	t.Helper()
	// A real workspace under the real data home, not newTestWorkspace's
	// isolated XDG_DATA_HOME: this run needs the kernel and VM scaffolding
	// scripts/setup-vm-host.sh installed there, which a temporary data home
	// does not have.
	id, err := workspace.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Create(repoRoot, id, "manual-privileged-tool", "develop", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Remove(id) })
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		t.Fatal(err)
	}
	worktreeDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- runStatedaemon(ctx, stateDir, repoRoot, worktreeDir) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(5 * time.Second):
			t.Error("runStatedaemon did not stop after context cancellation")
		}
	})

	curatedSocket := statedaemon.CuratedSocketPath(stateDir)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(curatedSocket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	httpClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", curatedSocket)
		},
	}}
	client := mcp.NewClient(&mcp.Implementation{Name: "manual-test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: "http://unix/", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}
