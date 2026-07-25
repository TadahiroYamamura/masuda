package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

const defaultBase = "develop"

func newWorktreeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Manage per-branch git worktrees (local-only: create, merge, remove)",
	}
	cmd.AddCommand(newWorktreeCreateCommand())
	cmd.AddCommand(newWorktreeMergeCommand())
	cmd.AddCommand(newWorktreeRemoveCommand())
	return cmd
}

func newWorktreeCreateCommand() *cobra.Command {
	var base string
	cmd := &cobra.Command{
		Use:   "create <branch>",
		Short: "Create a worktree for branch, creating the branch from --base if it doesn't exist yet",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			dir, err := worktree.Create(root, args[0], base)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", defaultBase, "branch to create the worktree's branch from, if it doesn't exist yet")
	return cmd
}

func newWorktreeMergeCommand() *cobra.Command {
	var into string
	cmd := &cobra.Command{
		Use:   "merge <branch>",
		Short: "Locally merge branch into --into (never pushes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return worktree.Merge(root, args[0], into)
		},
	}
	cmd.Flags().StringVar(&into, "into", defaultBase, "branch to merge into; must already be checked out in the main worktree")
	return cmd
}

func newWorktreeRemoveCommand() *cobra.Command {
	var keepBranch bool
	cmd := &cobra.Command{
		Use:   "remove <branch>",
		Short: "Remove branch's worktree (and, unless --keep-branch, the branch itself)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return worktree.Remove(root, args[0], !keepBranch)
		},
	}
	cmd.Flags().BoolVar(&keepBranch, "keep-branch", false, "remove only the worktree checkout, keep the branch ref")
	return cmd
}
