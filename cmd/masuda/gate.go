package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/gate"
	"github.com/TadahiroYamamura/masuda/internal/hostloop"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

// newGateCommand builds the `masuda plan ...` / `masuda review ...` command
// group for gate n. Both gates share the same show/chat/approve/reject shape
// (ADR-0006); only the artifact they show and what approval triggers differ.
func newGateCommand(n gate.Name) *cobra.Command {
	cmd := &cobra.Command{
		Use:   string(n),
		Short: fmt.Sprintf("Operate on the %s gate for a workspace", n),
	}
	cmd.AddCommand(newGateShowCommand(n))
	cmd.AddCommand(newGateChatCommand(n))
	cmd.AddCommand(newGateApproveCommand(n))
	cmd.AddCommand(newGateRejectCommand(n))
	return cmd
}

// gateStateDir resolves a workspace ID to its state directory (where gate
// markers and the artifacts they judge live, per roadmap step 7 — never the
// worktree itself).
func gateStateDir(id string) (root, stateDir string, err error) {
	root, err = repoRoot()
	if err != nil {
		return "", "", err
	}
	if !workspace.Exists(id) {
		return "", "", fmt.Errorf("no workspace %q", id)
	}
	stateDir, err = workspace.StateDir(id)
	if err != nil {
		return "", "", err
	}
	return root, stateDir, nil
}

func newGateShowCommand(n gate.Name) *cobra.Command {
	return &cobra.Command{
		Use:   "show <workspace-id>",
		Short: fmt.Sprintf("Print the artifact the %s gate is judging", n),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stateDir, err := gateStateDir(args[0])
			if err != nil {
				return err
			}
			content, err := gate.Show(stateDir, n)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), content)
			return nil
		},
	}
}

func newGateChatCommand(n gate.Name) *cobra.Command {
	return &cobra.Command{
		Use:   "chat <workspace-id>",
		Short: fmt.Sprintf("Attach interactively to discuss the %s gate before deciding (ADR-0006)", n),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !workspace.Exists(id) {
				return fmt.Errorf("no workspace %q", id)
			}
			if n == gate.Plan {
				// G1 can be waiting in either place: the phase 1-2 host loop
				// (first time through) or the phase 4-5 sandbox (reopened by
				// a plan deviation, ADR-0010) — try both.
				if hostloop.IsRunning(id) {
					return attach(hostloop.AttachArgs(id))
				}
				if sandbox.IsRunning(id) {
					return attach(sandbox.AttachArgs(id))
				}
				return fmt.Errorf("no plan session running for %q — run `masuda plan start %s` or `masuda sandbox start %s` first", id, id, id)
			}
			if !sandbox.IsRunning(id) {
				return fmt.Errorf("sandbox for %q is not running — run `masuda sandbox start %s` first", id, id)
			}
			return attach(sandbox.AttachArgs(id))
		},
	}
}

func newGateApproveCommand(n gate.Name) *cobra.Command {
	return &cobra.Command{
		Use:   "approve <workspace-id> [feedback]",
		Short: fmt.Sprintf("Approve the %s gate", n),
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, stateDir, err := gateStateDir(args[0])
			if err != nil {
				return err
			}
			feedback := ""
			if len(args) > 1 {
				feedback = args[1]
			}
			if err := gate.Approve(stateDir, n, feedback); err != nil {
				return err
			}
			if n == gate.Review {
				return finalizeReviewApproval(root, args[0])
			}
			return nil
		},
	}
}

func newGateRejectCommand(n gate.Name) *cobra.Command {
	return &cobra.Command{
		Use:   "reject <workspace-id> <feedback>",
		Short: fmt.Sprintf("Reject the %s gate with feedback for the next pass", n),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, stateDir, err := gateStateDir(args[0])
			if err != nil {
				return err
			}
			return gate.Reject(stateDir, n, args[1])
		},
	}
}

// finalizeReviewApproval implements ADR-0005: approving G2 merges the
// workspace's branch locally and tears down its worktree/sandbox/state
// directory, all without ever pushing.
func finalizeReviewApproval(root, id string) error {
	info, err := workspace.Load(id)
	if err != nil {
		return err
	}
	if sandbox.IsRunning(id) {
		if err := sandbox.Stop(id); err != nil {
			return fmt.Errorf("stopping sandbox after approval: %w", err)
		}
	}
	if err := worktree.Merge(root, id, info.Branch, defaultBase); err != nil {
		return fmt.Errorf("merging %s after approval: %w", info.Branch, err)
	}
	if err := worktree.Remove(root, id, info.Branch, true); err != nil {
		return err
	}
	return workspace.Remove(id)
}

// attach replaces the current process with an interactive docker exec, so the
// user's terminal (stdin/stdout/stderr, raw mode) is wired straight to tmux
// attach inside the container.
func attach(argv []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}
