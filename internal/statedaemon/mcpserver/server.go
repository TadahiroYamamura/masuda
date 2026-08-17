// Package mcpserver wraps a statedaemon.Store as an MCP server exposing the
// full trusted tool set (state_get/state_put/state_wait_for_change/
// state_list/state_delete). This is the "trusted" side of Issue #35's design
// (host CLI, orchestrator/*.py) -- the curated, Claude-facing tool set
// (e.g. wait_for_gate_change) is a separate, narrower server built on the
// same Store, added in a later step.
package mcpserver

import (
	"context"
	"fmt"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// New returns an MCP server exposing store's full Get/Put/Delete/List/
// WaitForChange surface as tools. Callers serve it over whatever transport
// fits the caller (UDS today; see mcp.NewStreamableHTTPHandler).
func New(store *statedaemon.Store) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "masuda-statedaemon",
		Version: "0.1.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "state_get",
		Description: "Get the current value for a key, e.g. \"gate:plan\" or \"artifact:plan/summary.md\".",
	}, get(store))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "state_put",
		Description: "Set a key's value, persist it, and wake anyone blocked in state_wait_for_change on that key.",
	}, put(store))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "state_delete",
		Description: "Delete a key. Deleting a key that doesn't exist is not an error.",
	}, del(store))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "state_list",
		Description: "List every key currently starting with a prefix, e.g. \"gate:\" or \"review_results/\".",
	}, list(store))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "state_wait_for_change",
		Description: "Block until the next state_put or state_delete call on a key, then return its new value. The RPC equivalent of ADR-0017's single blocking inotifywait call -- one call per wait, no polling.",
	}, waitForChange(store))

	return server
}

type getInput struct {
	Key string `json:"key" jsonschema:"the key to read"`
}

type getOutput struct {
	Value string `json:"value" jsonschema:"the key's current value, empty if not found"`
	Found bool   `json:"found" jsonschema:"whether the key exists"`
}

func get(store *statedaemon.Store) mcp.ToolHandlerFor[getInput, getOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, getOutput, error) {
		v, ok := store.Get(in.Key)
		return nil, getOutput{Value: string(v), Found: ok}, nil
	}
}

type putInput struct {
	Key   string `json:"key" jsonschema:"the key to write"`
	Value string `json:"value" jsonschema:"the value to store"`
}

type putOutput struct{}

func put(store *statedaemon.Store) mcp.ToolHandlerFor[putInput, putOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in putInput) (*mcp.CallToolResult, putOutput, error) {
		if err := store.Put(in.Key, []byte(in.Value)); err != nil {
			return nil, putOutput{}, fmt.Errorf("state_put: %w", err)
		}
		return nil, putOutput{}, nil
	}
}

type deleteInput struct {
	Key string `json:"key" jsonschema:"the key to delete"`
}

type deleteOutput struct{}

func del(store *statedaemon.Store) mcp.ToolHandlerFor[deleteInput, deleteOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in deleteInput) (*mcp.CallToolResult, deleteOutput, error) {
		if err := store.Delete(in.Key); err != nil {
			return nil, deleteOutput{}, fmt.Errorf("state_delete: %w", err)
		}
		return nil, deleteOutput{}, nil
	}
}

type listInput struct {
	Prefix string `json:"prefix" jsonschema:"only keys starting with this prefix are returned"`
}

type listOutput struct {
	Keys []string `json:"keys" jsonschema:"matching keys, sorted"`
}

func list(store *statedaemon.Store) mcp.ToolHandlerFor[listInput, listOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, listOutput, error) {
		return nil, listOutput{Keys: store.List(in.Prefix)}, nil
	}
}

type waitForChangeInput struct {
	Key string `json:"key" jsonschema:"the key to wait on"`
}

type waitForChangeOutput struct {
	Value string `json:"value" jsonschema:"the key's new value, empty if it was deleted"`
	Found bool   `json:"found" jsonschema:"false if the key was deleted rather than written"`
}

func waitForChange(store *statedaemon.Store) mcp.ToolHandlerFor[waitForChangeInput, waitForChangeOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in waitForChangeInput) (*mcp.CallToolResult, waitForChangeOutput, error) {
		v, ok, err := store.WaitForChange(ctx, in.Key)
		if err != nil {
			return nil, waitForChangeOutput{}, fmt.Errorf("state_wait_for_change: %w", err)
		}
		return nil, waitForChangeOutput{Value: string(v), Found: ok}, nil
	}
}
