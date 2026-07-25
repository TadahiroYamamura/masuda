package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

func newSandboxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Start or stop the docker sandbox for a branch's worktree",
	}
	cmd.AddCommand(newSandboxStartCommand())
	cmd.AddCommand(newSandboxStopCommand())
	return cmd
}

func newSandboxStartCommand() *cobra.Command {
	var image string
	cmd := &cobra.Command{
		Use:   "start <branch>",
		Short: "Start a sandbox container bind-mounting branch's worktree at /workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			branch := args[0]
			root, err := repoRoot()
			if err != nil {
				return err
			}
			worktreeDir := worktree.Dir(root, branch)
			if _, err := os.Stat(worktreeDir); err != nil {
				return fmt.Errorf("no worktree for %q — run `masuda worktree create %s` first", branch, branch)
			}
			claudeMd := root + "/runtime/CLAUDE.md"
			h, err := sandbox.Start(branch, worktreeDir, claudeMd, image)
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
		Use:   "stop <branch>",
		Short: "Stop and remove branch's sandbox container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.Stop(args[0])
		},
	}
}
