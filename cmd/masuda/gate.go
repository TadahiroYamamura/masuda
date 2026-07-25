package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/gate"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

// newGateCommand builds the `masuda plan ...` / `masuda review ...` command
// group for gate n. Both gates share the same show/chat/approve/reject shape
// (ADR-0006); only the artifact they show and what approval triggers differ.
func newGateCommand(n gate.Name) *cobra.Command {
	cmd := &cobra.Command{
		Use:   string(n),
		Short: fmt.Sprintf("Operate on the %s gate for a branch's worktree", n),
	}
	cmd.AddCommand(newGateShowCommand(n))
	cmd.AddCommand(newGateChatCommand(n))
	cmd.AddCommand(newGateApproveCommand(n))
	cmd.AddCommand(newGateRejectCommand(n))
	return cmd
}

func gateWorktreeDir(branch string) (root, dir string, err error) {
	root, err = repoRoot()
	if err != nil {
		return "", "", err
	}
	dir = worktree.Dir(root, branch)
	if _, err := os.Stat(dir); err != nil {
		return "", "", fmt.Errorf("no worktree for %q", branch)
	}
	return root, dir, nil
}

func newGateShowCommand(n gate.Name) *cobra.Command {
	return &cobra.Command{
		Use:   "show <branch>",
		Short: fmt.Sprintf("Print the artifact the %s gate is judging", n),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, dir, err := gateWorktreeDir(args[0])
			if err != nil {
				return err
			}
			content, err := gate.Show(dir, n)
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
		Use:   "chat <branch>",
		Short: fmt.Sprintf("Attach interactively to the sandbox's tmux session to discuss the %s gate before deciding", n),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			branch := args[0]
			if n == gate.Plan {
				return fmt.Errorf("masuda plan chat isn't implemented yet — it needs the GATE:<name> keep-alive mechanism (roadmap step 5); the phase 1-2 host loop ends its session on reaching G1. Use `masuda plan show %s` and `masuda plan approve|reject %s` instead", branch, branch)
			}
			if !sandbox.IsRunning(branch) {
				return fmt.Errorf("sandbox for %q is not running — run `masuda sandbox start %s` first", branch, branch)
			}
			return attach(sandbox.AttachArgs(branch))
		},
	}
}

func newGateApproveCommand(n gate.Name) *cobra.Command {
	return &cobra.Command{
		Use:   "approve <branch> [feedback]",
		Short: fmt.Sprintf("Approve the %s gate", n),
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, dir, err := gateWorktreeDir(args[0])
			if err != nil {
				return err
			}
			feedback := ""
			if len(args) > 1 {
				feedback = args[1]
			}
			if err := gate.Approve(dir, n, feedback); err != nil {
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
		Use:   "reject <branch> <feedback>",
		Short: fmt.Sprintf("Reject the %s gate with feedback for the next pass", n),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, dir, err := gateWorktreeDir(args[0])
			if err != nil {
				return err
			}
			return gate.Reject(dir, n, args[1])
		},
	}
}

// finalizeReviewApproval implements ADR-0005: approving G2 merges the worktree's
// branch locally and tears down the worktree/sandbox, all without ever pushing.
func finalizeReviewApproval(root, branch string) error {
	if sandbox.IsRunning(branch) {
		if err := sandbox.Stop(branch); err != nil {
			return fmt.Errorf("stopping sandbox after approval: %w", err)
		}
	}
	if err := worktree.Merge(root, branch, defaultBase); err != nil {
		return fmt.Errorf("merging %s after approval: %w", branch, err)
	}
	return worktree.Remove(root, branch, true)
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
