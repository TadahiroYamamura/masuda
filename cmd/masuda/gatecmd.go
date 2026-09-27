package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpclient"
	"github.com/TadahiroYamamura/masuda/internal/workflow/engine"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

// newGateCommand operates the approval gates of a running workflow
// (ADR-0066, ADR-0078). Gate names come from the workflow definition, so
// they are an argument rather than one subcommand per gate. Approving is
// only possible from the host: nothing in the sandbox can write a
// decision (ADR-0079).
func newGateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Show, approve or reject a workflow's open approval gate",
	}
	cmd.AddCommand(newGateShowCommand(), newGateDecideCommand(true), newGateDecideCommand(false))
	return cmd
}

func openGateRequest(cmd *cobra.Command, id, gate string) (engine.GateRequest, *mcpclient.Client, string, error) {
	stateDir, err := ensureGateWorkspace(id)
	if err != nil {
		return engine.GateRequest{}, nil, "", err
	}
	c, err := mcpclient.Dial(cmd.Context(), statedaemon.SocketPath(stateDir))
	if err != nil {
		return engine.GateRequest{}, nil, "", err
	}
	v, found, err := c.Get(cmd.Context(), "wf:gate-open/"+gate)
	if err != nil {
		c.Close()
		return engine.GateRequest{}, nil, "", err
	}
	if !found {
		c.Close()
		return engine.GateRequest{}, nil, "", fmt.Errorf("gate %q is not waiting for a decision in workspace %s", gate, id)
	}
	var req engine.GateRequest
	if err := json.Unmarshal([]byte(v), &req); err != nil {
		c.Close()
		return engine.GateRequest{}, nil, "", err
	}
	return req, c, stateDir, nil
}

func newGateShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "show <workspace-id> <gate>",
		Short:             "Print what an open gate asks you to decide on",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req, c, stateDir, err := openGateRequest(cmd, args[0], args[1])
			if err != nil {
				return err
			}
			defer c.Close()
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "gate: %s\noccurrence: %s\ntarget: %s\n\n", req.Name, req.Occurrence, req.Target)
			switch {
			case len(req.Files) > 0:
				fmt.Fprintln(out, "changed files:")
				for _, f := range req.Files {
					fmt.Fprintf(out, "  %s\n", f)
				}
			case req.Target == "plan":
				summary, err := latestFile(filepath.Join(stateDir, "wf", "out"), "summary.md")
				if err != nil {
					return err
				}
				b, err := os.ReadFile(summary)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "%s\n\n(full plan: %s)\n", b, filepath.Join(filepath.Dir(summary), "plan.json"))
			case req.Target == "diff":
				info, err := workspace.Load(args[0])
				if err != nil {
					return err
				}
				stat, err := exec.Command("git", "-C", worktree.Dir(info.RepoRoot, args[0]), "diff", "--stat", info.Base).CombinedOutput()
				if err != nil {
					return fmt.Errorf("git diff --stat: %w\n%s", err, stat)
				}
				fmt.Fprintf(out, "%s", stat)
				if report, err := latestFile(filepath.Join(stateDir, "wf", "out"), "report.md"); err == nil {
					fmt.Fprintf(out, "\nreview report: %s\n", report)
				}
			}
			return nil
		},
	}
}

func newGateDecideCommand(approve bool) *cobra.Command {
	use, short := "approve", "Approve the open gate"
	if !approve {
		use, short = "reject", "Reject the open gate; the comment goes to the next step"
	}
	return &cobra.Command{
		Use:               use + " <workspace-id> <gate> [comment]",
		Short:             short,
		Args:              cobra.RangeArgs(2, 3),
		ValidArgsFunction: completeWorkspaceIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req, c, _, err := openGateRequest(cmd, args[0], args[1])
			if err != nil {
				return err
			}
			defer c.Close()
			d := engine.Decision{Occurrence: req.Occurrence, Hash: req.Hash, Approved: approve}
			if len(args) == 3 {
				d.Comment = args[2]
			}
			b, err := json.Marshal(d)
			if err != nil {
				return err
			}
			// The decision names the occurrence and content hash shown by the
			// open request. The engine checks both against what it is
			// asking now before taking the decision, so a decision on
			// content that changed in the meantime is dropped (ADR-0066).
			if err := c.Put(cmd.Context(), "wf:gate-decision/"+req.Name, string(b)); err != nil {
				return err
			}
			result := "rejected"
			if approve {
				result = "approved"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (occurrence %s)\n", req.Name, result, req.Occurrence)
			return nil
		},
	}
}

// latestFile finds the newest copy of name under the per-occurrence output
// directories.
func latestFile(outDir, name string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(outDir, "*", name))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no %s has been written", strings.TrimSuffix(name, filepath.Ext(name)))
	}
	sort.Strings(matches)
	return matches[len(matches)-1], nil
}
