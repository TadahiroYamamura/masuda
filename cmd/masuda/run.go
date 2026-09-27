package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpclient"
	"github.com/TadahiroYamamura/masuda/internal/workflow/check"
	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
	"github.com/TadahiroYamamura/masuda/internal/workflow/defaults"
	"github.com/TadahiroYamamura/masuda/internal/workflow/engine"
	"github.com/TadahiroYamamura/masuda/internal/workflow/snapshot"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

func newRunCommand() *cobra.Command {
	var dryRun bool
	var stubsPath, base, image, name string
	var inputs []string
	cmd := &cobra.Command{
		Use:   "run <workflow-path> <branch-or-workspace-id>",
		Short: "Start a workflow on a branch, or resume the one running in a workspace",
		Long: `Start a workflow on a branch, or resume the one running in a workspace.

The workflow path is relative to .masuda/ without extension, e.g.
workflows/develop. Given a branch, a new workspace is created: the branch is
cloned if it exists, created from --base otherwise. Given a workspace ID,
its workflow resumes from where its records say it stands.

--input key=value passes a workflow input; key=@path passes the content of
a file on the host, which the sandbox could not read by path.

--dry-run walks the workflow without agents, git or a VM: agent outcomes,
gate decisions, check results and foreach item counts come from --stubs
(anything not scripted takes the successful answer).`,
		Args: func(cmd *cobra.Command, args []string) error {
			if dryRun {
				return cobra.RangeArgs(1, 2)(cmd, args)
			}
			return cobra.ExactArgs(2)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun {
				return runDry(cmd, args[0], stubsPath, inputs)
			}
			root, err := repoRoot()
			if err != nil {
				return err
			}
			if workspace.Exists(args[1]) {
				return resumeRun(cmd, root, args[0], args[1], image)
			}
			return startRun(cmd, root, args[0], args[1], base, image, name, inputs)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "walk the workflow with stubbed outcomes; run no agent, touch no git")
	cmd.Flags().StringVar(&stubsPath, "stubs", "", "YAML file scripting outcomes, gates, checks and item counts for --dry-run")
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "workflow input as key=value or key=@file (repeatable)")
	cmd.Flags().StringVar(&base, "base", defaultBase, "branch to create the branch from if it does not exist, and to diff against")
	cmd.Flags().StringVar(&image, "image", config.DefaultImageEntry, "name of the .masuda/images/ entry to build the VM rootfs from")
	cmd.Flags().StringVar(&name, "name", "", "optional human-readable label for the workspace")
	return cmd
}

// workflowSourceFor reads definitions from root's .masuda/, falling back to
// the bundled defaults (ADR-0064, ADR-0070).
func workflowSourceFor(root string) def.Source {
	src := def.Source{Bundled: defaults.FS()}
	dir := filepath.Join(root, config.DirName)
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		src.Repo = os.DirFS(dir)
	}
	return src
}

func startRun(cmd *cobra.Command, root, workflowPath, branch, base, image, name string, rawInputs []string) error {
	src := workflowSourceFor(root)
	set, errs := def.Load(src, workflowPath)
	if len(errs) == 0 {
		errs = check.Run(set, check.Options{})
	}
	if len(errs) != 0 {
		for _, e := range errs {
			fmt.Fprintln(cmd.ErrOrStderr(), e)
		}
		return errors.New("the workflow does not pass the load-time checks")
	}
	// A fresh workspace has no approved plan, so a workflow that writes
	// before approving one could never pass its own checks here.
	if check.RequiresPlan(set) {
		return fmt.Errorf("%s must be started on a workspace that already has an approved plan; pass that workspace's ID instead of a branch", workflowPath)
	}
	values, err := parseInputs(rawInputs, set.Workflows[workflowPath].Inputs)
	if err != nil {
		return err
	}
	resolvedBase, err := resolveBase(cmd, root, "base", base, defaultBase)
	if err != nil {
		return err
	}
	if !worktree.BranchExists(root, branch) {
		fmt.Fprintf(cmd.OutOrStdout(), "branch %s does not exist; creating it from %s\n", branch, resolvedBase)
	}
	info, worktreeDir, err := newWorkspace(root, branch, resolvedBase, name)
	if err != nil {
		return err
	}
	stateDir, err := workspace.StateDir(info.ID)
	if err != nil {
		return err
	}
	paths, err := writeInputs(stateDir, values)
	if err != nil {
		return err
	}
	store, closeStore, err := daemonStore(cmd.Context(), stateDir)
	if err != nil {
		return err
	}
	defer closeStore()
	fixed, err := snapshot.Save(store, src, workflowPath, check.Options{})
	if err != nil {
		return err
	}
	if err := (&engine.Engine{Set: fixed, Store: store}).Start(paths); err != nil {
		return err
	}
	if err := bootSandbox(cmd, root, info.ID, worktreeDir, stateDir, image); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "workspace=%s workflow=%s branch=%s\n", info.ID, workflowPath, branch)
	return nil
}

