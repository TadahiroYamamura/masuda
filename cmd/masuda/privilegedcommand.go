package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

// newPrivilegedCommandCommand groups commands for managing this
// repository's declared privileged commands (ADR-0053):
// config.Config.PrivilegedCommands (repoRoot's committed
// .masuda/settings.json) only *declares* which commands want to run in a
// disposable, privileged VM -- these commands manage this user's
// *approval* of them, recorded in the gitignored
// .masuda/settings.local.json (config.LocalSettings). Same shape as
// newMCPCommand (cmd/masuda/mcp.go) and newEgressCommand for the same
// reason (Issue #19), operating on the repository directly rather than a
// workspace.
func newPrivilegedCommandCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "privileged-command",
		Short: "Manage this repository's declared privileged commands (run in a disposable VM)",
	}
	cmd.AddCommand(newPrivilegedCommandListCommand())
	cmd.AddCommand(newPrivilegedCommandApproveCommand())
	cmd.AddCommand(newPrivilegedCommandRejectCommand())
	return cmd
}

func newPrivilegedCommandListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List privileged commands declared in .masuda/settings.json and this user's approval status",
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
			if len(cfg.PrivilegedCommands) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no privileged commands declared in %s\n", config.SettingsPath(root))
				return nil
			}
			names := make([]string, 0, len(cfg.PrivilegedCommands))
			for name := range cfg.PrivilegedCommands {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				decl := cfg.PrivilegedCommands[name]
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", name, privilegedCommandStatus(root, decl, local.PrivilegedCommands[name]))
			}
			return nil
		},
	}
}

// privilegedCommandStatus summarizes decl/approval the way a run request
// will decide whether to start a disposable VM at all, so `masuda
// privileged-command list` never reports a state the runner would then
// refuse.
func privilegedCommandStatus(root string, decl config.PrivilegedCommandDecl, approval config.PrivilegedCommandApproval) string {
	if err := validatePrivilegedCommandDecl(root, decl); err != nil {
		return "unrunnable declaration: " + err.Error()
	}
	if !approval.Approved {
		return "not approved"
	}
	hash, err := config.PrivilegedCommandHash(root, decl)
	if err != nil || approval.DeclHash != hash {
		// Also reached when the declaration itself is untouched but the
		// image entry's contents changed: the approval covers both
		// (config.PrivilegedCommandHash).
		return "approved, but declaration or image changed since -- re-approve"
	}
	return "approved"
}

// validatePrivilegedCommandDecl rejects declarations no approval could
// meaningfully cover. Image has no masuda-side fallback by design
// (config.PrivilegedCommandDecl.Image), so an entry missing it can never
// run; refusing that at approve time keeps the failure where a human is
// already reading the declaration, rather than surfacing later when the AI
// requests a run and can only report a broken tool call.
func validatePrivilegedCommandDecl(root string, decl config.PrivilegedCommandDecl) error {
	if strings.TrimSpace(decl.Command) == "" {
		return errors.New(`"command" is empty`)
	}
	if strings.TrimSpace(decl.Image) == "" {
		return errors.New(`"image" is empty, and masuda has no built-in default image`)
	}
	if err := config.ValidateImageEntry(decl.Image); err != nil {
		return err
	}
	if _, err := os.Stat(config.ImageDockerfilePath(root, decl.Image)); err != nil {
		return fmt.Errorf(`"image": no Dockerfile at %s`, config.ImageDockerfilePath(root, decl.Image))
	}
	if decl.TimeoutSeconds < 0 {
		return fmt.Errorf(`"timeoutSeconds" is negative (%d)`, decl.TimeoutSeconds)
	}
	for _, out := range decl.Outputs {
		if err := config.ValidateOutputPath(out); err != nil {
			return err
		}
	}
	return nil
}

func newPrivilegedCommandApproveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "approve <command-name>",
		Short: "Approve a declared privileged command so it may run in a disposable VM",
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
			decl, ok := cfg.PrivilegedCommands[name]
			if !ok {
				return fmt.Errorf("no privileged command %q declared in %s", name, config.SettingsPath(root))
			}
			if err := validatePrivilegedCommandDecl(root, decl); err != nil {
				return fmt.Errorf("privileged command %q in %s: %w", name, config.SettingsPath(root), err)
			}
			local, err := config.LoadLocal(root)
			if err != nil {
				return fmt.Errorf("reading %s: %w", config.SettingsLocalPath(root), err)
			}
			if local.PrivilegedCommands == nil {
				local.PrivilegedCommands = map[string]config.PrivilegedCommandApproval{}
			}
			hash, err := config.PrivilegedCommandHash(root, decl)
			if err != nil {
				return err
			}
			local.PrivilegedCommands[name] = config.PrivilegedCommandApproval{Approved: true, DeclHash: hash}
			if err := config.SaveLocal(root, local); err != nil {
				return err
			}
			warnIfNotGitignored(cmd, root, config.SettingsLocalPath(root))

			// Echo what was approved: the AI can only ever name this
			// entry, never pass a command line of its own, so this
			// printout is the one place a human sees the exact string
			// their approval authorizes.
			fmt.Fprintf(cmd.OutOrStdout(), "approved %q: %s\nruns in image entry %s\n", name, decl.Command, decl.Image)
			if len(decl.Outputs) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "collects: %s\n", strings.Join(decl.Outputs, ", "))
			}
			return nil
		},
	}
}

func newPrivilegedCommandRejectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reject <command-name>",
		Short: "Revoke approval for a declared privileged command",
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
			if _, ok := local.PrivilegedCommands[name]; !ok {
				fmt.Fprintf(cmd.OutOrStdout(), "%q was not approved\n", name)
				return nil
			}
			delete(local.PrivilegedCommands, name)
			return config.SaveLocal(root, local)
		},
	}
}
