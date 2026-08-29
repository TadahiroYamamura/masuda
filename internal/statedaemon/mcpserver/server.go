// Package mcpserver wraps a statedaemon.Store as an MCP server exposing the
// full trusted tool set (state_get/state_put/state_list/state_delete/
// state_apply). This is the "trusted" side of Issue #35's design
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

// New returns an MCP server exposing store's Get/Put/Delete/List surface
// as tools. Blocking waits are deliberately absent here: the only thing
// anything waits on is a gate, and that belongs to the curated set
// (NewCurated's wait_for_gate_change), which can state the condition it is
// waiting for instead of asking for the next change to anything. Callers serve it over whatever transport
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
		Description: "Set a key's value, persist it, and wake anyone waiting on that key (e.g. the curated set's wait_for_gate_change).",
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
		Name: "state_apply",
		Description: "Apply several ops as one atomic step: every \"check\" op is evaluated first, and only if all of " +
			"them hold do the \"put\" and \"delete\" ops land. Reports applied=false, having changed nothing, when a " +
			"check does not hold -- read the key again and decide again. Use this to take a decision off a key and " +
			"record what you did with it in one step, so nothing can land in between and be destroyed unseen.",
	}, apply(store))

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

// applyOp mirrors statedaemon.Op on the wire. Value is a pointer so that
// "absent" and "the empty string" stay distinguishable: a check with no
// value asserts the key does not exist, which is a different question from a
// check against "".
type applyOp struct {
	Op    string  `json:"op" jsonschema:"one of \"check\", \"put\", \"delete\""`
	Key   string  `json:"key" jsonschema:"the key this op acts on"`
	Value *string `json:"value,omitempty" jsonschema:"put's new value, or check's expected current value; omit it on a check to require that the key does not exist"`
}

type applyInput struct {
	Ops []applyOp `json:"ops" jsonschema:"the ops to apply as one atomic step"`
}

type applyOutput struct {
	Applied bool `json:"applied" jsonschema:"false when a check did not hold, in which case nothing was changed"`
}

func apply(store *statedaemon.Store) mcp.ToolHandlerFor[applyInput, applyOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in applyInput) (*mcp.CallToolResult, applyOutput, error) {
		ops := make([]statedaemon.Op, 0, len(in.Ops))
		for i, o := range in.Ops {
			op := statedaemon.Op{Kind: statedaemon.OpKind(o.Op), Key: o.Key}
			if o.Value != nil {
				op.Value = []byte(*o.Value)
				if op.Value == nil {
					op.Value = []byte{}
				}
			} else if op.Kind == statedaemon.OpPut {
				return nil, applyOutput{}, fmt.Errorf("state_apply: ops[%d] puts %q with no value", i, o.Key)
			}
			ops = append(ops, op)
		}
		applied, err := store.Apply(ops)
		if err != nil {
			return nil, applyOutput{}, fmt.Errorf("state_apply: %w", err)
		}
		return nil, applyOutput{Applied: applied}, nil
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
