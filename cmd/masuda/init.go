package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/perspectives"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/selfupdate"
	"github.com/TadahiroYamamura/masuda/internal/verify"
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
// fine. enableAllProjectMcpServers/enabledMcpjsonServers/
// disabledMcpjsonServers are seeded at their own inert values
// (false/empty/empty — matching Claude Code's own no-trust-by-default
// behavior, granting nothing) rather than left out entirely: the point
// isn't to pre-approve anything, it's to put the selective-trust escape
// hatch for Issue #10's MCP prompt (approve specific servers by name in
// enabledMcpjsonServers, block specific ones in disabledMcpjsonServers, or
// flip enableAllProjectMcpServers if a repo's servers are all trusted) in
// front of the user instead of requiring them to know Claude Code's
// settings schema to discover it. skipDangerousModePermissionPrompt
// suppresses the bypass-permissions-mode disclaimer dialog that
// `--dangerously-skip-permissions` would otherwise show on first run
// (ADR-0034).
const defaultClaudeSettings = `{"theme": "dark-ansi", "enableAllProjectMcpServers": false, "enabledMcpjsonServers": [], "disabledMcpjsonServers": [], "skipDangerousModePermissionPrompt": true}`

// dockerfileTemplate is .masuda/Dockerfile's starting content (ADR-0032):
// FROM the publicly published masuda base image, pinned to tag. Pinned (not
// "latest") so two `docker build`s of an unchanged Dockerfile give the same
// result; `masuda update` bumps the pin via
// internal/selfupdate.UpdateDockerfileFromTag. Same materialize-don't-embed
// pattern as .masuda/reviews/ -- the user is free to add language
// toolchains, LSP plugins, etc. on top, same as
// docker/{go,python,typescript,full}/Dockerfile already do against the old
// locally-built masuda-loop tag.
func dockerfileTemplate(tag string) string {
	return fmt.Sprintf(`# Sandbox image for this project. Customize freely -- add language
# toolchains, LSP plugins, etc. "masuda sandbox build" (or "masuda update")
# bumps the pinned tag below to the latest published release and rebuilds
# this file.
FROM %s:%s
`, selfupdate.DefaultDockerImage, tag)
}

// newInitCommand builds `masuda init` (ADR-0024): a one-shot setup step that
// populates a target repository's .masuda/ directory — .masuda/settings.json
// (the image/base fields .masuda.json used to hold, now read exclusively
// from here, plus a claudeSettings default), .masuda/reviews/ (masuda's 14
// built-in review perspectives, written out as individually
// editable/deletable files — see internal/perspectives), and .masuda/Dockerfile.
//
// ADR-0033: the review perspectives and the Dockerfile's pinned FROM tag
// both come from the same GitHub Release (internal/selfupdate), pinned to
// this CLI build's own version when known, or the latest release for local
// "dev" builds — so `masuda init` now requires network access, unlike
// before ADR-0033.
//
// Deliberately refuses to run again once .masuda/ already exists, rather
// than trying to reconcile it: ADR-0024 treats this as a one-time seed, not
// a sync — re-running must never resurrect a perspective file the user
// disabled. Picking up newly-introduced built-in perspectives into an
// already-initialized project is `masuda update`'s job (ADR-0033), not this
// command's.
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

			var release selfupdate.Release
			if version == "dev" {
				release, err = selfupdate.FetchLatestRelease(selfupdate.DefaultAPIBase, selfupdate.DefaultRepo)
			} else {
				release, err = selfupdate.FetchReleaseByTag(selfupdate.DefaultAPIBase, selfupdate.DefaultRepo, version)
			}
			if err != nil {
				return err
			}
			reviewsAsset, ok := selfupdate.FindAsset(release, selfupdate.ReviewsAssetName)
			if !ok {
				return fmt.Errorf("release %s has no reviews asset (%s)", release.TagName, selfupdate.ReviewsAssetName)
			}
			reviewsBundleAsset, ok := selfupdate.FindAsset(release, selfupdate.BundleAssetName(selfupdate.ReviewsAssetName))
			if !ok {
				return fmt.Errorf("release %s has no signature bundle for %s — refusing to materialize unverified review perspectives", release.TagName, selfupdate.ReviewsAssetName)
			}
			reviewsBundleBytes, err := selfupdate.DownloadBytes(reviewsBundleAsset.BrowserDownloadURL)
			if err != nil {
				return err
			}
			identity, err := verify.ExpectedIdentity(selfupdate.DefaultRepo)
			if err != nil {
				return err
			}
			trusted, err := verify.TrustedMaterial()
			if err != nil {
				return err
			}

			// Materialized explicitly even when --image wasn't passed, rather
			// than leaving Image empty for some later reader to implicitly
			// fall back to sandbox.DefaultImage at its own point of use —
			// masuda doesn't carry implicit defaults for values a project's
			// own committed settings.json can just state outright (ADR-0031's
			// principle, applied here to Image too).
			resolvedImage := image
			if resolvedImage == "" {
				resolvedImage = sandbox.DefaultImage
			}
			cfg := config.Config{Image: resolvedImage, Base: base, ClaudeSettings: json.RawMessage(defaultClaudeSettings)}
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

			if _, err := selfupdate.SyncReviews(reviewsAsset.BrowserDownloadURL, perspectives.ReviewsDir(root), verifiedBundleFunc(reviewsBundleBytes, identity, trusted)); err != nil {
				return err
			}

			if err := os.WriteFile(config.DockerfilePath(root), []byte(dockerfileTemplate(release.TagName)), 0o644); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "initialized %s (settings.json, reviews/, Dockerfile) from release %s\n", dir, release.TagName)
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "docker image to record in .masuda/settings.json (optional)")
	cmd.Flags().StringVar(&base, "base", "", "trunk branch to record in .masuda/settings.json (optional)")
	return cmd
}