func resumeRun(cmd *cobra.Command, root, workflowPath, id, image string) error {
	if err := ensureDaemon(id); err != nil {
		return err
	}
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		return err
	}
	store, closeStore, err := daemonStore(cmd.Context(), stateDir)
	if err != nil {
		return err
	}
	defer closeStore()
	set, err := snapshot.Load(store)
	if err != nil {
		return err
	}
	if set.Root != workflowPath {
		return fmt.Errorf("workspace %s is running %s, not %s", id, set.Root, workflowPath)
	}
	info, err := workspace.Load(id)
	if err != nil {
		return err
	}
	if !sandboxBackend.IsRunning(id) {
		if err := bootSandbox(cmd, info.RepoRoot, id, worktree.Dir(info.RepoRoot, id), stateDir, image); err != nil {
			return err
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "workspace=%s workflow=%s resumed\n", id, workflowPath)
	return nil
}

func bootSandbox(cmd *cobra.Command, root, id, worktreeDir, stateDir, image string) error {
	resolvedImage, err := resolveImage(cmd, root, image, config.DefaultImageEntry)
	if err != nil {
		return err
	}
	warnIfNoClaudeToken(cmd)
	_, err = sandboxBackend.Start(id, worktreeDir, stateDir, root, resolvedImage)
	return err
}

// parseInputs checks the given inputs against what the workflow declares:
// all declared inputs are required and nothing else is accepted (ADR-0077).
func parseInputs(raw []string, declared []string) (map[string]string, error) {
	want := map[string]bool{}
	for _, d := range declared {
		want[d] = true
	}
	values := map[string]string{}
	for _, kv := range raw {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--input %q: want key=value or key=@file", kv)
		}
		if !want[k] {
			return nil, fmt.Errorf("--input %s: the workflow declares no such input (it takes: %s)", k, strings.Join(declared, ", "))
		}
		if strings.HasPrefix(v, "@") {
			b, err := os.ReadFile(strings.TrimPrefix(v, "@"))
			if err != nil {
				return nil, fmt.Errorf("--input %s: %w", k, err)
			}
			v = string(b)
		}
		values[k] = v
	}
	var missing []string
	for _, d := range declared {
		if _, ok := values[d]; !ok {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing --input for: %s", strings.Join(missing, ", "))
	}
	return values, nil
}

// writeInputs stores each input as a file in the shared state directory:
// agents receive inputs as paths, never inlined (ADR-0077).
func writeInputs(stateDir string, values map[string]string) (map[string]string, error) {
	paths := map[string]string{}
	for k, v := range values {
		p := filepath.Join(stateDir, "wf", "inputs", k+".md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			return nil, err
		}
		paths[k] = p
	}
	return paths, nil
}

// clientStore is the daemon's store reached over its trusted socket, for
// the CLI to fix the definitions and record the run's start.
type clientStore struct {
	ctx context.Context
	c   *mcpclient.Client
}

func (s clientStore) Get(k string) ([]byte, bool) {
	v, ok, err := s.c.Get(s.ctx, k)
	if err != nil || !ok {
		return nil, false
	}
	return []byte(v), true
}
func (s clientStore) Put(k string, v []byte) error { return s.c.Put(s.ctx, k, string(v)) }
func (s clientStore) Delete(k string) error        { return s.c.Delete(s.ctx, k) }
func (s clientStore) List(prefix string) []string {
	keys, _ := s.c.List(s.ctx, prefix)
	return keys
}

func daemonStore(ctx context.Context, stateDir string) (engine.Store, func(), error) {
	c, err := mcpclient.Dial(ctx, statedaemon.SocketPath(stateDir))
	if err != nil {
		return nil, nil, err
	}
	return clientStore{ctx: ctx, c: c}, func() { c.Close() }, nil
}

func runDry(cmd *cobra.Command, workflowPath, stubsPath string, rawInputs []string) error {
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
	set, errs := def.Load(workflowSource(), workflowPath)
	if len(errs) == 0 {
		errs = check.Run(set, check.Options{})
	}
	if len(errs) != 0 {
		for _, e := range errs {
			fmt.Fprintln(cmd.ErrOrStderr(), e)
		}
		return errors.New("the workflow does not pass the load-time checks")
	}
	in := map[string]string{}
	for _, kv := range rawInputs {
		k, v, _ := strings.Cut(kv, "=")
		in[k] = v
	}
	for _, name := range set.Workflows[set.Root].Inputs {
		if _, ok := in[name]; !ok {
			return fmt.Errorf("%s needs --input %s=...", workflowPath, name)
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
}
