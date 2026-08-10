package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/hunkcontext"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

// newReviewStartCommand builds `masuda review start`, the standalone review
// entrypoint (design doc "レビュー単体での再利用", roadmap step 6). Unlike
// `masuda plan start` (which creates a *new* branch for a new task), this
// targets a branch that already has real commits on it: it skips phases 0-4
// entirely (no investigation, no plan, no G1, nothing to implement) and goes
// straight into phase 5's review against the sandbox already built for the
// full pipeline.
//
// Every invocation mints its own workspace ID (roadmap step 7), so unlike
// the original version of this command, it never needs to refuse to run
// against a branch that already has a full pipeline (or another review) in
// flight — each workspace gets its own worktree, state directory, and
// sandbox container, so nothing to collide over.
func newReviewStartCommand() *cobra.Command {
	var base, image, name string
	cmd := &cobra.Command{
		Use:   "start <branch-or-ref> [--base develop]",
		Short: "Review an existing branch standalone, skipping investigate/plan/implement",
		Long: `Review an existing branch standalone, skipping investigate/plan/implement.

Unlike masuda plan start, branch-or-ref must already exist — there is nothing
to review on a branch masuda would otherwise create fresh from base. Reuses
the same phase 4-5 sandbox and orchestrator as the full pipeline; plan/steps.json
never exists here, so the ADR-0010 mechanical backstop is skipped (there is
no plan to have deviated from) and the review diff is computed against
--base directly, not an implementation's uncommitted changes.

Prints a fresh workspace ID on success; use it with masuda review
show|chat|approve|reject.`,
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
			resolvedBase, err := resolveBase(cmd, root, "base", base, defaultBase)
			if err != nil {
				return err
			}
			info, worktreeDir, err := newWorkspace(root, branch, resolvedBase, name)
			if err != nil {
				return err
			}
			if err := seedReviewOnly(info.ID); err != nil {
				return err
			}
			stateDir, err := workspace.StateDir(info.ID)
			if err != nil {
				return err
			}
			resolvedImage, err := resolveImage(cmd, root, image, sandbox.DefaultImage)
			if err != nil {
				return err
			}
			h, err := sandbox.Start(info.ID, worktreeDir, stateDir, root, resolvedImage)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace=%s container=%s host_port=%d\n", info.ID, h.ContainerName, h.HostPort)
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", defaultBase, "ref to diff and review against")
	cmd.Flags().StringVar(&image, "image", sandbox.DefaultImage, "docker image to run")
	cmd.Flags().StringVar(&name, "name", "", "optional human-readable label for this workspace (display only, shown in `workspace list`/`info`)")
	return cmd
}

// newReviewHunkCommand builds `masuda review hunk`, an alternative to
// `masuda review show` (ADR-0019): instead of printing final_report.md as
// text, it converts the confirmed findings in review_results/ into Hunk's
// --agent-context sidecar format (internal/hunkcontext, ADR-0020's
// structured file/startLine/endLine schema) and opens the reviewed diff in
// Hunk with those findings annotated inline. Requires the hunk CLI
// (https://hunk.dev) on the host's PATH; masuda review show keeps working
// without it.
func newReviewHunkCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "hunk <workspace-id>",
		Short:             "Open the reviewed diff in Hunk with masuda's findings annotated",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			root, stateDir, err := gateStateDir(id)
			if err != nil {
				return err
			}
			contextPath, err := hunkcontext.Build(stateDir)
			if err != nil {
				return err
			}
			if err := os.Chdir(worktree.Dir(root, id)); err != nil {
				return fmt.Errorf("cd into worktree for %s: %w", id, err)
			}
			return attach([]string{"hunk", "diff", "--agent-context", contextPath})
		},
	}
}

// seedReviewOnly marks phase 4 as already "done" so
// orchestrator/implement_review_graph.py's detect_phase skips straight to
// phase 5 on its first invocation — there is no implementation to run or
// wait for here, the branch's commits already are the change under review.
// A no-op if already seeded (idempotent resume after e.g. a dead container).
func seedReviewOnly(id string) error {
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, "implementation_result.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	data, err := json.Marshal(map[string]any{"status": "done"})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
