package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/hostloop"
	"github.com/TadahiroYamamura/masuda/internal/perspectives"
	"github.com/TadahiroYamamura/masuda/internal/selfupdate"
	"github.com/TadahiroYamamura/masuda/internal/verify"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// verifiedBundleFunc returns a DownloadAndReplace/SyncReviews "verify"
// closure that checks downloaded content against bundleBytes (a detached
// Sigstore signature bundle release asset, ADR-0038) under identity and
// trusted.
func verifiedBundleFunc(bundleBytes []byte, identity verify.Identity, trusted verify.Trust) func([]byte) error {
	return func(data []byte) error {
		return verify.VerifyBlob(data, bundleBytes, identity, trusted)
	}
}

// newUpdateCommand implements `masuda update` (ADR-0032/ADR-0033): replace
// the running CLI binary with the latest GitHub Release build, then (if the
// current directory is inside an initialized project) rebuild its
// .masuda/Dockerfile against the freshly published base and add any newly
// introduced built-in review perspectives to .masuda/reviews/. The
// binary-replace step deliberately does not use repoRoot() — it updates
// masuda itself, not something scoped to the project the CLI happens to be
// invoked from — but the Dockerfile-refresh and reviews-sync steps do,
// since .masuda/Dockerfile and .masuda/reviews/ belong to that project, not
// to masuda's own source tree.
func newUpdateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update masuda's own CLI binary (and this project's sandbox image, if any) to the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			infos, err := workspace.ListAll()
			if err != nil {
				return fmt.Errorf("listing workspaces: %w", err)
			}
			blocking := selfupdate.BlockingWorkspaces(infos, func(id string) bool {
				return hostloop.IsRunning(id) || sandboxBackend.IsRunning(id)
			})
			if len(blocking) > 0 {
				ids := make([]string, len(blocking))
				for i, info := range blocking {
					ids[i] = info.ID
				}
				return fmt.Errorf("refusing to update: workspace(s) still running (%s) — stop or remove them first", strings.Join(ids, ", "))
			}

			release, err := selfupdate.FetchLatestRelease(selfupdate.DefaultAPIBase, selfupdate.DefaultRepo)
			if err != nil {
				return err
			}

			// Built once and threaded through every verification below
			// (ADR-0038): identity is a pure computation, but trusted is a
			// live fetch against Sigstore's TUF CDN, worth doing only once
			// per invocation.
			identity, err := verify.ExpectedIdentity(selfupdate.DefaultRepo)
			if err != nil {
				return err
			}
			trusted, err := verify.TrustedMaterial()
			if err != nil {
				return err
			}

			if err := updateBinary(cmd, release, identity, trusted); err != nil {
				return err
			}
			if err := refreshProjectDockerfile(cmd, release); err != nil {
				return err
			}
			return syncProjectReviews(cmd, release, identity, trusted)
		},
	}
}

// updateBinary replaces the running masuda executable with the latest
// GitHub Release build, if it isn't already current. Refuses to install a
// binary whose release has no matching signature bundle, or whose
// signature fails verification (ADR-0038) — there is no unverified
// fallback path.
func updateBinary(cmd *cobra.Command, release selfupdate.Release, identity verify.Identity, trusted verify.Trust) error {
	if release.TagName == version {
		fmt.Fprintf(cmd.OutOrStdout(), "masuda is already up to date (%s)\n", version)
		return nil
	}

	assetName := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	asset, ok := selfupdate.FindAsset(release, assetName)
	if !ok {
		return fmt.Errorf("release %s has no asset for this platform (%s)", release.TagName, assetName)
	}
	bundleAsset, ok := selfupdate.FindAsset(release, selfupdate.BundleAssetName(assetName))
	if !ok {
		return fmt.Errorf("release %s has no signature bundle for %s — refusing to install an unverified binary", release.TagName, assetName)
	}
	bundleBytes, err := selfupdate.DownloadBytes(bundleAsset.BrowserDownloadURL)
	if err != nil {
		return err
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating running executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}

	if err := selfupdate.DownloadAndReplace(execPath, asset.BrowserDownloadURL, verifiedBundleFunc(bundleBytes, identity, trusted)); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "updated masuda %s -> %s\n", version, release.TagName)
	return nil
}

