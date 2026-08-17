package main

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

// newMCPCommand groups commands for managing this repository's declared
// child MCP servers (internal/statedaemon/mcpaggregator, Issue #35):
// config.Config.MCPServers (repoRoot's committed .masuda/settings.json)
// only *declares* servers -- these commands manage this user's *approval*
// of them, recorded in the gitignored .masuda/settings.local.json
// (config.LocalSettings). Unlike the plan/review/triage gate commands
// (cmd/masuda/gate.go), these operate on the repository directly, not a
// workspace -- there is one settings.local.json per repository, read
// straight from repoRoot by every workspace's daemon, not per-workspace
// state.
func newMCPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage this repository's declared child MCP servers (aggregated into the curated set)",
	}
	cmd.AddCommand(newMCPListCommand())
	cmd.AddCommand(newMCPApproveCommand())
	cmd.AddCommand(newMCPRejectCommand())
	return cmd
}

func newMCPListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List MCP servers declared in .masuda/settings.json and this user's approval status",
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
			if len(cfg.MCPServers) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no MCP servers declared in %s\n", config.SettingsPath(root))
				return nil
			}
			names := make([]string, 0, len(cfg.MCPServers))
			for name := range cfg.MCPServers {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				decl := cfg.MCPServers[name]
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", name, mcpStatus(decl, local.MCPServers[name]))
			}
			return nil
		},
	}
}

// mcpStatus summarizes decl/approval the same way
// internal/statedaemon/mcpaggregator.resolveApproved decides whether to
// actually start a server, so `masuda mcp list`'s output always matches
// what the daemon will do on its next start.
func mcpStatus(decl config.MCPServerDecl, approval config.MCPServerApproval) string {
	if !approval.Approved {
		return "not approved"
	}
	hash, err := config.DeclHash(decl)
	if err != nil || approval.DeclHash != hash {
		return "approved, but declaration changed since -- re-approve"
	}
	var missing []string
	for _, e := range decl.Env {
		if approval.Env[e] == "" {
			missing = append(missing, e)
		}
	}
	if len(missing) > 0 {
		return "approved, but missing env: " + strings.Join(missing, ", ")
	}
	return "approved"
}

func newMCPApproveCommand() *cobra.Command {
	var envFlags []string
	cmd := &cobra.Command{
		Use:   "approve <server-name>",
		Short: "Approve a declared MCP server so the daemon will start it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			root, err := repoRoot()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return fmt.Errorf("reading %s: %w", config.SettingsPath(root), err)
			}
			decl, ok := cfg.MCPServers[name]
			if !ok {
				return fmt.Errorf("no MCP server %q declared in %s", name, config.SettingsPath(root))
			}
			local, err := config.LoadLocal(root)
			if err != nil {
				return fmt.Errorf("reading %s: %w", config.SettingsLocalPath(root), err)
			}
			if local.MCPServers == nil {
				local.MCPServers = map[string]config.MCPServerApproval{}
			}
			approval := local.MCPServers[name]
			approval.Approved = true
			if approval.Env == nil {
				approval.Env = map[string]string{}
			}
			for _, kv := range envFlags {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("--env value %q must be KEY=VALUE", kv)
				}
				approval.Env[k] = v
			}
			hash, err := config.DeclHash(decl)
			if err != nil {
				return err
			}
			approval.DeclHash = hash
			local.MCPServers[name] = approval
			if err := config.SaveLocal(root, local); err != nil {
				return err
			}
			warnIfNotGitignored(cmd, root, config.SettingsLocalPath(root))

			var missing []string
			for _, e := range decl.Env {
				if approval.Env[e] == "" {
					missing = append(missing, e)
				}
			}
			if len(missing) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "approved %q, but still missing env: %s\nSupply them with --env KEY=VALUE, or edit %s directly.\n",
					name, strings.Join(missing, ", "), config.SettingsLocalPath(root))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "approved %q -- restart this workspace's daemon to pick it up\n", name)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&envFlags, "env", nil, "KEY=VALUE for a declared env var (repeatable)")
	return cmd
}

func newMCPRejectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reject <server-name>",
		Short: "Revoke approval (and stored env values) for a declared MCP server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			root, err := repoRoot()
			if err != nil {
				return err
			}
			local, err := config.LoadLocal(root)
			if err != nil {
				return fmt.Errorf("reading %s: %w", config.SettingsLocalPath(root), err)
			}
			if _, ok := local.MCPServers[name]; !ok {
				fmt.Fprintf(cmd.OutOrStdout(), "%q was not approved\n", name)
				return nil
			}
			delete(local.MCPServers, name)
			return config.SaveLocal(root, local)
		},
	}
}

// warnIfNotGitignored best-effort warns if path isn't covered by any
// applicable .gitignore. settings.local.json routinely carries real
// secret values (config.MCPServerApproval.Env), and ADR-0036 deliberately
// keeps .masuda/.gitignore entirely user-managed (masuda never writes to
// it), so this is the one place masuda can still catch "about to
// accidentally commit a token" without breaking that principle. Delegates
// the actual ignore-rule evaluation to `git check-ignore` rather than
// reimplementing gitignore semantics, so it correctly considers every
// applicable source (.masuda/.gitignore, the repo's own top-level
// .gitignore, global excludes, ...).
func warnIfNotGitignored(cmd *cobra.Command, root, path string) {
	out, err := exec.Command("git", "-C", root, "check-ignore", "-q", path).CombinedOutput()
	_ = out
	if err == nil {
		return // already ignored
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: %s is not covered by any .gitignore and may contain secret values -- "+
				"add it (or .masuda/*.local.json) to .masuda/.gitignore or %s's own .gitignore\n",
			path, root)
	}
	// Any other exit status (git itself failing, e.g. not a repo) is
	// ignored -- this is best-effort, not a hard requirement.
}
