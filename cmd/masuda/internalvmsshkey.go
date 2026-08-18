package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/sandbox"
)

// newInternalVMSSHKeyCommand groups the VM guest SSH key management
// masuda's VM backend needs (Issue #31 M5-5) -- `masuda chat` has no
// `docker exec` equivalent for a VM, so it SSHes in instead, authenticating
// with a keypair masuda itself owns (internal/sandbox.EnsureSSHKeypair),
// not a per-workspace secret. Hidden: VM backend isn't part of the public
// CLI surface yet, matching `internal rootfs`/`internal mcp-relay`.
func newInternalVMSSHKeyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "vm-ssh-key",
		Hidden: true,
		Short:  "Manage the keypair masuda uses to SSH into VM guests (Issue #31)",
	}
	cmd.AddCommand(newInternalVMSSHKeyRotateCommand())
	return cmd
}

func newInternalVMSSHKeyRotateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate",
		Short: "Generate a fresh VM SSH keypair, replacing the current one",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sandbox.GenerateSSHKeypair(); err != nil {
				return err
			}
			privatePath, publicPath, err := sandbox.SSHKeyPaths()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rotated VM SSH keypair: %s / %s\n", privatePath, publicPath)
			fmt.Fprintln(cmd.OutOrStdout(), "note: already-built rootfs images and already-running VMs keep trusting the previous key until rebuilt/restarted")
			return nil
		},
	}
}
