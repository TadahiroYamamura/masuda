package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

func newSandboxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Start or stop the docker sandbox for a workspace",
	}
	cmd.AddCommand(newSandboxStartCommand())
	cmd.AddCommand(newSandboxStopCommand())
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
			h, err := sandbox.Start(info.ID, worktreeDir, stateDir, resolvedImage)
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
