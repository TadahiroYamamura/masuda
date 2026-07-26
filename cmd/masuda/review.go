package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/hostloop"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

// newReviewStartCommand builds `masuda review start`, the standalone review
// entrypoint (design doc "レビュー単体での再利用", roadmap step 6). Unlike
// `masuda plan start` (which creates a *new* branch for a new task), this
// targets a branch that already has real commits on it: it skips phases 0-4
// entirely (no investigation, no plan, no G1, nothing to implement) and goes
// straight into phase 5's review against the sandbox already built for the
// full pipeline.
func newReviewStartCommand() *cobra.Command {
	var base, image string
	cmd := &cobra.Command{
		Use:   "start <branch-or-ref> [--base develop]",
		Short: "Review an existing branch standalone, skipping investigate/plan/implement",
		Long: `Review an existing branch standalone, skipping investigate/plan/implement.

Unlike masuda plan start, branch-or-ref must already exist — there is nothing
to review on a branch masuda would otherwise create fresh from base. Reuses
the same phase 4-5 sandbox and orchestrator as the full pipeline; PLAN.md
never exists here, so the ADR-0010 mechanical backstop is skipped (there is
no plan to have deviated from) and the review diff is computed against
--base directly, not an implementation's uncommitted changes.

Isolation is per-branch, not per-command: the worktree path and sandbox
container name are both derived from the branch name alone. Running this
against a branch that already has a full pipeline in flight (masuda plan/
sandbox start) would fight over the same worktree and container, and this
command would overwrite that pipeline's implementation_result.json out from
under it — so it refuses to run against a branch with a PLAN.md (a sign a
full-pipeline worktree already owns it) or a live host/sandbox session.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			branch := args[0]
			root, err := repoRoot()
			if err != nil {
				return err
			}
			if !worktree.BranchExists(root, branch) {
				return fmt.Errorf("branch %q does not exist — masuda review start only reviews an existing branch (use `masuda plan start` to create a new one)", branch)
			}
			if hostloop.IsRunning(branch) || sandbox.IsRunning(branch) {
				return fmt.Errorf("a session for %q is already running — masuda review start shares its worktree/container by branch name with the full pipeline and would clobber it; stop the existing session first if you're sure they don't overlap", branch)
			}
			worktreeDir := worktree.Dir(root, branch)
			if _, err := os.Stat(filepath.Join(worktreeDir, "PLAN.md")); err == nil {
				return fmt.Errorf("worktree for %q already has a PLAN.md — it looks like a full-pipeline worktree, not one masuda review start should reuse; remove it first (`masuda worktree remove %s`) if you really want a standalone review here", branch, branch)
			}
			worktreeDir, err = worktree.Create(root, branch, base)
			if err != nil {
				return err
			}
			if err := seedReviewOnly(worktreeDir); err != nil {
				return err
			}
			claudeMd := root + "/runtime/CLAUDE.md"
			h, err := sandbox.Start(branch, worktreeDir, claudeMd, image)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "container=%s host_port=%d\n", h.ContainerName, h.HostPort)
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", defaultBase, "ref to diff and review against")
	cmd.Flags().StringVar(&image, "image", sandbox.DefaultImage, "docker image to run")
	return cmd
}

// seedReviewOnly marks phase 4 as already "done" so
// orchestrator/implement_review_graph.py's detect_phase skips straight to
// phase 5 on its first invocation — there is no implementation to run or
// wait for here, the branch's commits already are the change under review.
// A no-op if already seeded (idempotent resume after e.g. a dead container).
func seedReviewOnly(worktreeDir string) error {
	path := filepath.Join(worktreeDir, "implementation_result.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	data, err := json.Marshal(map[string]any{"status": "done"})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
