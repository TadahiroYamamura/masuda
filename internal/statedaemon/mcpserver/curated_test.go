package mcpserver

import (
	"context"
	"encoding/json"
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
	server := NewCurated(store)

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

func TestCuratedOnlyExposesWaitForGateChange(t *testing.T) {
	_, session := connectCurated(t)
	res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "wait_for_gate_change" {
		names := make([]string, len(res.Tools))
		for i, tool := range res.Tools {
			names[i] = tool.Name
		}
		t.Fatalf("curated tool list = %v, want exactly [wait_for_gate_change]", names)
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
