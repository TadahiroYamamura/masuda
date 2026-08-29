package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectCurated wires a fresh Store-backed curated server to a client over
// an in-memory transport pair, mirroring server_test.go's connect for the
// trusted server.
func connectCurated(t *testing.T) (*statedaemon.Store, *mcp.ClientSession) {
	t.Helper()
	store, err := statedaemon.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewCurated(store, nil)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()

	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return store, session
}

func TestCuratedExposesExactlyTheHumanApprovalFlowTools(t *testing.T) {
	_, session := connectCurated(t)
	res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(res.Tools))
	for i, tool := range res.Tools {
		names[i] = tool.Name
	}
	want := map[string]bool{"wait_for_gate_change": true, "resolve_gate_from_chat": true}
	if len(names) != len(want) {
		t.Fatalf("curated tool list = %v, want exactly %v", names, want)
	}
	for _, name := range names {
		if !want[name] {
			t.Fatalf("curated tool list = %v, want exactly %v", names, want)
		}
	}
}

func TestWaitForGateChangeReturnsResolvedMarker(t *testing.T) {
	store, session := connectCurated(t)

	type waitResult struct {
		Status   string `json:"status"`
		Feedback string `json:"feedback"`
	}
	done := make(chan waitResult, 1)
	go func() {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
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

	select {
	case <-done:
		t.Fatal("wait_for_gate_change returned before the gate was resolved")
	case <-time.After(100 * time.Millisecond):
	}

	if err := store.Put("gate:plan", []byte(`{"status":"approved","feedback":"lgtm"}`)); err != nil {
		t.Fatal(err)
	}

	select {
	case out := <-done:
		if out.Status != "approved" || out.Feedback != "lgtm" {
			t.Fatalf("wait_for_gate_change result = %+v, want status=approved feedback=lgtm", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait_for_gate_change did not return after the gate was resolved")
	}
}

func TestWaitForGateChangeRejectsUnknownGateName(t *testing.T) {
	_, session := connectCurated(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "wait_for_gate_change",
		Arguments: map[string]any{"name": "not-a-real-gate"},
	})
	if err != nil {
		t.Fatalf("CallTool error = %v, want a tool-level error instead", err)
	}
	if !res.IsError {
		t.Fatal("wait_for_gate_change with an unknown gate name: IsError = false, want true")
	}
}

func TestResolveGateFromChatWritesMarkerWaitForGateChangeSees(t *testing.T) {
	store, session := connectCurated(t)

	type waitResult struct {
		Status   string `json:"status"`
		Feedback string `json:"feedback"`
	}
	done := make(chan waitResult, 1)
	go func() {
		value, err := store.WaitForPresence(context.Background(), "gate:review")
		if err != nil {
			t.Errorf("WaitForPresence error = %v, want nil", err)
			done <- waitResult{}
			return
		}
		var out waitResult
		json.Unmarshal([]byte(value), &out)
		done <- out
	}()
	time.Sleep(50 * time.Millisecond)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "resolve_gate_from_chat",
		Arguments: map[string]any{"name": "review", "status": "approved", "feedback": "対話で承認"},
	})
	if err != nil || res.IsError {
		t.Fatalf("CallTool(resolve_gate_from_chat) = (%+v, %v), want success", res, err)
	}

	select {
	case out := <-done:
		if out.Status != "approved" || out.Feedback != "対話で承認" {
			t.Fatalf("marker written by resolve_gate_from_chat = %+v, want status=approved feedback=対話で承認", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForPresence did not see resolve_gate_from_chat's write")
	}
}

func TestResolveGateFromChatRejectsTriage(t *testing.T) {
	store, session := connectCurated(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "resolve_gate_from_chat",
		Arguments: map[string]any{"name": "triage", "status": "approved"},
	})
	if err != nil {
		t.Fatalf("CallTool error = %v, want a tool-level error instead", err)
	}
	if !res.IsError {
		t.Fatal("resolve_gate_from_chat on the triage gate: IsError = false, want true -- ADR-0029 must block this")
	}
	if _, found := store.Get("gate:triage"); found {
		t.Fatal("resolve_gate_from_chat must not have written gate:triage despite the error")
	}
}

func TestResolveGateFromChatRejectsInvalidStatus(t *testing.T) {
	_, session := connectCurated(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "resolve_gate_from_chat",
		Arguments: map[string]any{"name": "plan", "status": "halted"},
	})
	if err != nil {
		t.Fatalf("CallTool error = %v, want a tool-level error instead", err)
	}
	if !res.IsError {
		t.Fatal("resolve_gate_from_chat with status=halted: IsError = false, want true (only approved/rejected allowed)")
	}
}

