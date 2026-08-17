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
	cmd.AddCommand(newWorkspaceInfoCommand())
	cmd.AddCommand(newWorkspaceRebaseCommand())
	cmd.AddCommand(newWorkspaceRenameCommand())
	return cmd
}

// newWorkspace mints a fresh workspace ID for branch, persists its metadata,
// and creates the git worktree keyed by that ID (roadmap step 7) — the
// shared "start something new" sequence every entrypoint (workspace create,
// plan start, review start) that isn't resuming an existing workspace uses.
// name is an optional display label (see workspace.Create) and may be empty.
func newWorkspace(root, branch, base, name string) (workspace.Info, string, error) {
	id, err := workspace.NewID()
	if err != nil {
		return workspace.Info{}, "", err
	}
	info, err := workspace.Create(root, id, branch, base, name)
	if err != nil {
		return workspace.Info{}, "", err
	}
	dir, err := worktree.Create(root, id, branch, base)
	if err != nil {
		return workspace.Info{}, "", err
	}
	// Started only once both the metadata and the worktree exist, so a
	// worktree.Create failure above never leaves an orphan daemon process
	// behind (see the known non-atomicity issue this function's doc comment
	// -- adding a third failure mode here would make it worse, not better).
	if err := startDaemon(id); err != nil {
		return workspace.Info{}, "", err
	}
	return info, dir, nil
}

func newWorkspaceCreateCommand() *cobra.Command {
	var base, name string
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
			info, dir, err := newWorkspace(root, args[0], resolvedBase, name)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace=%s worktree=%s\n", info.ID, dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", defaultBase, "branch to create the worktree's branch from, if it doesn't exist yet")
	cmd.Flags().StringVar(&name, "name", "", "optional human-readable label for this workspace (display only, shown in `workspace list`/`info`)")
	return cmd
}

func newWorkspaceMergeCommand() *cobra.Command {
	var into string
	cmd := &cobra.Command{
		Use:               "merge <workspace-id>",
		Short:             "Locally merge a workspace's branch into --into (never pushes)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaceIDs,
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
		Use:               "remove <workspace-id>",
		Short:             "Remove a workspace's worktree, state directory, and (unless --keep-branch) its branch",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			info, err := workspace.Load(args[0])
			if err != nil {
				return err
			}
			// Stop the daemon before workspace.Remove deletes daemon.pid out
			// from under it -- otherwise there'd be no way left to find the
			// process to signal, and it would keep running indefinitely.
			// Best-effort: a workspace created before this feature existed
			// has no daemon.pid at all, and that must not block removal.
			if err := stopDaemon(info.ID); err != nil {
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

// newWorkspaceInfoCommand builds `masuda workspace info`, a lookup for the
// one thing `list`'s table doesn't have room for: the absolute host paths of
// a workspace's clone and state directory. `create` prints the clone path
// once at creation time and nowhere else, so there was previously no way to
// look it back up -- needed, for instance, to know where to manually resolve
// a conflict `masuda workspace rebase` stopped on.
func newWorkspaceInfoCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "info <workspace-id>",
		Short:             "Show a workspace's paths and status",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			info, err := workspace.Load(args[0])
			if err != nil {
				return err
			}
			stateDir, err := workspace.StateDir(info.ID)
			if err != nil {
				return err
			}
			running := hostloop.IsRunning(info.ID) || sandbox.IsRunning(info.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "id=%s\nname=%s\nbranch=%s\nbase=%s\nworktree=%s\nstate_dir=%s\nstatus=%s\nrunning=%t\n",
				info.ID, info.Name, info.Branch, info.Base, worktree.Dir(root, info.ID), stateDir, workspace.Status(info.ID), running)
			return nil
		},
	}
}

// newWorkspaceRenameCommand builds `masuda workspace rename`, the only way
// to set or change a workspace's display name after creation (`create`/`plan
// start`/`review start`'s --name only covers creation time). Name is purely
// a label (workspace.Rename) — it plays no part in resolving a workspace, so
// renaming has no effect beyond `workspace list`/`info` output.
func newWorkspaceRenameCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "rename <workspace-id> <name>",
		Short:             "Set or change a workspace's display name",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return workspace.Rename(args[0], args[1])
		},
	}
}

// newWorkspaceRebaseCommand builds `masuda workspace rebase`, a manual,
// explicitly-invoked counterpart to `review approve`'s automatic Pull: when
// repoRoot's branch has moved on since this workspace's clone was created
// (e.g. another workspace targeting the same branch already landed first)
// and `review approve` refuses the resulting non-fast-forward, this replays
// the clone's commits on top of repoRoot's current tip so approve can retry
// as a clean fast-forward. Never wired into approve itself (ADR-0023): a
// real divergence needs a human to judge whether the two histories are
// still compatible, not an automatic rebase.
func newWorkspaceRebaseCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "rebase <workspace-id>",
		Short:             "Rebase a workspace's clone onto repoRoot's current branch tip",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			info, err := workspace.Load(args[0])
			if err != nil {
				return err
			}
			return worktree.Rebase(root, info.ID, info.Branch)
		},
	}
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
