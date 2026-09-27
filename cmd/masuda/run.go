package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/TadahiroYamamura/masuda/internal/workflow/check"
	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
	"github.com/TadahiroYamamura/masuda/internal/workflow/engine"
)

func newRunCommand() *cobra.Command {
	var dryRun bool
	var stubsPath string
	var inputs []string
	cmd := &cobra.Command{
		Use:   "run <workflow-path> [branch-or-workspace-id]",
		Short: "Run a workflow (currently only --dry-run is available)",
		Long: `Run a workflow.

--dry-run walks the workflow without agents, git or a VM: agent outcomes,
gate decisions, check results and foreach item counts come from --stubs
(anything not scripted takes the successful answer), and each answer is
printed as it is given.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dryRun {
				return errors.New("only --dry-run is implemented so far")
			}
			in := map[string]string{}
			for _, kv := range inputs {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("--input %q: want key=value", kv)
				}
				in[k] = v
			}
			var stubs engine.Stubs
			if stubsPath != "" {
				b, err := os.ReadFile(stubsPath)
				if err != nil {
					return err
				}
				dec := yaml.NewDecoder(strings.NewReader(string(b)))
				dec.KnownFields(true)
				if err := dec.Decode(&stubs); err != nil {
					return fmt.Errorf("%s: %w", stubsPath, err)
				}
			}
			set, errs := def.Load(workflowSource(), args[0])
			if len(errs) == 0 {
				errs = check.Run(set, check.Options{})
			}
			if len(errs) != 0 {
				for _, e := range errs {
					fmt.Fprintln(cmd.ErrOrStderr(), e)
				}
				return errors.New("the workflow does not pass the load-time checks")
			}
			for _, name := range set.Workflows[set.Root].Inputs {
				if _, ok := in[name]; !ok {
					return fmt.Errorf("%s needs --input %s=...", args[0], name)
				}
			}
			e := &engine.Engine{Set: set, Store: engine.NewMemStore(), Env: engine.NewStubEnv(stubs)}
			if err := e.Start(in); err != nil {
				return err
			}
			trace, st, err := engine.DryRun(e, stubs, 10000)
			out := cmd.OutOrStdout()
			for _, line := range trace {
				fmt.Fprintln(out, line)
			}
			if err != nil {
				return err
			}
			switch st.Kind {
			case engine.StatusDone:
				fmt.Fprintf(out, "finished: %s\n", st.Outcome)
			case engine.StatusBlocked:
				fmt.Fprintf(out, "blocked: %s\n", st.Reason)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "walk the workflow with stubbed outcomes; run no agent, touch no git")
	cmd.Flags().StringVar(&stubsPath, "stubs", "", "YAML file scripting outcomes, gates, checks and item counts for --dry-run")
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "workflow input as key=value (repeatable)")
	return cmd
}
