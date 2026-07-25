package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/hostloop"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

// newPlanStartCommand builds `masuda plan start`, which covers phases 0-2
// (worktree create -> investigate -> plan) up to the G1 gate. Unlike
// `masuda sandbox start` (phase 3+), this runs no Docker container — see
// ADR-0012 and internal/hostloop's package doc for why.
func newPlanStartCommand() *cobra.Command {
	var base string
	cmd := &cobra.Command{
		Use:   "start <branch> [task]",
		Short: "Create a worktree and start the phase 1-2 (investigate -> plan -> G1) host loop",
		Long: `Create a worktree and start the phase 1-2 (investigate -> plan -> G1) host loop.

task describes the work to investigate and plan; required the first time a
branch is started. Omit it to resume the loop after a G1 approve/reject —
masuda plan start is safe to re-run once the session has ended.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			branch := args[0]
			task := ""
			if len(args) > 1 {
				task = args[1]
			}
			root, err := repoRoot()
			if err != nil {
				return err
			}
			worktreeDir, err := worktree.Create(root, branch, base)
			if err != nil {
				return err
			}
			if err := hostloop.Start(root, branch, worktreeDir, task); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "session=%s worktree=%s\n", hostloop.SessionName(branch), worktreeDir)
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", defaultBase, "branch to create the worktree's branch from, if it doesn't exist yet")
	return cmd
}
