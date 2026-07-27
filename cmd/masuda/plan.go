package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/hostloop"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

// newPlanStartCommand builds `masuda plan start`, which covers phases 0-2
// (worktree create -> investigate -> plan) up to the G1 gate. Unlike
// `masuda sandbox start` (phase 3+), this runs no Docker container — see
// ADR-0012 and internal/hostloop's package doc for why.
func newPlanStartCommand() *cobra.Command {
	var base string
	var instructionsFile string
	cmd := &cobra.Command{
		Use:   "start <branch-or-workspace-id> [task]",
		Short: "Start a new workspace, or resume an existing one's phase 1-2 (investigate -> plan -> G1) host loop",
		Long: `Start a new workspace, or resume an existing one's phase 1-2 host loop.

Two independent workspaces can target the same branch (roadmap step 7), so
resuming an existing session must name the workspace, not the branch:

  masuda plan start <branch> "<task>"   # starts a brand new workspace
  masuda plan start <workspace-id>      # resumes that workspace's loop

The first form always mints a fresh workspace ID and worktree, even if one
already exists for the same branch — that's what makes running two
independent attempts against the same branch possible. The second form is
recognized by <workspace-id> already existing on disk (masuda workspace
list); task must be omitted there since the loop resumes from whatever
on-disk state it left off at.

--file lets you attach a pre-written instructions/investigation document
(only valid alongside the first form). The investigator fact-checks it
against the actual codebase before producing INVESTIGATION.md, instead of
following it blindly (ADR-0016) — useful when you've already done some
investigation yourself and want it verified before a plan is drafted from it.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}

			if workspace.Exists(args[0]) {
				if len(args) > 1 {
					return fmt.Errorf("workspace %q already exists — task is only accepted when starting a new workspace from a branch name", args[0])
				}
				if instructionsFile != "" {
					return fmt.Errorf("workspace %q already exists — --file is only accepted when starting a new workspace from a branch name", args[0])
				}
				info, err := workspace.Load(args[0])
				if err != nil {
					return err
				}
				stateDir, err := workspace.StateDir(info.ID)
				if err != nil {
					return err
				}
				worktreeDir := worktree.Dir(root, info.ID)
				if err := hostloop.Start(info.ID, worktreeDir, stateDir, ""); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "workspace=%s session=%s worktree=%s\n", info.ID, hostloop.SessionName(info.ID), worktreeDir)
				return nil
			}

			branch := args[0]
			task := ""
			if len(args) > 1 {
				task = args[1]
			}
			if task == "" {
				return fmt.Errorf("no task description given — required when starting a new workspace: masuda plan start %s \"<task>\"", branch)
			}
			resolvedBase, err := resolveBase(cmd, root, "base", base, defaultBase)
			if err != nil {
				return err
			}
			info, worktreeDir, err := newWorkspace(root, branch, resolvedBase)
			if err != nil {
				return err
			}
			stateDir, err := workspace.StateDir(info.ID)
			if err != nil {
				return err
			}
			if instructionsFile != "" {
				content, err := os.ReadFile(instructionsFile)
				if err != nil {
					return fmt.Errorf("reading --file %q: %w", instructionsFile, err)
				}
				if err := hostloop.WriteInstructions(stateDir, content); err != nil {
					return fmt.Errorf("writing instructions into workspace: %w", err)
				}
			}
			if err := hostloop.Start(info.ID, worktreeDir, stateDir, task); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "workspace=%s session=%s worktree=%s\n", info.ID, hostloop.SessionName(info.ID), worktreeDir)
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", defaultBase, "branch to create the worktree's branch from, if it doesn't exist yet")
	cmd.Flags().StringVar(&instructionsFile, "file", "", "path to a pre-written instructions/investigation document; the investigator will fact-check it against the codebase before producing INVESTIGATION.md (only valid when starting a new workspace)")
	return cmd
}
