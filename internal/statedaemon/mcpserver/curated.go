package mcpserver

import (
	"context"
	"fmt"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/workflow/host"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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

// Workflow is the workflow engine as the curated tools see it; nil leaves
// the workflow tools unregistered (a daemon with no workspace to run).
type Workflow interface {
	Get() (*host.Host, error)
}

func NewCurated(store *statedaemon.Store, runPrivileged PrivilegedRunner, workflow ...Workflow) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "masuda-statedaemon-curated",
		Version: "0.1.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "wait_for_gate_resolution",
		Description: "Block until the gate next_task named is resolved by a human on the host, " +
			"then return its status. Returns immediately when the gate is already resolved, so calling it again " +
			"after a dropped connection is safe and costs nothing. One call per wait, no polling.",
	}, waitForGateResolution(store))

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

	if len(workflow) > 0 && workflow[0] != nil {
		addWorkflowTools(server, workflow[0])
	}

	return server
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
	Name string `json:"name" jsonschema:"the gate to wait on, as next_task named it"`
}

type waitForGateResolutionOutput struct {
	Status   string `json:"status" jsonschema:"the gate's new status: approved, rejected, or halted"`
	Feedback string `json:"feedback,omitempty" jsonschema:"human-provided feedback, if any"`
}

// waitForGateResolution waits for a human's decision on a gate the
// workflow engine opened, triage included (ADR-0066).
func waitForGateResolution(store *statedaemon.Store) mcp.ToolHandlerFor[waitForGateResolutionInput, waitForGateResolutionOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in waitForGateResolutionInput) (*mcp.CallToolResult, waitForGateResolutionOutput, error) {
		return waitForWorkflowGate(ctx, store, in.Name)
	}
}
