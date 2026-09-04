package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// gateNames is the fixed, small set of gates wait_for_gate_resolution accepts --
// deliberately not "any key": exposing the generic store to Claude the way
// the trusted tool set does would let it write (not just wait on) arbitrary
// state, including the triage gate ADR-0029 says Claude must never resolve
// itself. See New's curated variant, NewCurated, for the guest-facing
// (Claude) tool set this belongs to.
var gateNames = map[string]bool{"plan": true, "review": true, "triage": true}

// chatResolvableGateNames is the gates resolve_gate_from_chat may close --
// runtime/CLAUDE.md's documented "a human told me to proceed mid-chat"
// escape hatch. The rule is that approving a gate this way must have no
// effect outside the workspace (ADR-0060):
//
//   - plan: approval only unblocks the loop. Allowed.
//   - review: approval fast-forwards the workspace's branch into the user's
//     real repository and tears the workspace down (finalizeReviewApproval).
//     A human has to run `masuda review approve` from the host for that.
//   - triage: the agent under suspicion must never close its own concern
//     (ADR-0029).
//
// Before this tool existed, all of that was enforced only by instructing
// Claude not to self-write the marker file -- a convention, not a technical
// boundary (Issue #13). Since ADR-0057 left the guest no other way to write
// a gate at all, this map is the boundary.
var chatResolvableGateNames = map[string]bool{"plan": true}

// NewCurated returns an MCP server exposing the narrow, human-approval-flow
// tool set meant for Claude itself (the main session inside a sandbox,
// eventually reached over vsock/UDS -- see Issue #35's design notes). This
// is deliberately not store's full trusted surface (New): a subagent or the
// main session getting an unrestricted state_put would let it write gate
// markers itself, undermining ADR-0029's "Claude must never resolve its own
// triage gate" rule and the human-approval model gates exist for in general.
// PrivilegedRunResult is what a finished privileged command reports back
// (ADR-0053). A local shape rather than internal/sandbox's: that package's
// own tests import this one, so depending on it here would be a cycle --
// and the two are answering different questions anyway (how a VM went, vs.
// what a session is told).
type PrivilegedRunResult struct {
	ExitCode int
	Log      string
	// ResultsDir is the run's directory as *this session* sees it, not as
	// the host does: it is what the session would open to read the full log
	// or a collected artifact.
	ResultsDir   string
	Outputs      []string
	OutputsError string
	TimedOut     bool
}

// PrivilegedRunner runs the declared command called name, after checking
// that a human approved that exact declaration. Injected rather than
// imported for the cycle above; cmd/masuda supplies the real one.
//
// Nil leaves run_privileged_command unregistered entirely -- the standalone
// daemon (no repository, see runStatedaemon's doc comment) has nothing to
// run a privileged command against, and a tool that always errors is worse
// than one that is not offered.
type PrivilegedRunner func(ctx context.Context, name string) (PrivilegedRunResult, error)

// maxToolLogBytes bounds what a run's log contributes to a tool result. The
// full log (up to the guest-side hard cap) stays on disk in the run
// directory, which the session can read as a file, so this only decides how
// much arrives inline. Sized as a realistic fraction of a model's context
// window; a plain constant to change when that changes, not a decision
// (ADR-0053).
const maxToolLogBytes = 200 << 10

// OrchestratorRunner advances the Build/Review orchestrator by one turn
// (detect_phase -> write_task_md) and returns the task text it produced.
// Injected for the same reason PrivilegedRunner is: running it means
// spawning a Python process against a worktree, which is cmd/masuda's
// business, not this package's.
//
// The task text is returned rather than left for the caller to read off
// TASK.md. The guest reaches this tool across a virtiofs share whose
// host->guest attribute cache lags by up to a second (measured ~0.5s), so
// a session told to "read TASK.md now" can legitimately read the previous
// turn's file. Handing back the body sidesteps that entirely -- and the
// orchestrator still writes TASK.md, which stays the durable record a
// human (or a resumed session) can read.
//
// Nil leaves next_task unregistered, the same way a nil PrivilegedRunner
// leaves run_privileged_command unregistered: a daemon with no worktree to
// run an orchestrator against has nothing to offer here.
type OrchestratorRunner func(ctx context.Context) (task string, err error)

