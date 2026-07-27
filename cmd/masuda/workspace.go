package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/hostloop"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

const defaultBase = "develop"

// newWorkspaceCommand builds `masuda workspace`, the CLI's home for
// workspace lifecycle management. Named after the broader workspace concept
// (internal/workspace: an ID plus its worktree, state directory, and
// metadata) rather than "worktree" — "worktree" is git terminology for just
// the checkout half of that, and having a git-flavored command group manage
// a strictly larger, non-git-specific concept (merge/remove/list all key on
// workspace ID, not a git worktree path) read as a naming mismatch once the
// two were pulled apart. internal/worktree remains an implementation detail
// this package calls into, not something exposed as its own CLI surface.
func newWorkspaceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage workspaces (local-only: create, merge, remove, list)",
	}
	cmd.AddCommand(newWorkspaceCreateCommand())
	cmd.AddCommand(newWorkspaceMergeCommand())
	cmd.AddCommand(newWorkspaceRemoveCommand())
	cmd.AddCommand(newWorkspaceListCommand())
	return cmd
}

// newWorkspace mints a fresh workspace ID for branch, persists its metadata,
// and creates the git worktree keyed by that ID (roadmap step 7) — the
// shared "start something new" sequence every entrypoint (workspace create,
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

func newWorkspaceCreateCommand() *cobra.Command {
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
			resolvedBase, err := resolveBase(cmd, root, "base", base, defaultBase)
			if err != nil {
				return err
			}
			info, dir, err := newWorkspace(root, args[0], resolvedBase)
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

func newWorkspaceMergeCommand() *cobra.Command {
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
			resolvedInto, err := resolveBase(cmd, root, "into", into, defaultBase)
			if err != nil {
				return err
			}
			return worktree.Merge(root, info.ID, info.Branch, resolvedInto)
		},
	}
	cmd.Flags().StringVar(&into, "into", defaultBase, "branch to merge into; must already be checked out in the main worktree")
	return cmd
}

func newWorkspaceRemoveCommand() *cobra.Command {
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

func newWorkspaceListCommand() *cobra.Command {
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
			entries := make([]workspace.EntryStatus, len(infos))
			for i, info := range infos {
				entries[i] = workspace.EntryStatus{
					Info:       info,
					TaskStatus: workspace.Status(info.ID),
					Running:    hostloop.IsRunning(info.ID) || sandbox.IsRunning(info.ID),
				}
			}
			fmt.Fprint(cmd.OutOrStdout(), workspace.FormatEntries(entries))
			return nil
		},
	}
}
