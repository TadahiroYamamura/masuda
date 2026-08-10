package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/selfupdate"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

func newSandboxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Start, stop, or (re)build the docker sandbox image for a workspace/project",
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
		Short:             "Start a sandbox container for an existing workspace",
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
			resolvedImage, err := resolveImage(cmd, root, image, sandbox.DefaultImage)
			if err != nil {
				return err
			}
			h, err := sandbox.Start(info.ID, worktreeDir, stateDir, root, resolvedImage)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "container=%s host_port=%d\n", h.ContainerName, h.HostPort)
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", sandbox.DefaultImage, "docker image to run")
	return cmd
}

func newSandboxStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "stop <workspace-id>",
		Short:             "Stop and remove a workspace's sandbox container",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.Stop(args[0])
		},
	}
}

// newSandboxBuildCommand rebuilds the current project's .masuda/Dockerfile
// on demand, independent of `masuda update`: early in a project, sandbox
// tooling (language toolchains, LSP plugins, ...) tends to change often,
// and `masuda update`'s machine-wide running-workspace block (ADR-0032,
// there for the CLI binary replace) has no bearing on a per-project image
// rebuild, so it shouldn't gate this. Unlike `masuda update`'s silent skip
// when .masuda/Dockerfile is absent, this command errors -- the user
// explicitly asked to build one.
func newSandboxBuildCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Rebuild this project's .masuda/Dockerfile against the latest published base image",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			dockerfilePath := config.DockerfilePath(root)
			if _, err := os.Stat(dockerfilePath); err != nil {
				return fmt.Errorf("%s not found — run `masuda init` first", dockerfilePath)
			}
			release, err := selfupdate.FetchLatestRelease(selfupdate.DefaultAPIBase, selfupdate.DefaultRepo)
			if err != nil {
				return err
			}
			return rebuildDockerfileForRelease(cmd, root, dockerfilePath, release)
		},
	}
}