func NewCurated(store *statedaemon.Store, runPrivileged PrivilegedRunner, runOrchestrator OrchestratorRunner) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "masuda-statedaemon-curated",
		Version: "0.1.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "wait_for_gate_resolution",
		Description: "Block until the given gate (\"plan\", \"review\", or \"triage\") is resolved by a human, " +
			"then return its status. Returns immediately when the gate is already resolved, so calling it again " +
			"after a dropped connection is safe and costs nothing. One call per wait, no polling.",
	}, waitForGateResolution(store))

	mcp.AddTool(server, &mcp.Tool{
		Name: "resolve_gate_from_chat",
		Description: "Resolve the \"plan\" gate yourself, only when a human told you to during a live " +
			"`masuda chat` conversation. Only \"plan\": approving it just unblocks the loop, while the other two " +
			"gates do something a human has to trigger from the host. \"review\" also lands the branch in the " +
			"real repository and removes the workspace, so approving it needs `masuda review approve " +
			"<workspace-id>`; \"triage\" must never be closed by the agent under the concern (ADR-0029). " +
			"If a human approves either of those in chat, tell them which command to run and keep waiting.",
	}, resolveGateFromChat(store))

	if runPrivileged != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name: "run_privileged_command",
			Description: "Run one of this repository's declared privileged commands (.masuda/settings.json's " +
				"privilegedCommands) in a throwaway VM that has root and a Docker daemon, and return its exit code " +
				"and log. Takes only the command's name: what actually runs comes from the declaration a human " +
				"approved, so there is no way to pass a command line of your own. Blocks until the run finishes. " +
				"If a command you need is missing or unapproved, say so and ask the human -- you cannot approve it.",
		}, runPrivilegedCommand(runPrivileged))
	}

	if runOrchestrator != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name: "next_task",
			Description: "Ask masuda what to work on next, and get the task text back. Call it when you have no " +
				"current task, and again each time you finish one and neither the DONE nor the GATE condition holds. " +
				"Takes no arguments: what the next task is follows from the workspace's own state, never from " +
				"anything you pass. The same text is also written to TASK.md, but use what this returns -- the file " +
				"you can see may still be the previous turn's.",
		}, nextTask(runOrchestrator))
	}

	return server
}

type nextTaskInput struct{}

type nextTaskOutput struct {
	Task string `json:"task" jsonschema:"the task to work on next, in the same form TASK.md holds"`
}

func nextTask(run OrchestratorRunner) mcp.ToolHandlerFor[nextTaskInput, nextTaskOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ nextTaskInput) (*mcp.CallToolResult, nextTaskOutput, error) {
		task, err := run(ctx)
		if err != nil {
			return nil, nextTaskOutput{}, fmt.Errorf("next_task: %w", err)
		}
		return nil, nextTaskOutput{Task: task}, nil
	}
}

type runPrivilegedCommandInput struct {
	Name string `json:"name" jsonschema:"the name of a command declared under privilegedCommands in .masuda/settings.json"`
}

type runPrivilegedCommandOutput struct {
	ExitCode     int      `json:"exitCode" jsonschema:"the command's exit status; -1 if it never reported one"`
	Log          string   `json:"log" jsonschema:"the command's combined stdout and stderr, truncated"`
	Truncated    bool     `json:"truncated,omitempty" jsonschema:"true when log was cut; the full log is the \"log\" file in resultsDir"`
	ResultsDir   string   `json:"resultsDir" jsonschema:"directory holding this run's log, exit code and collected outputs, readable from this session"`
	Outputs      []string `json:"outputs,omitempty" jsonschema:"files collected from the run, relative to resultsDir/outputs"`
	OutputsError string   `json:"outputsError,omitempty" jsonschema:"why the declared outputs were not all collected, if any were not"`
	TimedOut     bool     `json:"timedOut,omitempty" jsonschema:"true when masuda gave up waiting for the VM rather than the command finishing"`
}

// runPrivilegedCommand is the AI-facing entry to ADR-0053's disposable VM.
// The narrowness is the design: the input is a name, never a command, so
// the worst an unconstrained session can do is run something a human
// already read and approved.
func runPrivilegedCommand(run PrivilegedRunner) mcp.ToolHandlerFor[runPrivilegedCommandInput, runPrivilegedCommandOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in runPrivilegedCommandInput) (*mcp.CallToolResult, runPrivilegedCommandOutput, error) {
		result, err := run(ctx, in.Name)
		if err != nil {
			return nil, runPrivilegedCommandOutput{}, fmt.Errorf("run_privileged_command: %w", err)
		}
		log, truncated := truncateLog(result.Log, maxToolLogBytes)
		return nil, runPrivilegedCommandOutput{
			ExitCode:     result.ExitCode,
			Log:          log,
			Truncated:    truncated,
			ResultsDir:   result.ResultsDir,
			Outputs:      result.Outputs,
			OutputsError: result.OutputsError,
			TimedOut:     result.TimedOut,
		}, nil
	}
}

