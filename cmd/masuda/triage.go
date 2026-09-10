package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/gate"
)

// newTriageCommand builds `masuda triage ...` (ADR-0029). Unlike
// newGateCommand's shared show/approve/reject shape, the triage gate's
// resolution options are show/dismiss/redo/halt: dismiss and redo are
// approve/reject under a different name (see gate.Approve/gate.Reject), but
// halt has no G1/G2 equivalent — it's a dead end with no automatic resume,
// so it gets its own command rather than being folded into newGateCommand.
func newTriageCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "triage",
		Short: "Operate on the triage gate for a workspace (ADR-0029)",
	}
	cmd.AddCommand(newTriageShowCommand())
	cmd.AddCommand(newTriageDismissCommand())
	cmd.AddCommand(newTriageRedoCommand())
	cmd.AddCommand(newTriageHaltCommand())
	return cmd
}

func newTriageShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "show <workspace-id>",
		Short:             "Print the concern self-reported to the triage gate",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stateDir, err := ensureGateWorkspace(args[0])
			if err != nil {
				return err
			}
			content, err := gate.Show(cmd.Context(), stateDir, gate.Triage)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), content)
			return nil
		},
	}
}

func newTriageDismissCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "dismiss <workspace-id> [feedback]",
		Short:             "Dismiss the triage concern as a false positive and continue",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stateDir, err := ensureGateWorkspace(args[0])
			if err != nil {
				return err
			}
			feedback := ""
			if len(args) > 1 {
				feedback = args[1]
			}
			return gate.Approve(cmd.Context(), stateDir, gate.Triage, feedback)
		},
	}
}

func newTriageRedoCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "redo <workspace-id> <feedback>",
		Short:             "Redo the interrupted work after addressing the triage concern",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stateDir, err := ensureGateWorkspace(args[0])
			if err != nil {
				return err
			}
			return gate.Reject(cmd.Context(), stateDir, gate.Triage, args[1])
		},
	}
}

// newTriageHaltCommand is deliberately the entire implementation of "halt" —
// no sandbox teardown, no commit, no worktree operation (ADR-0029). Once a
// human halts a workspace, masuda provides no automatic path back; everything
// else is left exactly as found for manual investigation.
func newTriageHaltCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "halt <workspace-id> [reason]",
		Short:             "Halt the workspace over a confirmed concern (no automatic resume)",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stateDir, err := ensureGateWorkspace(args[0])
			if err != nil {
				return err
			}
			reason := ""
			if len(args) > 1 {
				reason = args[1]
			}
			return gate.Halt(cmd.Context(), stateDir, gate.Triage, reason)
		},
	}
}