// refreshProjectDockerfile rebuilds the current project's .masuda/Dockerfile
// (ADR-0032), if one exists. A no-op outside a project checkout, or inside
// one without a .masuda/Dockerfile — masuda init only started writing that
// file with ADR-0032, so already-initialized projects don't have one yet.
// `masuda sandbox build` (cmd/masuda/sandbox.go) is the standalone
// equivalent for rebuilding on demand, independent of the rest of `masuda
// update` (notably its machine-wide running-workspace block, which exists
// for the CLI binary replace step and has no bearing on a per-project image
// rebuild).
func refreshProjectDockerfile(cmd *cobra.Command, release selfupdate.Release) error {
	root, err := repoRoot()
	if err != nil {
		return nil
	}
	dockerfilePath := config.DockerfilePath(root)
	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return rebuildDockerfileForRelease(cmd, root, dockerfilePath, release)
}

// rebuildDockerfileForRelease bumps dockerfilePath's pinned FROM tag to
// release.TagName (so the pin doesn't go stale) and rebuilds it, tagging
// the result as .masuda/settings.json's "image" field. Shared by `masuda
// update` and `masuda sandbox build`.
func rebuildDockerfileForRelease(cmd *cobra.Command, root, dockerfilePath string, release selfupdate.Release) error {
	if err := selfupdate.UpdateDockerfileFromTag(dockerfilePath, release.TagName); err != nil {
		return err
	}

	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	// No implicit fallback here (ADR-0031's principle): masuda init
	// materializes Image explicitly, so an empty value here means the
	// project's own settings.json was edited to remove it, not "masuda
	// forgot to write one" -- surface that plainly instead of silently
	// substituting sandbox.DefaultImage.
	if cfg.Image == "" {
		return fmt.Errorf("%s has no image field set — set one explicitly (masuda init writes a default)", config.SettingsPath(root))
	}

	if err := selfupdate.RebuildDockerfile(dockerfilePath, root, cfg.Image, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "rebuilt %s -> %s\n", dockerfilePath, cfg.Image)
	return nil
}

// syncProjectReviews adds any built-in review perspectives (ADR-0033) not
// already present in the current project's .masuda/reviews/, without
// touching existing files — a perspective a user customized or disabled
// (frontmatter enable: false) stays exactly as they left it. A no-op
// outside a project checkout, or inside one that was never `masuda init`'d.
// A missing reviews asset on the release is reported but doesn't fail the
// whole command — the binary/Dockerfile updates above may have already
// succeeded. Once the reviews asset does exist, though, its signature
// bundle is mandatory: a missing bundle or a failed verification hard-fails
// (ADR-0038) — unlike the "asset entirely absent" case, there is no
// unverified-content fallback.
func syncProjectReviews(cmd *cobra.Command, release selfupdate.Release, identity verify.Identity, trusted verify.Trust) error {
	root, err := repoRoot()
	if err != nil {
		return nil
	}
	reviewsDir := perspectives.ReviewsDir(root)
	if _, err := os.Stat(reviewsDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}

	asset, ok := selfupdate.FindAsset(release, selfupdate.ReviewsAssetName)
	if !ok {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: release %s has no reviews asset (%s), skipping perspective sync\n", release.TagName, selfupdate.ReviewsAssetName)
		return nil
	}
	bundleAsset, ok := selfupdate.FindAsset(release, selfupdate.BundleAssetName(selfupdate.ReviewsAssetName))
	if !ok {
		return fmt.Errorf("release %s has no signature bundle for %s — refusing to sync unverified review perspectives", release.TagName, selfupdate.ReviewsAssetName)
	}
	bundleBytes, err := selfupdate.DownloadBytes(bundleAsset.BrowserDownloadURL)
	if err != nil {
		return err
	}

	added, err := selfupdate.SyncReviews(asset.BrowserDownloadURL, reviewsDir, verifiedBundleFunc(bundleBytes, identity, trusted))
	if err != nil {
		return err
	}
	if len(added) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "added %d new review perspective(s): %s\n", len(added), strings.Join(added, ", "))
	}
	return nil
}
