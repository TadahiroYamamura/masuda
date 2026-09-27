package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/workflow/host"
)

type nextTaskInput struct {
	PreviousOccurrence string `json:"previous_occurrence,omitempty" jsonschema:"the occurrence of the task you delegated last, if any"`
	PreviousAgentID    string `json:"previous_agent_id,omitempty" jsonschema:"the agent ID the subagent that did that task returned, if any"`
}

type reportResultInput struct {
	Occurrence string `json:"occurrence" jsonschema:"the occurrence named in the instruction file"`
	Outcome    string `json:"outcome" jsonschema:"one of the outcomes the instruction file lists"`
	Feedback   string `json:"feedback,omitempty" jsonschema:"what the next step should know"`
}

type reportResultOutput struct {
	Recorded bool `json:"recorded"`
}

type writeOutputInput struct {
	Occurrence string `json:"occurrence" jsonschema:"the occurrence named in the instruction file"`
	Name       string `json:"name" jsonschema:"the output name, as listed in the instruction file"`
	Content    string `json:"content" jsonschema:"the output's full content"`
}

type writeOutputOutput struct {
	Path string `json:"path" jsonschema:"where the output was stored"`
}

func addWorkflowTools(server *mcp.Server, w Workflow) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "next_task",
		Description: "Ask masuda what to do next. kind=agent: start the named subagent and tell it only to read the " +
			"instruction file at `instructions` and follow it -- do not copy the file's content into the delegation. " +
			"When the subagent returns, call next_task again with previous_occurrence and previous_agent_id. " +
			"kind=gate: call wait_for_gate_resolution with `gate`, then next_task again. kind=done or kind=blocked: stop.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nextTaskInput) (*mcp.CallToolResult, host.Next, error) {
		h, err := w.Get()
		if err != nil {
			return nil, host.Next{}, err
		}
		next, err := h.NextTask(in.PreviousOccurrence, in.PreviousAgentID)
		if err != nil {
			return nil, host.Next{}, fmt.Errorf("next_task: %w", err)
		}
		return nil, next, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "report_result",
		Description: "Report how the task in your instruction file ended. Call it once, at the end, with one of the " +
			"outcomes the instruction file lists. A refused report (unknown outcome, missing output) makes masuda " +
			"run the task again.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in reportResultInput) (*mcp.CallToolResult, reportResultOutput, error) {
		h, err := w.Get()
		if err != nil {
			return nil, reportResultOutput{}, err
		}
		if err := h.Report(in.Occurrence, in.Outcome, in.Feedback); err != nil {
			return nil, reportResultOutput{}, fmt.Errorf("report_result: %w", err)
		}
		return nil, reportResultOutput{Recorded: true}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "write_output",
		Description: "Write one of the outputs your instruction file lists. This is the only way outputs reach " +
			"masuda; files written directly are not picked up. Structured outputs are validated on the spot, and an " +
			"error says what to fix.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in writeOutputInput) (*mcp.CallToolResult, writeOutputOutput, error) {
		h, err := w.Get()
		if err != nil {
			return nil, writeOutputOutput{}, err
		}
		p, err := h.WriteOutput(in.Occurrence, in.Name, in.Content)
		if err != nil {
			return nil, writeOutputOutput{}, fmt.Errorf("write_output: %w", err)
		}
		return nil, writeOutputOutput{Path: p}, nil
	})
}

// waitForWorkflowGate waits for a human's decision on a workflow gate. The
// decision is left in place for the engine to validate and take on the
// next next_task call (ADR-0066).
func waitForWorkflowGate(ctx context.Context, store *statedaemon.Store, name string) (*mcp.CallToolResult, waitForGateResolutionOutput, error) {
	if _, open := store.Get("wf:gate-open/" + name); !open {
		if _, decided := store.Get("wf:gate-decision/" + name); !decided {
			return nil, waitForGateResolutionOutput{}, fmt.Errorf("wait_for_gate_resolution: gate %q is not open", name)
		}
	}
	value, err := store.WaitForPresence(ctx, "wf:gate-decision/"+name)
	if err != nil {
		return nil, waitForGateResolutionOutput{}, fmt.Errorf("wait_for_gate_resolution: %w", err)
	}
	var d struct {
		Approved bool   `json:"approved"`
		Comment  string `json:"comment"`
	}
	if err := json.Unmarshal(value, &d); err != nil {
		return nil, waitForGateResolutionOutput{}, err
	}
	status := "rejected"
	if d.Approved {
		status = "approved"
	}
	return nil, waitForGateResolutionOutput{Status: status, Feedback: d.Comment}, nil
}
