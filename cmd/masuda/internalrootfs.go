package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/rootfs"
)

// newInternalRootfsCommand groups rootfs-image plumbing for the VM backend
// (Cloud Hypervisor). Kept under `internal` rather than exposed as a public
// `masuda rootfs` command, matching newInternalCommand's own doc comment:
// VMBackend calls internal/rootfs.Build directly on every start, so this
// exists to build an image by hand while debugging, not as a step any
// normal workflow runs. It takes a Docker image reference rather than a
// .masuda/images/ entry name (ADR-0054) for the same reason -- debugging an
// export often means pointing it at an image that has no entry at all.
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
	var sizeMiB int
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Export a Docker image's filesystem into a bootable ext4 disk image",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if output == "" {
				return fmt.Errorf("--output is required")
			}
			if image == "" {
				return fmt.Errorf("--image is required")
			}
			if err := rootfs.Build(image, output, nil, sizeMiB); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", output)
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "docker image to export (required)")
	cmd.Flags().StringVar(&output, "output", "", "path to write the ext4 image to (required)")
	cmd.Flags().IntVar(&sizeMiB, "size-mib", 0, "minimum ext4 image size in MiB (0 = size it from the exported content)")
	return cmd
}
