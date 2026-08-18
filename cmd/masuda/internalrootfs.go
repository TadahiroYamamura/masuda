package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/rootfs"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
)

// newInternalRootfsCommand groups rootfs-image plumbing for the Issue #31
// VM backend (Cloud Hypervisor). Kept under `internal` rather than exposed
// as a public `masuda rootfs` command, matching newInternalCommand's own
// doc comment: masuda has no VM sandbox backend to consume this image yet
// (M2 only builds the image; M3 launches a VM with it, M5 wires a
// VMBackend into internal/sandbox), so there's nothing for a user-facing
// command to plug into.
func newInternalRootfsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "rootfs",
		Hidden: true,
		Short:  "Build a VM disk image from a masuda sandbox Docker image (Issue #31)",
	}
	cmd.AddCommand(newInternalRootfsBuildCommand())
	return cmd
}

func newInternalRootfsBuildCommand() *cobra.Command {
	var image, output string
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Export a Docker image's filesystem into a bootable ext4 disk image",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if output == "" {
				return fmt.Errorf("--output is required")
			}
			if err := rootfs.Build(image, output); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", output)
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", sandbox.DefaultImage, "docker image to export")
	cmd.Flags().StringVar(&output, "output", "", "path to write the ext4 image to (required)")
	return cmd
}
