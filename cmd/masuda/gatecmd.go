package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpclient"
	"github.com/TadahiroYamamura/masuda/internal/workflow/data"
	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
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
			req, c, _, err := openGateRequest(cmd, args[0], args[1])
			if err != nil {
				return err
			}
			defer c.Close()
			trusted, err := workspace.TrustedDir(args[0])
			if err != nil {
				return err
			}
			// What is shown comes from the host-only copies, the same ones
			// the gate's hash is taken over.
			outputs := data.Store{Dir: filepath.Join(trusted, "wf")}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "gate: %s\noccurrence: %s\ntarget: %s\n\n", req.Name, req.Occurrence, req.Target)
			switch {
			case len(req.Files) > 0:
				fmt.Fprintln(out, "changed files:")
				for _, f := range req.Files {
					fmt.Fprintf(out, "  %s\n", f)
				}
			case req.Target == "plan":
				p, ok, err := outputs.Latest(def.DataPlan)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("no plan has been written")
				}
				plan, _, err := data.ReadPlan(p)
				if err != nil {
					return err
				}
				printPlan(out, plan)
				fmt.Fprintf(out, "\n(full plan: %s)\n", p)
			case req.Target == "diff":
				info, err := workspace.Load(args[0])
				if err != nil {
					return err
				}
				dir := worktree.Dir(info.RepoRoot, args[0])
				fork, err := worktree.ForkPoint(dir, info.Base)
				if err != nil {
					return err
				}
				stat, err := exec.Command("git", "-C", dir, "diff", "--stat", fork).CombinedOutput()
				if err != nil {
					return fmt.Errorf("git diff --stat: %w\n%s", err, stat)
				}
				fmt.Fprintf(out, "%s", stat)
				if report, ok, err := outputs.Latest(def.DataReport); err == nil && ok {
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

// printPlan shows what approving a plan commits to: besides the summary,
// the steps and the files each may change, which is what later commits are
// checked against.
func printPlan(w io.Writer, plan *data.Plan) {
	fmt.Fprintf(w, "%s\n\nsteps:\n", strings.TrimSpace(plan.Summary))
	for i, st := range plan.Steps {
		fmt.Fprintf(w, "  %d. %s\n", i+1, st.Description)
		for _, f := range st.Files {
			fmt.Fprintf(w, "       %s\n", f.Path)
		}
	}
	if len(plan.ExpectedByproducts) > 0 {
		fmt.Fprintf(w, "\nexpected byproducts (left uncommitted, not counted as changes outside the plan): %s\n", strings.Join(plan.ExpectedByproducts, ", "))
	}
}
