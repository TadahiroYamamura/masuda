package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/gate"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

// newGateCommand builds the `masuda plan ...` / `masuda review ...` command
// group for gate n. Both gates share the same show/approve/reject shape
// (ADR-0006); only the artifact they show and what approval triggers differ.
// Attaching to chat with the session (`masuda chat`) doesn't need a gate name
// at all — see newChatCommand — so it isn't part of this group.
func newGateCommand(n gate.Name) *cobra.Command {
	cmd := &cobra.Command{
		Use:   string(n),
		Short: fmt.Sprintf("Operate on the %s gate for a workspace", n),
	}
	cmd.AddCommand(newGateShowCommand(n))
	cmd.AddCommand(newGateApproveCommand(n))
	cmd.AddCommand(newGateRejectCommand(n))
	return cmd
}

// gateStateDir resolves a workspace ID to its state directory (where gate
// markers and the artifacts they judge live, per roadmap step 7 — never the
// worktree itself). The workspace ID is the only input: state directories are
// global (see internal/workspace), so this deliberately never consults the
// cwd, and gate commands work from anywhere — including outside a git
// repository (Issue #25).
func gateStateDir(id string) (string, error) {
	if !workspace.Exists(id) {
		return "", fmt.Errorf("no workspace %q", id)
	}
	return workspace.StateDir(id)
}

func newGateShowCommand(n gate.Name) *cobra.Command {
	return &cobra.Command{
		Use:               "show <workspace-id>",
		Short:             fmt.Sprintf("Print the artifact the %s gate is judging", n),
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stateDir, err := gateStateDir(args[0])
			if err != nil {
				return err
			}
			content, err := gate.Show(cmd.Context(), stateDir, n)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), content)
			return nil
		},
	}
}

func newGateApproveCommand(n gate.Name) *cobra.Command {
	return &cobra.Command{
		Use:               "approve <workspace-id> [feedback]",
		Short:             fmt.Sprintf("Approve the %s gate", n),
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stateDir, err := gateStateDir(args[0])
			if err != nil {
				return err
			}
			feedback := ""
			if len(args) > 1 {
				feedback = args[1]
			}
			if err := gate.Approve(cmd.Context(), stateDir, n, feedback); err != nil {
				return err
			}
			if n == gate.Review {
				return finalizeReviewApproval(args[0])
			}
			return nil
		},
	}
}

func newGateRejectCommand(n gate.Name) *cobra.Command {
	return &cobra.Command{
		Use:               "reject <workspace-id> <feedback>",
		Short:             fmt.Sprintf("Reject the %s gate with feedback for the next pass", n),
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stateDir, err := gateStateDir(args[0])
			if err != nil {
				return err
			}
			return gate.Reject(cmd.Context(), stateDir, n, args[1])
		},
	}
}

// finalizeReviewApproval implements ADR-0005 and ADR-0023: approving G2
// pulls the workspace's branch back into repoRoot (fast-forward only — see
// worktree.Pull) and tears down its worktree/sandbox/state directory, all
// without ever pushing or merging into a separate integration branch.
//
// The repository it acts on is the one `create` recorded for this workspace,
// never the one the CLI happens to be invoked from: everything below commits,
// fast-forwards or deletes, and running that against whichever repository the
// cwd points at is exactly the failure Issue #25 hit in practice.
func finalizeReviewApproval(id string) error {
	info, err := workspace.Load(id)
	if err != nil {
		return err
	}
	root := info.RepoRoot
	// A recorded root that no longer exists (the repository was moved or
	// deleted after the workspace was created) would otherwise surface as a
	// bare `git` failure from deep inside worktree.Commit, which reads as a
	// masuda bug rather than as stale metadata the user has to fix.
	if _, err := os.Stat(root); err != nil {
		return fmt.Errorf("workspace %s records repository %s, which is no longer readable: %w", id, root, err)
	}
	if sandboxBackend.IsRunning(id) {
		if err := sandboxBackend.Stop(id); err != nil {
			return fmt.Errorf("stopping sandbox after approval: %w", err)
		}
	}
	if err := worktree.Commit(root, id, commitMessage(id)); err != nil {
		return fmt.Errorf("committing %s's work before approval: %w", info.Branch, err)
	}
	if err := worktree.Pull(root, id, info.Branch); err != nil {
		return fmt.Errorf("pulling %s after approval: %w", info.Branch, err)
	}
	// deleteBranch=false: unlike the old Merge-into-develop model, branch
	// itself is now the landed deliverable (ADR-0023) — the user still
	// needs it to push and open a PR, so only the clone/state directory
	// are torn down here.
	if err := worktree.Remove(root, id, info.Branch, false); err != nil {
		return err
	}
	return workspace.Remove(id)
}

// commitMessage reads the message orchestrator/*.py's synthesize phase wrote
// (workspace.CommitMessageFileName), falling back to a generic message for
// workspaces created before that file existed — data preservation (commit
// something) matters more here than message quality.
func commitMessage(id string) string {
	stateDir, err := workspace.StateDir(id)
	if err == nil {
		if data, err := os.ReadFile(filepath.Join(stateDir, workspace.CommitMessageFileName)); err == nil {
			if msg := strings.TrimSpace(string(data)); msg != "" {
				return msg
			}
		}
	}
	return fmt.Sprintf("masuda: workspace %sの変更を反映する", id)
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
