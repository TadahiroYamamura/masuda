package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/sandbox"
)

// newClaudeCommand groups what a human has to set up so the Claude Code
// session inside a sandbox VM can authenticate at all -- currently just the
// `claude setup-token` OAuth token masuda hands guests in place of the
// Docker path's ~/.claude credential file bind mounts (see
// internal/sandbox/claudetoken.go for why a VM needs a different
// mechanism).
//
// Top-level and visible, not under `internal`: this is a step in
// docs/INSTALLATION.md that the user runs by hand, and `internal` means
// "masuda's own plumbing, called by masuda". It used to be
// `masuda internal claude-token set`, hidden on the grounds that the VM
// backend was not part of the public CLI surface yet -- which stopped being
// true when ADR-0044 made VMBackend the only backend, leaving a documented
// human step that `--help` denied the existence of.
func newClaudeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claude",
		Short: "Manage the Claude Code credentials masuda hands sandbox VMs",
	}
	cmd.AddCommand(newClaudeSetTokenCommand())
	return cmd
}

func newClaudeSetTokenCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set-token",
		Short: "Save a `claude setup-token` OAuth token, read from stdin",
		Long: "Saves the long-lived OAuth token `claude setup-token` prints, for masuda to hand\n" +
			"sandbox VM guests as CLAUDE_CODE_OAUTH_TOKEN. Read from stdin so the token never\n" +
			"lands in shell history or `ps` output:\n\n" +
			"  claude setup-token\n" +
			"  echo \"<the token it printed>\" | masuda claude set-token\n\n" +
			"One token per masuda installation, not per workspace. Without it a VM still boots,\n" +
			"but the `claude` inside it has no credentials and exits immediately.",
		Args: cobra.NoArgs,
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

// warnIfNoClaudeToken prints a warning before starting a VM with no token
// registered. A warning rather than an error: a tokenless VM still boots and
// is still worth having (SSH in, inspect the share, run a privileged
// command), so refusing to start would be wrong. But the way it fails
// otherwise is silent and misleading -- `claude` exits for lack of
// credentials, the tmux session ends, and masuda-loop.service reports the
// loop as complete, which is indistinguishable from a loop that ran and
// finished.
func warnIfNoClaudeToken(cmd *cobra.Command) {
	if sandbox.HasClaudeOAuthToken() {
		return
	}
	fmt.Fprintln(cmd.ErrOrStderr(),
		"警告: Claude OAuthトークンが未登録です。VMは起動しますが、中のclaudeは認証できずに即終了し、\n"+
			"      ループが走らないまま「完了」したように見えます。登録するには:\n"+
			"        claude setup-token\n"+
			"        echo \"<出力されたトークン>\" | masuda claude set-token")
}
