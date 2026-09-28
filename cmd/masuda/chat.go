package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

func newChatCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "chat <workspace-id>",
		Short:             "Attach interactively to whichever session is running for a workspace",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !workspace.Exists(id) {
				return fmt.Errorf("no workspace %q", id)
			}
			if sandboxBackend.IsRunning(id) {
				attachArgs, err := sandboxBackend.AttachArgs(id)
				if err != nil {
					return err
				}
				return attach(attachArgs)
			}
			return fmt.Errorf("no session running for %q — start it with `masuda run <workflow-path> %s`", id, id)
		},
	}
}
