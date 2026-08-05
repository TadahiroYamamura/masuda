package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/perspectives"
)

// defaultClaudeSettings is the starting value `masuda init` writes into
// .masuda/settings.json's claudeSettings field. masuda itself carries no
// implicit Claude Code defaults at runtime (see internal/config.Config's
// ClaudeSettings doc) — this is the one place such a default exists, and
// only as literal, user-editable/deletable content, the same materialize-
// don't-embed pattern .masuda/reviews/ already uses for built-in review
// perspectives.
//
// theme sidesteps the interactive first-run theme-selection wizard and
// carries no security implication, so masuda suggesting it by default is
// fine. enableAllProjectMcpServers/enabledMcpjsonServers are seeded at
// their own inert values (false/empty — matching Claude Code's own
// no-trust-by-default behavior, granting nothing) rather than left out
// entirely: the point isn't to pre-approve anything, it's to put the
// selective-trust escape hatch for Issue #10's MCP prompt (approve
// specific servers by name in enabledMcpjsonServers, or flip
// enableAllProjectMcpServers if a repo's servers are all trusted) in front
// of the user instead of requiring them to know Claude Code's settings
// schema to discover it.
const defaultClaudeSettings = `{"theme": "dark-ansi", "enableAllProjectMcpServers": false, "enabledMcpjsonServers": []}`

// dockerfileTemplate is .masuda/Dockerfile's starting content (ADR-0032):
// FROM the publicly published masuda base image, so `masuda update` can
// refresh it with `docker build --pull` without the user needing to know
// where that image lives. Same materialize-don't-embed pattern as
// .masuda/reviews/ -- the user is free to add language toolchains, LSP
// plugins, etc. on top, same as docker/{go,python,typescript,full}/Dockerfile
// already do against the old locally-built masuda-loop tag.
const dockerfileTemplate = `# Sandbox image for this project (ADR-0032). Customize freely -- add
# language toolchains, LSP plugins, etc. "masuda update" rebuilds this file
# with "docker build --pull" and tags the result as .masuda/settings.json's
# "image" field (or masuda-loop if that field is unset), so FROM below
# always picks up the latest published base on update.
FROM tadahiroyamamura/masuda:latest
`

// newInitCommand builds `masuda init` (ADR-0024): a one-shot setup step that
// populates a target repository's .masuda/ directory — .masuda/settings.json
// (the image/base fields .masuda.json used to hold, now read exclusively
// from here, plus a claudeSettings default) and .masuda/reviews/ (masuda's
// 14 built-in review perspectives, written out as individually
// editable/deletable files — see internal/perspectives).
//
// Deliberately refuses to run again once .masuda/ already exists, rather
// than trying to reconcile it: ADR-0024 treats this as a one-time seed, not
// a sync — re-running must never resurrect a perspective file the user
// deleted. Picking up newly-introduced built-in perspectives into an
// already-initialized project is left to GitHub Issue #8, not this command.
func newInitCommand() *cobra.Command {
	var image, base string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize .masuda/ in the current repository (settings + built-in review perspectives)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			dir := filepath.Join(root, config.DirName)
			if _, err := os.Stat(dir); err == nil {
				return fmt.Errorf("%s already exists — masuda init only runs once per repository", dir)
			} else if !os.IsNotExist(err) {
				return err
			}

			cfg := config.Config{Image: image, Base: base, ClaudeSettings: json.RawMessage(defaultClaudeSettings)}
			data, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return err
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(config.SettingsPath(root), data, 0o644); err != nil {
				return err
			}

			if err := perspectives.WriteBuiltins(perspectives.ReviewsDir(root)); err != nil {
				return err
			}

			if err := os.WriteFile(config.DockerfilePath(root), []byte(dockerfileTemplate), 0o644); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "initialized %s (settings.json, reviews/, Dockerfile)\n", dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "docker image to record in .masuda/settings.json (optional)")
	cmd.Flags().StringVar(&base, "base", "", "trunk branch to record in .masuda/settings.json (optional)")
	return cmd
}
