package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// gateNames is the fixed, small set of gates wait_for_gate_change accepts --
// deliberately not "any key": exposing the generic store to Claude the way
// the trusted tool set does would let it write (not just wait on) arbitrary
// state, including the triage gate ADR-0029 says Claude must never resolve
// itself. See New's curated variant, NewCurated, for the guest-facing
// (Claude) tool set this belongs to.
var gateNames = map[string]bool{"plan": true, "review": true, "triage": true}

// NewCurated returns an MCP server exposing only the narrow, read/wait-only
// tool set meant for Claude itself (the main session inside a sandbox,
// eventually reached over vsock/UDS -- see Issue #35's design notes). This
// is deliberately not store's full trusted surface (New): a subagent or the
// main session getting an unrestricted state_put would let it write gate
// markers itself, undermining ADR-0029's "Claude must never resolve its own
// triage gate" rule and the human-approval model gates exist for in general.
func NewCurated(store *statedaemon.Store) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "masuda-statedaemon-curated",
		Version: "0.1.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "wait_for_gate_change",
		Description: "Block until the given gate (\"plan\", \"review\", or \"triage\") is resolved by a human, " +
			"then return its new status. The equivalent of ADR-0017's single blocking inotifywait call for the " +
			"GATE:<name> loop step -- one call per wait, no polling.",
	}, waitForGateChange(store))

	return server
}

type waitForGateChangeInput struct {
	Name string `json:"name" jsonschema:"the gate to wait on: \"plan\", \"review\", or \"triage\""`
}

type waitForGateChangeOutput struct {
	Status   string `json:"status" jsonschema:"the gate's new status: approved, rejected, or halted"`
	Feedback string `json:"feedback,omitempty" jsonschema:"human-provided feedback, if any"`
}

func waitForGateChange(store *statedaemon.Store) mcp.ToolHandlerFor[waitForGateChangeInput, waitForGateChangeOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in waitForGateChangeInput) (*mcp.CallToolResult, waitForGateChangeOutput, error) {
		if !gateNames[in.Name] {
			return nil, waitForGateChangeOutput{}, fmt.Errorf(
				"wait_for_gate_change: unknown gate %q, want \"plan\", \"review\", or \"triage\"", in.Name)
		}
		value, found, err := store.WaitForChange(ctx, "gate:"+in.Name)
		if err != nil {
			return nil, waitForGateChangeOutput{}, fmt.Errorf("wait_for_gate_change: %w", err)
		}
		if !found {
			// Gate markers are only ever replaced with a new decision
			// (Approve/Reject/Halt all Put), never deleted -- this would
			// mean something else deleted the key out from under us.
			return nil, waitForGateChangeOutput{}, fmt.Errorf("wait_for_gate_change: gate %q was cleared rather than resolved", in.Name)
		}
		var marker struct {
			Status   string `json:"status"`
			Feedback string `json:"feedback,omitempty"`
		}
		if err := json.Unmarshal([]byte(value), &marker); err != nil {
			return nil, waitForGateChangeOutput{}, fmt.Errorf("wait_for_gate_change: parsing marker: %w", err)
		}
		return nil, waitForGateChangeOutput{Status: marker.Status, Feedback: marker.Feedback}, nil
	}
}
