package main

import (
	"fmt"
	"slices"
	"sort"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

// newEgressCommand groups commands for managing this repository's declared
// TLS egress allowlist (Issue #11 M4): config.Config.EgressAllowlist
// (repoRoot's committed .masuda/settings.json) only *declares* hostnames
// the sandbox VM wants to reach -- these commands manage this user's
// *approval* of them, recorded in the gitignored .masuda/settings.local.json
// (config.LocalSettings). Same shape as newMCPCommand (cmd/masuda/mcp.go)
// for the same reason (Issue #19), operating on the repository directly
// rather than a workspace.
func newEgressCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "egress",
		Short: "Manage this repository's declared TLS egress allowlist (sandbox VM network access)",
	}
	cmd.AddCommand(newEgressListCommand())
	cmd.AddCommand(newEgressApproveCommand())
	cmd.AddCommand(newEgressRejectCommand())
	return cmd
}

func newEgressListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List hostnames declared in .masuda/settings.json and this user's approval status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return fmt.Errorf("reading %s: %w", config.SettingsPath(root), err)
			}
			local, err := config.LoadLocal(root)
			if err != nil {
				return fmt.Errorf("reading %s: %w", config.SettingsLocalPath(root), err)
			}
			if len(cfg.EgressAllowlist) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no egress hostnames declared in %s\n", config.SettingsPath(root))
				return nil
			}
			names := append([]string(nil), cfg.EgressAllowlist...)
			sort.Strings(names)
			for _, h := range names {
				status := "not approved"
				if slices.Contains(local.EgressAllowlist, h) {
					status = "approved"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", h, status)
			}
			return nil
		},
	}
}

func newEgressApproveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "approve <hostname>",
		Short: "Approve a declared egress hostname so the sandbox VM may reach it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := args[0]
			root, err := repoRoot()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return fmt.Errorf("reading %s: %w", config.SettingsPath(root), err)
			}
			if !slices.Contains(cfg.EgressAllowlist, host) {
				return fmt.Errorf("hostname %q is not declared in %s", host, config.SettingsPath(root))
			}
			local, err := config.LoadLocal(root)
			if err != nil {
				return fmt.Errorf("reading %s: %w", config.SettingsLocalPath(root), err)
			}
			if slices.Contains(local.EgressAllowlist, host) {
				fmt.Fprintf(cmd.OutOrStdout(), "%q is already approved\n", host)
				return nil
			}
			local.EgressAllowlist = append(local.EgressAllowlist, host)
			if err := config.SaveLocal(root, local); err != nil {
				return err
			}
			warnIfNotGitignored(cmd, root, config.SettingsLocalPath(root))
			fmt.Fprintf(cmd.OutOrStdout(), "approved %q -- restart this workspace's VM to pick it up\n", host)
			return nil
		},
	}
}

func newEgressRejectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reject <hostname>",
		Short: "Revoke approval for a declared egress hostname",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := args[0]
			root, err := repoRoot()
			if err != nil {
				return err
			}
			local, err := config.LoadLocal(root)
			if err != nil {
				return fmt.Errorf("reading %s: %w", config.SettingsLocalPath(root), err)
			}
			idx := slices.Index(local.EgressAllowlist, host)
			if idx == -1 {
				fmt.Fprintf(cmd.OutOrStdout(), "%q was not approved\n", host)
				return nil
			}
			local.EgressAllowlist = slices.Delete(local.EgressAllowlist, idx, idx+1)
			return config.SaveLocal(root, local)
		},
	}
}
