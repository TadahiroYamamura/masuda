// Command masuda is the host-side CLI for the AI-collaborative development
// sandbox: worktree lifecycle management, sandbox container startup, and G1/G2
// gate operations. See docs/design/sandbox-workflow.md for the full picture —
// this CLI currently covers implementation-roadmap.md step 2 (the CLI skeleton)
// only; it is not yet wired into a 6-phase LangGraph pipeline (step 3).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/gate"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "masuda",
		Short:         "AI-collaborative development sandbox orchestration",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newWorktreeCommand())
	root.AddCommand(newSandboxCommand())

	planCmd := newGateCommand(gate.Plan)
	planCmd.AddCommand(newPlanStartCommand())
	root.AddCommand(planCmd)

	root.AddCommand(newGateCommand(gate.Review))
	return root
}

// repoRoot returns the top-level directory of the masuda checkout the CLI is
// being run from — the "main checkout" that worktrees are created against.
func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repository: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