// connectCuratedWithRunner is connectCurated with the privileged-command
// tool registered against a stub runner, so the tool surface can be
// exercised without a VM.
func connectCuratedWithRunner(t *testing.T, run PrivilegedRunner) *mcp.ClientSession {
	t.Helper()
	store, err := statedaemon.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewCurated(store, run)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// A daemon with no workspace to act on must not advertise the tool at all
// (ADR-0053): a tool that is offered and always fails is worse than one
// that was never listed.
func TestCuratedOffersRunPrivilegedCommandOnlyWithARunner(t *testing.T) {
	session := connectCuratedWithRunner(t, func(context.Context, string) (PrivilegedRunResult, error) {
		return PrivilegedRunResult{}, nil
	})
	res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tool := range res.Tools {
		if tool.Name == "run_privileged_command" {
			found = true
		}
	}
	if !found {
		t.Fatal("run_privileged_command not offered even though a runner was supplied")
	}
}

// The narrowness is the design (ADR-0053): a session names a declaration,
// and never supplies a command line of its own.
func TestRunPrivilegedCommandPassesOnlyTheName(t *testing.T) {
	var gotName string
	session := connectCuratedWithRunner(t, func(_ context.Context, name string) (PrivilegedRunResult, error) {
		gotName = name
		return PrivilegedRunResult{
			ExitCode:   0,
			Log:        "all good\n",
			ResultsDir: "/masuda-state/privilegedCommands/e2e/abc123",
			Outputs:    []string{"report.txt"},
		}, nil
	})

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_privileged_command",
		Arguments: map[string]any{"name": "e2e"},
	})
	if err != nil || res.IsError {
		t.Fatalf("CallTool = (%+v, %v), want success", res, err)
	}
	if gotName != "e2e" {
		t.Errorf("runner called with %q, want %q", gotName, "e2e")
	}

	var out struct {
		ExitCode   int      `json:"exitCode"`
		Log        string   `json:"log"`
		Truncated  bool     `json:"truncated"`
		ResultsDir string   `json:"resultsDir"`
		Outputs    []string `json:"outputs"`
	}
	data, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 0 || out.Log != "all good\n" || out.Truncated {
		t.Errorf("output = %+v, want the runner's result verbatim", out)
	}
	if out.ResultsDir != "/masuda-state/privilegedCommands/e2e/abc123" {
		t.Errorf("resultsDir = %q, want the path as this session sees it", out.ResultsDir)
	}
	if len(out.Outputs) != 1 || out.Outputs[0] != "report.txt" {
		t.Errorf("outputs = %v, want [report.txt]", out.Outputs)
	}
}

// A refusal (undeclared, unapproved, or changed since approval) has to reach
// the session as an error it can relay to a human, not as a silent zero
// result.
func TestRunPrivilegedCommandSurfacesRefusals(t *testing.T) {
	session := connectCuratedWithRunner(t, func(context.Context, string) (PrivilegedRunResult, error) {
		return PrivilegedRunResult{}, errors.New(`privileged command "e2e" is declared but not approved`)
	})

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_privileged_command",
		Arguments: map[string]any{"name": "e2e"},
	})
	if err == nil && !res.IsError {
		t.Fatal("CallTool succeeded, want the refusal surfaced")
	}
}

func TestTruncateLogKeepsTheEnd(t *testing.T) {
	log := strings.Repeat("a", 10) + "THE FAILURE"
	got, truncated := truncateLog(log, 11)
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	// What a session needs from a failed run is where the output stopped.
	if got != "THE FAILURE" {
		t.Fatalf("truncateLog() = %q, want the tail", got)
	}
	if short, truncated := truncateLog("fits", 11); truncated || short != "fits" {
		t.Fatalf("truncateLog(short) = (%q, %v), want it untouched", short, truncated)
	}
}
