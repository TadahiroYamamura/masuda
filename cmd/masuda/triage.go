package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/workflow/engine"
)

// newTriageCommand builds `masuda triage ...` (ADR-0029). The triage gate
// is reserved by the engine and opens when an agent reports a security
// concern, wherever the workflow stands. Besides dismiss and redo it
// offers halt, which stops the run with no automatic way back.
func newTriageCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "triage",
		Short: "Decide on a security concern an agent reported (ADR-0029)",
	}
	cmd.AddCommand(newTriageShowCommand())
	cmd.AddCommand(newTriageDecideCommand("dismiss", "<workspace-id> [comment]", "Dismiss the concern as a false positive; the agent continues its task", 1, 2,
		func(args []string) engine.Decision {
			return engine.Decision{Approved: true, Comment: optional(args, 1)}
		}))
	cmd.AddCommand(newTriageDecideCommand("redo", "<workspace-id> <instructions>", "Send the agent back to its task with your instructions, after addressing the concern", 2, 2,
		func(args []string) engine.Decision { return engine.Decision{Approved: false, Comment: args[1]} }))
	cmd.AddCommand(newTriageDecideCommand("halt", "<workspace-id> [reason]", "Stop the workflow over a confirmed concern (no automatic resume)", 1, 2,
		func(args []string) engine.Decision { return engine.Decision{Halt: true, Comment: optional(args, 1)} }))
	return cmd
}

func optional(args []string, i int) string {
	if len(args) > i {
		return args[i]
	}
	return ""
}

func newTriageShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "show <workspace-id>",
		Short:             "Print the concern an agent reported",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req, c, _, err := openGateRequest(cmd, args[0], "triage")
			if err != nil {
				return err
			}
			defer c.Close()
			fmt.Fprintf(cmd.OutOrStdout(), "reported by task %s:\n\n%s\n", req.Occurrence, req.Detail)
			return nil
		},
	}
}

// newTriageDecideCommand writes a triage decision. halt deliberately does
// nothing else: no teardown, no commit, no worktree operation. Everything
// is left as found for a human to investigate (ADR-0029).
func newTriageDecideCommand(use, argsUse, short string, minArgs, maxArgs int, decide func([]string) engine.Decision) *cobra.Command {
	return &cobra.Command{
		Use:               use + " " + argsUse,
		Short:             short,
		Args:              cobra.RangeArgs(minArgs, maxArgs),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req, c, _, err := openGateRequest(cmd, args[0], "triage")
			if err != nil {
				return err
			}
			defer c.Close()
			d := decide(args)
			d.Occurrence, d.Hash = req.Occurrence, req.Hash
			b, err := json.Marshal(d)
			if err != nil {
				return err
			}
			if err := c.Put(cmd.Context(), "wf:gate-decision/triage", string(b)); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "triage: %s (task %s)\n", use, req.Occurrence)
			return nil
		},
	}
}
