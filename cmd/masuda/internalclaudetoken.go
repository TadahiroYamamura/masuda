package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/sandbox"
)

// newInternalClaudeTokenCommand groups management of the `claude
// setup-token` OAuth token masuda's VM backend hands guests in place of the
// Docker path's ~/.claude credential file bind mounts (Issue #31 M5-6) --
// see internal/sandbox/claudetoken.go for why a VM needs a different
// mechanism. Hidden: VM backend isn't part of the public CLI surface yet,
// matching `internal vm-ssh-key`.
func newInternalClaudeTokenCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "claude-token",
		Hidden: true,
		Short:  "Manage the OAuth token masuda hands VM guests for Claude Code authentication (Issue #31)",
	}
	cmd.AddCommand(newInternalClaudeTokenSetCommand())
	return cmd
}

func newInternalClaudeTokenSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set",
		Short: "Save a `claude setup-token` OAuth token, read from stdin",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("reading token from stdin: %w", err)
			}
			if err := sandbox.SetClaudeOAuthToken(string(data)); err != nil {
				return err
			}
			path, err := sandbox.ClaudeOAuthTokenPath()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "saved Claude OAuth token to %s\n", path)
			return nil
		},
	}
}
