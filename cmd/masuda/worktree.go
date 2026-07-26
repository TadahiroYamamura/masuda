package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

const defaultBase = "develop"

func newWorktreeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Manage per-workspace git worktrees (local-only: create, merge, remove, list)",
	}
	cmd.AddCommand(newWorktreeCreateCommand())
	cmd.AddCommand(newWorktreeMergeCommand())
	cmd.AddCommand(newWorktreeRemoveCommand())
	cmd.AddCommand(newWorktreeListCommand())
	return cmd
}

// newWorkspace mints a fresh workspace ID for branch, persists its metadata,
// and creates the git worktree keyed by that ID (roadmap step 7) — the
// shared "start something new" sequence every entrypoint (worktree create,
// plan start, review start) that isn't resuming an existing workspace uses.
func newWorkspace(root, branch, base string) (workspace.Info, string, error) {
	id, err := workspace.NewID(branch)
	if err != nil {
		return workspace.Info{}, "", err
	}
	info, err := workspace.Create(root, id, branch, base)
	if err != nil {
		return workspace.Info{}, "", err
	}
	dir, err := worktree.Create(root, id, branch, base)
	if err != nil {
		return workspace.Info{}, "", err
	}
	return info, dir, nil
}

func newWorktreeCreateCommand() *cobra.Command {
	var base string
	cmd := &cobra.Command{
		Use:   "create <branch>",
		Short: "Create a new workspace for branch, creating the branch from --base if it doesn't exist yet",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			info, dir, err := newWorkspace(root, args[0], base)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace=%s worktree=%s\n", info.ID, dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", defaultBase, "branch to create the worktree's branch from, if it doesn't exist yet")
	return cmd
}

func newWorktreeMergeCommand() *cobra.Command {
	var into string
	cmd := &cobra.Command{
		Use:   "merge <workspace-id>",
		Short: "Locally merge a workspace's branch into --into (never pushes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			info, err := workspace.Load(args[0])
			if err != nil {
				return err
			}
			return worktree.Merge(root, info.ID, info.Branch, into)
		},
	}
	cmd.Flags().StringVar(&into, "into", defaultBase, "branch to merge into; must already be checked out in the main worktree")
	return cmd
}

func newWorktreeRemoveCommand() *cobra.Command {
	var keepBranch bool
	cmd := &cobra.Command{
		Use:   "remove <workspace-id>",
		Short: "Remove a workspace's worktree, state directory, and (unless --keep-branch) its branch",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			info, err := workspace.Load(args[0])
			if err != nil {
				return err
			}
			if err := worktree.Remove(root, info.ID, info.Branch, !keepBranch); err != nil {
				return err
			}
			return workspace.Remove(info.ID)
		},
	}
	cmd.Flags().BoolVar(&keepBranch, "keep-branch", false, "remove only the worktree checkout, keep the branch ref")
	return cmd
}

func newWorktreeListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List workspaces for the current repository",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			infos, err := workspace.List(root)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), workspace.FormatList(infos))
			return nil
		},
	}
}
