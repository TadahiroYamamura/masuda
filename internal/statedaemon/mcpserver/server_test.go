package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect wires a fresh Store-backed server to a client over an in-memory
// transport pair and returns the connected session, closing everything on
// test cleanup.
func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	store, err := statedaemon.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := New(store)

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

// callTool calls name with args and decodes the result's StructuredContent
// into out.
func callTool(t *testing.T, session *mcp.ClientSession, name string, args, out any) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool(%s) returned a tool error: %+v", name, res.Content)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decoding %s result: %v", name, err)
	}
}

func TestPutGetRoundTripOverMCP(t *testing.T) {
	session := connect(t)

	var putOut struct{}
	callTool(t, session, "state_put", map[string]any{"key": "gate:plan", "value": `{"status":"pending"}`}, &putOut)

	var getOut struct {
		Value string `json:"value"`
		Found bool   `json:"found"`
	}
	callTool(t, session, "state_get", map[string]any{"key": "gate:plan"}, &getOut)
	if !getOut.Found || getOut.Value != `{"status":"pending"}` {
		t.Fatalf("state_get = %+v, want found=true value=%q", getOut, `{"status":"pending"}`)
	}
}

func TestGetMissingKeyOverMCP(t *testing.T) {
	session := connect(t)

	var getOut struct {
		Value string `json:"value"`
		Found bool   `json:"found"`
	}
	callTool(t, session, "state_get", map[string]any{"key": "gate:plan"}, &getOut)
	if getOut.Found {
		t.Fatalf("state_get = %+v, want found=false", getOut)
	}
}

func TestDeleteOverMCP(t *testing.T) {
	session := connect(t)

	var putOut struct{}
	callTool(t, session, "state_put", map[string]any{"key": "gate:plan", "value": "v"}, &putOut)

	var deleteOut struct{}
	callTool(t, session, "state_delete", map[string]any{"key": "gate:plan"}, &deleteOut)

	var getOut struct {
		Value string `json:"value"`
		Found bool   `json:"found"`
	}
	callTool(t, session, "state_get", map[string]any{"key": "gate:plan"}, &getOut)
	if getOut.Found {
		t.Fatalf("state_get after state_delete = %+v, want found=false", getOut)
	}
}

func TestListOverMCP(t *testing.T) {
	session := connect(t)

	var putOut struct{}
	callTool(t, session, "state_put", map[string]any{"key": "gate:plan", "value": "v"}, &putOut)
	callTool(t, session, "state_put", map[string]any{"key": "gate:review", "value": "v"}, &putOut)
	callTool(t, session, "state_put", map[string]any{"key": "artifact:plan/summary.md", "value": "v"}, &putOut)

	var listOut struct {
		Keys []string `json:"keys"`
	}
	callTool(t, session, "state_list", map[string]any{"prefix": "gate:"}, &listOut)
	want := []string{"gate:plan", "gate:review"}
	if len(listOut.Keys) != len(want) || listOut.Keys[0] != want[0] || listOut.Keys[1] != want[1] {
		t.Fatalf("state_list = %v, want %v", listOut.Keys, want)
	}
}

func TestPutRejectedForInvalidKeyOverMCP(t *testing.T) {
	session := connect(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "state_put",
		Arguments: map[string]any{"key": "no-namespace", "value": "v"},
	})
	if err != nil {
		t.Fatalf("CallTool error = %v, want a tool-level error instead", err)
	}
	if !res.IsError {
		t.Fatal("state_put with an invalid key: IsError = false, want true")
	}
}

func TestApplyOverMCP(t *testing.T) {
	session := connect(t)

	var putOut struct{}
	callTool(t, session, "state_put", map[string]any{"key": "gate:plan", "value": `{"status":"approved"}`}, &putOut)

	consume := []map[string]any{
		{"op": "check", "key": "gate:plan", "value": `{"status":"approved"}`},
		{"op": "put", "key": "internal:plan-approved", "value": `{"status":"approved"}`},
		{"op": "delete", "key": "gate:plan"},
	}
	var applyOut struct {
		Applied bool `json:"applied"`
	}
	callTool(t, session, "state_apply", map[string]any{"ops": consume}, &applyOut)
	if !applyOut.Applied {
		t.Fatal("state_apply applied = false, want true")
	}

	var getOut struct {
		Value string `json:"value"`
		Found bool   `json:"found"`
	}
	callTool(t, session, "state_get", map[string]any{"key": "gate:plan"}, &getOut)
	if getOut.Found {
		t.Error("gate:plan still found after the apply that consumed it")
	}
	callTool(t, session, "state_get", map[string]any{"key": "internal:plan-approved"}, &getOut)
	if !getOut.Found || getOut.Value != `{"status":"approved"}` {
		t.Errorf("internal:plan-approved = %+v, want the consumed decision", getOut)
	}

	// Replaying the same consume now that the marker is gone must lose on the
	// check rather than write the durable record a second time.
	callTool(t, session, "state_apply", map[string]any{"ops": consume}, &applyOut)
	if applyOut.Applied {
		t.Error("state_apply applied = true on a replay, want false")
	}
}

func TestApplyCheckWithoutValueMeansAbsentOverMCP(t *testing.T) {
	session := connect(t)

	var applyOut struct {
		Applied bool `json:"applied"`
	}
	callTool(t, session, "state_apply", map[string]any{"ops": []map[string]any{
		{"op": "check", "key": "gate:plan"},
		{"op": "put", "key": "gate:plan", "value": "v"},
	}}, &applyOut)
	if !applyOut.Applied {
		t.Fatal("state_apply applied = false while gate:plan was absent, want true")
	}

	callTool(t, session, "state_apply", map[string]any{"ops": []map[string]any{
		{"op": "check", "key": "gate:plan"},
		{"op": "put", "key": "gate:review", "value": "v"},
	}}, &applyOut)
	if applyOut.Applied {
		t.Error("state_apply applied = true with an absent-check on a present key, want false")
	}
}

func TestApplyRejectsPutWithoutValue(t *testing.T) {
	session := connect(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "state_apply",
		Arguments: map[string]any{"ops": []map[string]any{
			{"op": "put", "key": "gate:plan"},
		}},
	})
	if err != nil {
		t.Fatalf("CallTool error = %v, want a tool-level error instead", err)
	}
	if !res.IsError {
		t.Fatal("state_apply with a valueless put: IsError = false, want true")
	}
}
