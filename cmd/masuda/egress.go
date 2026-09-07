package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

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
	var all bool
	cmd := &cobra.Command{
		Use:   "approve [hostname]",
		Short: "Approve a declared egress hostname so the sandbox VM may reach it",
		Long: `Approve a declared egress hostname so the sandbox VM may reach it.

Approval is separate from declaration on purpose: .masuda/settings.json says
what the project wants to reach, .masuda/settings.local.json says what this
user agreed to (ADR-0041's split, GitHub Issue #19). A hostname has to appear
in both before the egress proxy lets it through.

--all approves every hostname the project declares. masuda init seeds that
list with the hosts Claude Code itself needs, so a fresh checkout's first
step is normally:

    masuda egress approve --all

--all prints what it is about to approve and asks before doing it. It is a
shortcut for agreeing to the project's list, not for skipping the decision:
what is being agreed to is that an AI agent inside the sandbox may reach
those hosts and choose what to send them.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return fmt.Errorf("give exactly one of <hostname> or --all")
			}
			root, err := repoRoot()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return fmt.Errorf("reading %s: %w", config.SettingsPath(root), err)
			}
			hosts := args
			if all {
				if len(cfg.EgressAllowlist) == 0 {
					return fmt.Errorf("no egress hostnames declared in %s", config.SettingsPath(root))
				}
				hosts = cfg.EgressAllowlist
			}
			local, err := config.LoadLocal(root)
			if err != nil {
				return fmt.Errorf("reading %s: %w", config.SettingsLocalPath(root), err)
			}

			// Already-approved hosts are worth saying out loud when the user
			// named one -- they asked about that host specifically. Under
			// --all they are just noise: the user asked to agree to the
			// project's list, not for a report on the parts already settled.
			var pending []string
			for _, host := range hosts {
				if !slices.Contains(cfg.EgressAllowlist, host) {
					return fmt.Errorf("hostname %q is not declared in %s", host, config.SettingsPath(root))
				}
				if slices.Contains(local.EgressAllowlist, host) {
					if !all {
						fmt.Fprintf(cmd.OutOrStdout(), "%q is already approved\n", host)
					}
					continue
				}
				pending = append(pending, host)
			}
			if len(pending) == 0 {
				if all {
					fmt.Fprintf(cmd.OutOrStdout(), "all %d declared hostnames are already approved\n", len(hosts))
				}
				return nil
			}
			if all {
				ok, err := confirmEgressApproval(cmd, pending)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(cmd.OutOrStdout(), "nothing approved")
					return nil
				}
			}

			local.EgressAllowlist = append(local.EgressAllowlist, pending...)
			if err := config.SaveLocal(root, local); err != nil {
				return err
			}
			warnIfNotGitignored(cmd, root, config.SettingsLocalPath(root))
			for _, host := range pending {
				fmt.Fprintf(cmd.OutOrStdout(), "approved %q -- takes effect on the VM's next connection attempt, no restart needed\n", host)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "approve every hostname declared in .masuda/settings.json")
	return cmd
}

// confirmEgressApproval asks before approving a whole list at once.
//
// A flag's help text is not a control: --all is a convenience, and the thing
// it makes convenient is granting network reach to a sandbox whose occupant
// is an AI agent that decides for itself what to send. So the list is shown
// and the decision is asked for, rather than assumed from the flag.
//
// Reads from the command's own input, so anything non-interactive (a script,
// a pipe, a closed stdin) reaches EOF and is treated as "no" -- the answer
// that changes nothing. Approving individually still works there.
func confirmEgressApproval(cmd *cobra.Command, hosts []string) (bool, error) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "These hostnames will become reachable from the sandbox, where an AI agent")
	fmt.Fprintln(out, "decides for itself what to send them:")
	fmt.Fprintln(out)
	for _, host := range hosts {
		fmt.Fprintf(out, "  %s\n", host)
	}
	fmt.Fprintf(out, "\nApprove all %d? [y/N]: ", len(hosts))

	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
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
