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
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// version is masuda's own version, embedded at build time via
// `-ldflags "-X main.version=..."` (.github/workflows/release.yml, ADR-0032).
// Local `make build`/`make install` builds don't set this — the CI-built
// release binaries are the only ones that carry a real version, since a
// bare `go build` has no way to know which git tag it corresponds to
// without also embedding the source tree's location (ADR-0032 deliberately
// rejected that).
var version = "dev"

func main() {
	if err := newRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "masuda",
		Short:         "AI-collaborative development sandbox orchestration",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newInitCommand())
	root.AddCommand(newInternalCommand())
	root.AddCommand(newWorkspaceCommand())
	root.AddCommand(newSandboxCommand())
	root.AddCommand(newChatCommand())
	root.AddCommand(newUpdateCommand())

	planCmd := newGateCommand(gate.Plan)
	planCmd.AddCommand(newPlanStartCommand())
	root.AddCommand(planCmd)

	reviewCmd := newGateCommand(gate.Review)
	reviewCmd.AddCommand(newReviewStartCommand())
	reviewCmd.AddCommand(newReviewHunkCommand())
	root.AddCommand(reviewCmd)

	root.AddCommand(newTriageCommand())
	root.AddCommand(newMCPCommand())
	root.AddCommand(newEgressCommand())
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
		return config.Config{}, fmt.Errorf("reading %s: %w", config.SettingsPath(root), err)
	}
	return cfg, nil
}

// resolveImage returns the Docker image a sandbox-starting command should
// use: the --image flag if the user passed it explicitly, otherwise
// .masuda/settings.json's declared image (internal/config), otherwise fall.
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

// completeWorkspaceIDs is a shared cobra.Command.ValidArgsFunction for every
// subcommand whose first positional argument is a <workspace-id> (chat,
// plan/review show|approve|reject, review hunk, sandbox start|stop,
// workspace merge|remove, plan start's resume form): it looks up every
// workspace known to the current repo (internal/workspace.List, the same
// source `masuda workspace list` prints) instead of leaving the user to
// copy-paste an ID from that command's output. Errors (not in a git repo,
// no workspaces yet) just fall back to no suggestions rather than surfacing
// a completion-time error to the shell.
func completeWorkspaceIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	root, err := repoRoot()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	infos, err := workspace.List(root)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ids := make([]string, 0, len(infos))
	for _, info := range infos {
		if strings.HasPrefix(info.ID, toComplete) {
			ids = append(ids, info.ID)
		}
	}
	return ids, cobra.ShellCompDirectiveNoFileComp
}

// resolveBase returns the branch a --base/--into flag should default to:
// the flag's value if the user passed flagName explicitly, otherwise
// .masuda/settings.json's declared base (internal/config), otherwise fall. flagName
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
