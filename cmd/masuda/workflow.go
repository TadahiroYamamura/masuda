package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/workflow/check"
	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
	"github.com/TadahiroYamamura/masuda/internal/workflow/defaults"
	"github.com/TadahiroYamamura/masuda/internal/workflow/render"
)

func newWorkflowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Inspect workflow definitions (.masuda/workflows/, falling back to the bundled defaults)",
	}
	cmd.AddCommand(newWorkflowCheckCommand(), newWorkflowShowCommand())
	return cmd
}

// workflowSource resolves definitions from the current repository's
// .masuda/ first and the bundled defaults second. Outside a git repository
// only the defaults are visible.
func workflowSource() def.Source {
	src := def.Source{Bundled: defaults.FS()}
	if root, err := repoRoot(); err == nil {
		dir := filepath.Join(root, config.DirName)
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			src.Repo = os.DirFS(dir)
		}
	}
	return src
}

func newWorkflowCheckCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "check [workflow-path...]",
		Short: "Run the load-time checks on workflows (default: every top-level workflow)",
		Long: `Run the load-time checks on workflows (default: every top-level workflow).

A workflow path is relative to .masuda/ without extension, e.g.
workflows/develop. With no argument, every workflow directly under
workflows/ is checked, in the repository and among the bundled defaults.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			src := workflowSource()
			paths := args
			if len(paths) == 0 {
				var err error
				if paths, err = topLevelWorkflows(src); err != nil {
					return err
				}
			}
			// Check names are verified against the repository's settings when
			// there is one; outside a repository they are left unverified.
			var opts check.Options
			if root, err := repoRoot(); err == nil {
				cfg, err := loadConfig(root)
				if err != nil {
					return err
				}
				if err := cfg.ValidateChecks(); err != nil {
					return err
				}
				opts.CheckNames = map[string]bool{}
				for n := range cfg.Checks {
					opts.CheckNames[n] = true
				}
			}
			failed := false
			out := cmd.OutOrStdout()
			for _, p := range paths {
				set, errs := def.Load(src, p)
				if len(errs) == 0 {
					errs = check.Run(set, opts)
				}
				if len(errs) != 0 {
					failed = true
					fmt.Fprintf(out, "%s: %d problem(s)\n", p, len(errs))
					for _, e := range errs {
						fmt.Fprintf(out, "  %s\n", e)
					}
					continue
				}
				note := ""
				if check.RequiresPlan(set) {
					note = " (must be started on a workspace that already has an approved plan)"
				}
				fmt.Fprintf(out, "%s: ok%s\n", p, note)
			}
			if failed {
				return errors.New("workflow check found problems")
			}
			return nil
		},
	}
}

func newWorkflowShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <workflow-path>",
		Short: "Print a workflow as a Mermaid diagram, with the safety checks the engine always inserts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := workflowSource()
			b, origin, err := src.Read(args[0])
			if err != nil {
				return err
			}
			w, errs := def.ParseWorkflow(args[0], b)
			if len(errs) != 0 {
				return fmt.Errorf("%s does not parse: %v", args[0], errs)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%%%% source: %s\n%s", origin, render.Mermaid(w))
			return nil
		},
	}
}

// topLevelWorkflows lists the workflows directly under workflows/, the
// ones meant to be started with `masuda run`, from both layers.
func topLevelWorkflows(src def.Source) ([]string, error) {
	seen := map[string]bool{}
	for _, fsys := range []fs.FS{src.Repo, src.Bundled} {
		if fsys == nil {
			continue
		}
		entries, err := fs.ReadDir(fsys, "workflows")
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
				seen["workflows/"+strings.TrimSuffix(e.Name(), ".yaml")] = true
			}
		}
	}
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}