// truncateLog keeps the *end* of an oversized log. What a session needs
// from a failed run is almost always the failure, which is where the output
// stopped, not the setup chatter it started with.
func truncateLog(log string, max int) (string, bool) {
	if len(log) <= max {
		return log, false
	}
	return log[len(log)-max:], true
}

type waitForGateResolutionInput struct {
	Name string `json:"name" jsonschema:"the gate to wait on: \"plan\", \"review\", or \"triage\""`
}

type waitForGateResolutionOutput struct {
	Status   string `json:"status" jsonschema:"the gate's new status: approved, rejected, or halted"`
	Feedback string `json:"feedback,omitempty" jsonschema:"human-provided feedback, if any"`
}

func waitForGateResolution(store *statedaemon.Store) mcp.ToolHandlerFor[waitForGateResolutionInput, waitForGateResolutionOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in waitForGateResolutionInput) (*mcp.CallToolResult, waitForGateResolutionOutput, error) {
		if !gateNames[in.Name] {
			return nil, waitForGateResolutionOutput{}, fmt.Errorf(
				"wait_for_gate_resolution: unknown gate %q, want \"plan\", \"review\", or \"triage\"", in.Name)
		}
		// An absent marker *is* the unresolved state (nothing ever writes a
		// "pending" one -- orchestrator/*.py reads a missing key as pending
		// and deletes the marker once it has consumed the decision), so
		// waiting for the key to exist is exactly waiting for a human to
		// decide.
		value, err := store.WaitForPresence(ctx, "gate:"+in.Name)
		if err != nil {
			return nil, waitForGateResolutionOutput{}, fmt.Errorf("wait_for_gate_resolution: %w", err)
		}
		var marker struct {
			Status   string `json:"status"`
			Feedback string `json:"feedback,omitempty"`
		}
		if err := json.Unmarshal([]byte(value), &marker); err != nil {
			return nil, waitForGateResolutionOutput{}, fmt.Errorf("wait_for_gate_resolution: parsing marker: %w", err)
		}
		return nil, waitForGateResolutionOutput{Status: marker.Status, Feedback: marker.Feedback}, nil
	}
}

type resolveGateFromChatInput struct {
	Name     string `json:"name" jsonschema:"the gate to resolve: \"plan\" or \"review\" only, never \"triage\""`
	Status   string `json:"status" jsonschema:"\"approved\" or \"rejected\""`
	Feedback string `json:"feedback,omitempty" jsonschema:"a summary of what the human said in chat"`
}

type resolveGateFromChatOutput struct{}

func resolveGateFromChat(store *statedaemon.Store) mcp.ToolHandlerFor[resolveGateFromChatInput, resolveGateFromChatOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in resolveGateFromChatInput) (*mcp.CallToolResult, resolveGateFromChatOutput, error) {
		if !chatResolvableGateNames[in.Name] {
			return nil, resolveGateFromChatOutput{}, fmt.Errorf(
				"resolve_gate_from_chat: gate %q cannot be resolved this way -- only %q can. "+
					"For \"review\", approval also lands the branch in the real repository and removes the "+
					"workspace, so a human must run `masuda review approve <workspace-id>` from the host "+
					"(ADR-0060). For \"triage\", the agent under a concern must never close it (ADR-0029). "+
					"Report what the human said and wait; do not try to work around this.", in.Name, "plan")
		}
		if in.Status != "approved" && in.Status != "rejected" {
			return nil, resolveGateFromChatOutput{}, fmt.Errorf(
				"resolve_gate_from_chat: status must be \"approved\" or \"rejected\", got %q", in.Status)
		}
		marker := struct {
			Status    string    `json:"status"`
			Feedback  string    `json:"feedback,omitempty"`
			DecidedAt time.Time `json:"decided_at"`
		}{Status: in.Status, Feedback: in.Feedback, DecidedAt: time.Now()}
		data, err := json.Marshal(marker)
		if err != nil {
			return nil, resolveGateFromChatOutput{}, err
		}
		if err := store.Put("gate:"+in.Name, data); err != nil {
			return nil, resolveGateFromChatOutput{}, fmt.Errorf("resolve_gate_from_chat: %w", err)
		}
		return nil, resolveGateFromChatOutput{}, nil
	}
}
