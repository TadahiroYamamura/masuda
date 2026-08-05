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
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/selfupdate"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// newUpdateCommand implements `masuda update` (ADR-0032): replace the
// running CLI binary with the latest GitHub Release build, then (if the
// current directory is inside a project with a .masuda/Dockerfile) rebuild
// that project's sandbox image against the freshly published base. The
// binary-replace step deliberately does not use repoRoot() — it updates
// masuda itself, not something scoped to the project the CLI happens to be
// invoked from — but the Dockerfile-refresh step does, since .masuda/Dockerfile
// belongs to that project, not to masuda's own source tree.
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
				return hostloop.IsRunning(id) || sandbox.IsRunning(id)
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
			if err := updateBinary(cmd, release); err != nil {
				return err
			}
			return refreshProjectDockerfile(cmd, release)
		},
	}
}

// updateBinary replaces the running masuda executable with the latest
// GitHub Release build, if it isn't already current.
func updateBinary(cmd *cobra.Command, release selfupdate.Release) error {
	if release.TagName == version {
		fmt.Fprintf(cmd.OutOrStdout(), "masuda is already up to date (%s)\n", version)
		return nil
	}

	assetName := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	asset, ok := selfupdate.FindAsset(release, assetName)
	if !ok {
		return fmt.Errorf("release %s has no asset for this platform (%s)", release.TagName, assetName)
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating running executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}

	if err := selfupdate.DownloadAndReplace(execPath, asset.BrowserDownloadURL); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "updated masuda %s -> %s\n", version, release.TagName)
	return nil
}

// refreshProjectDockerfile rebuilds the current project's .masuda/Dockerfile
// (ADR-0032), if one exists, tagging the result as .masuda/settings.json's
// "image" field (or sandbox.DefaultImage if that field is unset). A no-op
// outside a project checkout, or inside one without a .masuda/Dockerfile —
// masuda init only started writing that file with ADR-0032, so
// already-initialized projects don't have one yet. Bumps the Dockerfile's
// pinned FROM tag to release.TagName before rebuilding, so the pin doesn't
// go stale.
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

	if err := selfupdate.UpdateDockerfileFromTag(dockerfilePath, release.TagName); err != nil {
		return err
	}

	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	tag := cfg.Image
	if tag == "" {
		tag = sandbox.DefaultImage
	}

	if err := selfupdate.RebuildDockerfile(dockerfilePath, root, tag, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "rebuilt %s -> %s\n", dockerfilePath, tag)
	return nil
}
