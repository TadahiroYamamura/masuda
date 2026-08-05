package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/hostloop"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/selfupdate"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// newUpdateCommand implements `masuda update` (ADR-0032): replace the
// running CLI binary with the latest GitHub Release build. Deliberately does
// not use repoRoot() — unlike every other subcommand, this one updates
// masuda itself, not something scoped to the project the CLI happens to be
// invoked from.
func newUpdateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update masuda's own CLI binary to the latest release",
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
		},
	}
}
