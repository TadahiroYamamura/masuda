// Command masuda is the host-side CLI for the AI-collaborative development
// sandbox: workspace lifecycle management, sandbox container startup, and
// G1/G2 gate operations, driving the 6-phase investigate/plan/implement/review
// pipeline (orchestrator/investigate_plan_graph.py, implement_review_graph.py).
// See docs/design/sandbox-workflow.md and CLAUDE.md's 現状 section for the
// full picture.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/config"
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
	root.AddCommand(newWorkspaceCommand())
	root.AddCommand(newSandboxCommand())
	root.AddCommand(newChatCommand())

	planCmd := newGateCommand(gate.Plan)
	planCmd.AddCommand(newPlanStartCommand())
	root.AddCommand(planCmd)

	reviewCmd := newGateCommand(gate.Review)
	reviewCmd.AddCommand(newReviewStartCommand())
	root.AddCommand(reviewCmd)
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

func loadConfig(root string) (config.Config, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return config.Config{}, fmt.Errorf("reading %s: %w", config.FileName, err)
	}
	return cfg, nil
}

// resolveImage returns the Docker image a sandbox-starting command should
// use: the --image flag if the user passed it explicitly, otherwise
// .masuda.json's declared image (internal/config), otherwise fall.
// Letting the target repository commit its own default here means masuda
// doesn't need any language-detection logic of its own to pick an image
// with the right LSP tooling baked in — that choice is the repo's, same as
// this project already defers to a repo's own .claude/settings.json for
// permissions.
func resolveImage(cmd *cobra.Command, root, flagImage, fall string) (string, error) {
	if cmd.Flags().Changed("image") {
		return flagImage, nil
	}
	cfg, err := loadConfig(root)
	if err != nil {
		return "", err
	}
	if cfg.Image != "" {
		return cfg.Image, nil
	}
	return fall, nil
}

// resolveBase returns the branch a --base/--into flag should default to:
// the flag's value if the user passed flagName explicitly, otherwise
// .masuda.json's declared base (internal/config), otherwise fall. flagName
// is "base" or "into" — the two flags share one config field since they're
// almost always the same branch (what work starts from is what it merges
// back into).
func resolveBase(cmd *cobra.Command, root, flagName, flagValue, fall string) (string, error) {
	if cmd.Flags().Changed(flagName) {
		return flagValue, nil
	}
	cfg, err := loadConfig(root)
	if err != nil {
		return "", err
	}
	if cfg.Base != "" {
		return cfg.Base, nil
	}
	return fall, nil
}
