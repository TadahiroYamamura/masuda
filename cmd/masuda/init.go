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

// newInitCommand builds `masuda init` (ADR-0024): a one-shot setup step that
// populates a target repository's .masuda/ directory — .masuda/settings.json
// (the image/base fields .masuda.json used to hold, now read exclusively
// from here) and .masuda/reviews/ (masuda's 14 built-in review perspectives,
// written out as individually editable/deletable files — see
// internal/perspectives).
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

			cfg := config.Config{Image: image, Base: base}
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

			fmt.Fprintf(cmd.OutOrStdout(), "initialized %s (settings.json, reviews/)\n", dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "docker image to record in .masuda/settings.json (optional)")
	cmd.Flags().StringVar(&base, "base", "", "trunk branch to record in .masuda/settings.json (optional)")
	return cmd
}
