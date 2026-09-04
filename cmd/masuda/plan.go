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
	var name string
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

--file passes a pre-written instructions/investigation document (only valid
alongside the first form), and is a complete input on its own — the <task>
argument can be omitted when it is given:

  masuda plan start <branch> --file notes.md
  masuda plan start <branch> --file notes.md "<narrow it down>"

The investigator fact-checks the document against the actual codebase before
producing INVESTIGATION.md, instead of following it blindly (ADR-0016) —
which matters most when the document is the whole assignment: a past
investigation report or a saved GitHub issue is exactly the kind of input
that can have gone stale.`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeWorkspaceIDs,
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
				if name != "" {
					return fmt.Errorf("workspace %q already exists — --name is only accepted when starting a new workspace from a branch name (use `masuda workspace rename` to relabel it)", args[0])
				}
				info, err := workspace.Load(args[0])
				if err != nil {
					return err
				}
				stateDir, err := workspace.StateDir(info.ID)
				if err != nil {
					return err
				}
				// The daemon started at workspace creation may no longer be
				// running by the time a resume happens (host reboot, manual
				// kill, a crash) -- startDaemon is idempotent, so this is
				// safe to call even when it's still alive.
				if err := startDaemon(info.ID); err != nil {
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
			task, err = taskBriefFor(task, instructionsFile, branch)
			if err != nil {
				return err
			}
			resolvedBase, err := resolveBase(cmd, root, "base", base, defaultBase)
			if err != nil {
				return err
			}
			info, worktreeDir, err := newWorkspace(root, branch, resolvedBase, name)
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
	cmd.Flags().StringVar(&name, "name", "", "optional human-readable label for this workspace (display only; only valid when starting a new workspace)")
	return cmd
}

// taskBriefFor decides what to record as the workspace's task brief
// (internal:task-brief, read back by investigate_plan_graph.py's
// _investigate_task).
//
// A document is a complete input on its own: what gets worked on is often
// something that already exists -- a past investigation report, a GitHub
// issue saved to a file -- and restating it as a one-line task adds nothing.
// The investigate prompt already carries a section telling the investigator
// to read INSTRUCTIONS.md and fact-check it against the codebase (ADR-0016),
// so all the brief has to do in that case is point at it; the section that
// follows carries the real content. Defaulting here rather than reshaping
// that prompt keeps the "task brief is always present" invariant
// _read_task_brief() relies on.
//
// A task given alongside --file is kept as-is: it then reads as a narrowing
// of the document ("just the X part of this"), not as the whole assignment.
func taskBriefFor(task, instructionsFile, branch string) (string, error) {
	if task != "" {
		return task, nil
	}
	if instructionsFile != "" {
		// No CLI vocabulary here: the reader is a subagent that never sees
		// the command line, so a flag name is a string it cannot resolve.
		// What it can act on is the next section, which names the file by
		// absolute path.
		return "このワークスペースには事前に用意された指示書がある。次節「事前に用意された指示書の検証」が" +
			"示すファイルを読み、そこに書かれている作業を今回のタスクとせよ。", nil
	}
	return "", fmt.Errorf(
		"no task description given — required when starting a new workspace: masuda plan start %s \"<task>\" "+
			"(or pass a document with --file instead)", branch)
}
