package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/selfupdate"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

// sandboxBackend is the single Backend implementation every subcommand uses
// to manage a workspace's sandbox (Issue #31): Docker's execution runtime
// (DockerBackend, and the package-level Start/Stop/IsRunning/AttachArgs
// functions it wrapped) was removed once VMBackend proved stable in real
// use -- see internal/sandbox/backend.go. `docker build`/`docker export`
// (internal/rootfs.Build) still runs, unrelated to this: that's building
// the *image* a VM's rootfs is derived from, not running a container.
var sandboxBackend sandbox.Backend = sandbox.VMBackend{}

func newSandboxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Start, stop, or (re)build the sandbox VM's source image for a workspace/project",
	}
	cmd.AddCommand(newSandboxStartCommand())
	cmd.AddCommand(newSandboxStopCommand())
	cmd.AddCommand(newSandboxBuildCommand())
	return cmd
}

func newSandboxStartCommand() *cobra.Command {
	var image string
	cmd := &cobra.Command{
		Use:               "start <workspace-id>",
		Short:             "Start a sandbox VM for an existing workspace",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			info, err := workspace.Load(args[0])
			if err != nil {
				return err
			}
			worktreeDir := worktree.Dir(root, info.ID)
			stateDir, err := workspace.StateDir(info.ID)
			if err != nil {
				return err
			}
			resolvedImage, err := resolveImage(cmd, root, image, config.DefaultImageEntry)
			if err != nil {
				return err
			}
			warnIfNoClaudeToken(cmd)
			h, err := sandboxBackend.Start(info.ID, worktreeDir, stateDir, root, resolvedImage)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "container=%s host_port=%d\n", h.ContainerName, h.HostPort)
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", config.DefaultImageEntry, "name of the .masuda/images/ entry to build the VM rootfs from")
	return cmd
}

func newSandboxStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "stop <workspace-id>",
		Short:             "Stop and remove a workspace's sandbox VM",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandboxBackend.Stop(args[0])
		},
	}
}

// newSandboxBuildCommand rebuilds the current project's declared image
// entries on demand, independent of `masuda update`: early in a project, sandbox
// tooling (language toolchains, LSP plugins, ...) tends to change often,
// and `masuda update`'s machine-wide running-workspace block (ADR-0032,
// there for the CLI binary replace) has no bearing on a per-project image
// rebuild, so it shouldn't gate this. Unlike `masuda update`'s silent skip
// when a project declares no image entries, this command errors -- the user
// explicitly asked to build them.
func newSandboxBuildCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Rebuild this project's .masuda/images/ entries against the latest published base image",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			entries, err := config.ListImageEntries(root)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				return fmt.Errorf("no image entries declared under %s — run `masuda init` first", config.ImagesDir(root))
			}
			release, err := selfupdate.FetchLatestRelease(selfupdate.DefaultAPIBase, selfupdate.DefaultRepo)
			if err != nil {
				return err
			}
			return rebuildImagesForRelease(cmd, root, entries, release)
		},
	}
}
