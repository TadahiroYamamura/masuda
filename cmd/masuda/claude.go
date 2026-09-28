package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
)

// newClaudeCommand groups what a human has to set up so the Claude Code
// session inside a sandbox VM can authenticate at all -- currently the
// `claude setup-token` OAuth tokens the host-side API gateway puts in place
// of the guest's placeholder (ADR-0084, internal/sandbox/claudetoken.go).
//
// Top-level and visible, not under `internal`: this is a step in
// docs/INSTALLATION.md that the user runs by hand, and `internal` means
// "masuda's own plumbing, called by masuda".
func newClaudeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claude",
		Short: "Manage the Claude Code credentials masuda hands sandbox VMs",
	}
	cmd.AddCommand(newClaudeSetTokenCommand())
	cmd.AddCommand(newClaudeListTokensCommand())
	cmd.AddCommand(newClaudeUseCommand())
	return cmd
}

func newClaudeSetTokenCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "set-token",
		Short: "Save a `claude setup-token` OAuth token, read from stdin",
		Long: "Saves the long-lived OAuth token `claude setup-token` prints. It stays on the host:\n" +
			"sandbox VM guests hold only a placeholder, which a host-side gateway replaces with\n" +
			"this token on their API requests. Read from stdin so the token never\n" +
			"lands in shell history or `ps` output:\n\n" +
			"  claude setup-token\n" +
			"  echo \"<the token it printed>\" | masuda claude set-token [--name work]\n\n" +
			"Several tokens can be kept under different names (one per account); each repository\n" +
			"picks one with `masuda claude use`, and uses \"default\" otherwise. Without a token a VM\n" +
			"still boots, but every API call the `claude` inside it makes is refused.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("reading token from stdin: %w", err)
			}
			if err := sandbox.SetClaudeToken(name, string(data)); err != nil {
				return err
			}
			path, err := sandbox.ClaudeTokenPath(name)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "saved Claude OAuth token %q to %s\n", name, path)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", sandbox.DefaultClaudeToken, "name to save the token under")
	return cmd
}

func newClaudeListTokensCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list-tokens",
		Short: "List the registered Claude tokens by name (never their values), marking this repository's",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			names, err := sandbox.ListClaudeTokens()
			if err != nil {
				return err
			}
			if len(names) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no Claude tokens registered -- see `masuda claude set-token --help`")
				return nil
			}
			// Outside a repository there is simply nothing to mark.
			selected := ""
			if root, err := repoRoot(); err == nil {
				if local, err := config.LoadLocal(root); err == nil {
					selected = local.ClaudeToken
					if selected == "" {
						selected = sandbox.DefaultClaudeToken
					}
				}
			}
			for _, n := range names {
				mark := "  "
				if n == selected {
					mark = "* "
				}
				fmt.Fprintln(cmd.OutOrStdout(), mark+n)
			}
			return nil
		},
	}
}

func newClaudeUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Choose which registered Claude token this repository's sandbox VMs use",
		Long: "Records the choice in .masuda/settings.local.json (not committed), so it applies to\n" +
			"this repository on this machine only. Takes effect the next time a VM starts; running\n" +
			"VMs keep the token they started with.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := sandbox.ValidateClaudeTokenName(name); err != nil {
				return err
			}
			if !sandbox.HasClaudeToken(name) {
				return fmt.Errorf("no Claude token %q is registered -- run `masuda claude set-token --name %s` first", name, name)
			}
			root, err := repoRoot()
			if err != nil {
				return err
			}
			local, err := config.LoadLocal(root)
			if err != nil {
				return err
			}
			// The default is stored as no choice at all, so the file stays
			// free of a field that only restates the default.
			if name == sandbox.DefaultClaudeToken {
				local.ClaudeToken = ""
			} else {
				local.ClaudeToken = name
			}
			if err := config.SaveLocal(root, local); err != nil {
				return err
			}
			warnIfNotGitignored(cmd, root, config.SettingsLocalPath(root))
			fmt.Fprintf(cmd.OutOrStdout(), "this repository now uses the Claude token %q -- restart its running VMs (masuda sandbox stop/start) to switch them\n", name)
			return nil
		},
	}
}

// warnIfNoClaudeToken prints a warning before starting a VM whose repository
// resolves to the default token when none is registered. A warning rather
// than an error: a tokenless VM still boots and is still worth having (SSH
// in, inspect the share, run a privileged command), so refusing to start
// would be wrong. But the way it fails otherwise is silent and misleading --
// the loop's API calls are refused, the tmux session ends, and
// masuda-loop.service reports the loop as complete, which is
// indistinguishable from a loop that ran and finished. A repository that
// names a token that is not registered is not warned about here: VM start
// refuses it outright (sandbox.ResolveRepoClaudeToken).
func warnIfNoClaudeToken(cmd *cobra.Command, root string) {
	name, _, err := sandbox.ResolveRepoClaudeToken(root)
	if err != nil || sandbox.HasClaudeToken(name) {
		return
	}
	fmt.Fprintln(cmd.ErrOrStderr(),
		"警告: Claude OAuthトークンが未登録です。VMは起動しますが、中のclaudeのAPI呼び出しはすべて拒否され、\n"+
			"      ループが走らないまま「完了」したように見えます。登録するには:\n"+
			"        claude setup-token\n"+
			"        echo \"<出力されたトークン>\" | masuda claude set-token")
}
