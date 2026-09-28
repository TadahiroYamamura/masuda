// Package hostenv is the engine.Env the state daemon runs with: it reaches
// a workspace's clone with git and keeps the run's files under the
// workspace's state directory (<state>/wf/).
package hostenv

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/sharedfs"
	"github.com/TadahiroYamamura/masuda/internal/workflow/data"
	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
	"github.com/TadahiroYamamura/masuda/internal/workflow/engine"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

// Perspective is one review perspective as an item source. How
// perspectives are held is undecided (ADR-0082), so they are injected.
type Perspective struct {
	Name    string
	Content string
}

// Env implements engine.Env for one workspace.
type Env struct {
	WorkspaceID string
	RepoRoot    string
	Worktree    string
	// StateDir is shared into the sandbox: files there are for agents to
	// read, and anything the sandbox could have rewritten is never read
	// back. TrustedDir is the host-only counterpart holding what the
	// engine decides by: outputs, the findings ledger, snapshots and the
	// execution log.
	StateDir   string
	TrustedDir string
	BaseRef    string
	Branch     string
	// Store is the daemon's KV store, for the few facts the environment
	// keeps itself (approved deviations, used commit messages).
	Store engine.Store

	Perspectives func() ([]Perspective, error)
	// RunCheckFn runs a checks entry from settings.json; nil until check
	// execution exists.
	RunCheckFn func(name string) (passed bool, feedback string, err error)
	// Teardown removes the workspace after publish or discard. It runs
	// last: after it, the state directory is gone.
	Teardown func() error
	// ExportDir is where exports and logs go (~/.masuda/exports/<id>).
	ExportDir string
	// TranscriptsDir holds the Claude Code conversation logs (ADR-0075);
	// empty if they are not reachable from the host.
	TranscriptsDir string

	mu sync.Mutex
}

func (e *Env) wf(parts ...string) string {
	return filepath.Join(append([]string{e.StateDir, "wf"}, parts...)...)
}

func (e *Env) trusted(parts ...string) string {
	return filepath.Join(append([]string{e.TrustedDir, "wf"}, parts...)...)
}

// Outputs is where agents' outputs are kept: read from the trusted side,
// with a copy in the state directory for agents.
func (e *Env) Outputs() data.Store {
	return data.Store{Dir: e.trusted(), MirrorRoot: e.StateDir, MirrorRel: "wf"}
}

// Ledger is the findings ledger, kept the same way as Outputs.
func (e *Env) Ledger() data.Ledger {
	return data.Ledger{File: e.trusted("findings.json"), MirrorRoot: e.StateDir, MirrorName: filepath.Join("wf", "findings.json")}
}

// Put writes a file the engine hands to agents: first the copy the engine
// keeps, then the one agents read under the state directory, whose path is
// returned. rel is relative to the workflow's directory on both sides.
func (e *Env) Put(rel string, b []byte) (string, error) {
	if err := writeFile(e.trusted(rel), b); err != nil {
		return "", err
	}
	if err := sharedfs.WriteFile(e.StateDir, filepath.Join("wf", rel), b); err != nil {
		return "", err
	}
	return e.wf(rel), nil
}

// trustedOnly are the files under the trusted side that agents never get a
// copy of.
var trustedOnly = map[string]bool{"snapshots": true, "execution-log.jsonl": true}

// VerifyCopies compares every copy handed to agents with the engine's own
// and puts the engine's back where they differ, returning the paths
// (relative to the workflow directory) that had been changed or removed.
// Run before each task, it keeps what one agent did to the shared files
// from reaching the next, and says that it happened.
func (e *Env) VerifyCopies() ([]string, error) {
	var changed []string
	err := filepath.WalkDir(e.trusted(), func(p string, d os.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(e.trusted(), p)
		top, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
		if trustedOnly[top] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		want, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		shared := filepath.Join("wf", rel)
		if got, err := sharedfs.ReadRegular(e.StateDir, shared); err == nil && bytes.Equal(got, want) {
			return nil
		}
		changed = append(changed, rel)
		return sharedfs.WriteFile(e.StateDir, shared, want)
	})
	sort.Strings(changed)
	return changed, err
}

func (e *Env) data() data.Store    { return e.Outputs() }
func (e *Env) ledger() data.Ledger { return e.Ledger() }
func (e *Env) logFile() string     { return e.trusted("execution-log.jsonl") }

// Data resolves a data name to the file an agent is given: the diffs are
// computed now, the rest are the copies of the latest outputs.
func (e *Env) Data(name string) (string, bool, error) {
	if def.EngineComputed[name] {
		return e.computed(name)
	}
	p, ok, err := e.source(name)
	if err != nil || !ok {
		return "", ok, err
	}
	if name == def.DataFindings {
		return e.ledger().MirrorPath(), true, nil
	}
	return e.data().Mirrored(p), true, nil
}

// source is the trusted file behind a data name that agents write.
func (e *Env) source(name string) (string, bool, error) {
	if name == def.DataFindings {
		f := e.ledger().File
		if _, err := os.Stat(f); err != nil {
			return "", false, nil
		}
		return f, true, nil
	}
	return e.data().Latest(name)
}

func (e *Env) computed(name string) (string, bool, error) {
	switch name {
	case def.DataDiff:
		fork, err := worktree.ForkPoint(e.Worktree, e.BaseRef)
		if err != nil {
			return "", false, err
		}
		return e.writeDiff(name, fork)
	case def.DataStepDiff:
		// Steps commit at their boundary, so a step's changes are exactly
		// what differs from HEAD: this equals the diff from the last step
		// tag (or from the plan approval's HEAD for the first step).
		return e.writeDiff(name, "HEAD")
	}
	return "", false, fmt.Errorf("%q is not computed by the engine", name)
}

func (e *Env) writeDiff(name, rev string) (string, bool, error) {
	d, err := worktree.DiffAgainst(e.Worktree, rev)
	if err != nil {
		return "", false, err
	}
	p, err := e.Put(filepath.Join("diffs", fmt.Sprintf("%s-%d.diff", name, time.Now().UnixNano())), []byte(d))
	return p, err == nil, err
}

func (e *Env) plan() (*data.Plan, string, error) {
	p, ok, err := e.data().Latest(def.DataPlan)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", errors.New("no plan has been written")
	}
	return data.ReadPlan(p)
}

func (e *Env) stepTag(planHash string, index int) string {
	return fmt.Sprintf("masuda-step-%s-%s-%d", e.WorkspaceID, planHash[:12], index+1)
}

// stepIndex recovers a step's index from its item key ("step-3" is index
// 2) or item file path.
func stepIndex(keyOrPath string) (int, error) {
	base := strings.TrimSuffix(filepath.Base(keyOrPath), ".json")
	n, err := strconv.Atoi(strings.TrimPrefix(base, "step-"))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%q does not name a step", keyOrPath)
	}
	return n - 1, nil
}

func (e *Env) Items(over, from, since string) ([]engine.Item, error) {
	var items []engine.Item
	add := func(key string, content []byte) error {
		p, err := e.Put(filepath.Join("items", key+itemExt(over)), content)
		if err != nil {
			return err
		}
		items = append(items, engine.Item{Key: key, Path: p})
		return nil
	}
	switch over {
	case def.OverSteps:
		plan, _, err := e.plan()
		if err != nil {
			return nil, err
		}
		for i, s := range plan.Steps {
			b, _ := json.MarshalIndent(s, "", "  ")
			if err := add(fmt.Sprintf("step-%d", i+1), b); err != nil {
				return nil, err
			}
		}
	case def.OverPerspectives:
		if e.Perspectives == nil {
			return nil, errors.New("no perspective source is configured")
		}
		ps, err := e.Perspectives()
		if err != nil {
			return nil, err
		}
		var only map[string]bool
		if from != "" {
			var names []string
			b, err := os.ReadFile(e.data().Path(from, def.DataSelectedPerspectives))
			if err != nil {
				return nil, fmt.Errorf("reading the perspectives %s selected: %w", from, err)
			}
			if err := json.Unmarshal(b, &names); err != nil {
				return nil, err
			}
			only = map[string]bool{}
			for _, n := range names {
				only[n] = true
			}
		}
		for _, p := range ps {
			if only == nil || only[p.Name] {
				if err := add("perspective-"+p.Name, []byte(p.Content)); err != nil {
					return nil, err
				}
			}
		}
	case def.OverFindings:
		rs, err := e.ledger().ToFix(since)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			b, _ := json.MarshalIndent(r, "", "  ")
			if err := add("finding-"+r.ID, b); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unknown foreach set %q", over)
	}
	return items, nil
}

func itemExt(over string) string {
	if over == def.OverPerspectives {
		return ".md"
	}
	return ".json"
}

func (e *Env) StepDone(key string) (bool, error) {
	_, hash, err := e.plan()
	if err != nil {
		return false, err
	}
	i, err := stepIndex(key)
	if err != nil {
		return false, err
	}
	return worktree.TagExists(e.Worktree, e.stepTag(hash, i)), nil
}

func (e *Env) ItemFinished(over, key, outcome string) error {
	if over != def.OverFindings {
		return nil
	}
	status := data.StatusResolved
	if outcome != def.OutcomeDone {
		status = data.StatusUnresolved
	}
	return e.ledger().SetStatus(strings.TrimPrefix(key, "finding-"), status)
}

func (e *Env) RunCheck(name string) (bool, string, error) {
	if e.RunCheckFn == nil {
		return false, "", fmt.Errorf("check %q: running checks is not available", name)
	}
	return e.RunCheckFn(name)
}

const keyApprovedDeviations = "wf:approved-deviations/"

// allowed returns what a commit of this scope may include (ADR-0058,
// ADR-0081): the plan's files for the scope and deviations a human
// approved under this plan.
func (e *Env) allowed(scope, step string) (map[string]bool, *data.Plan, string, int, error) {
	plan, hash, err := e.plan()
	if err != nil {
		return nil, nil, "", 0, err
	}
	index := -1
	if scope == "step" {
		if index, err = stepIndex(step); err != nil {
			return nil, nil, "", 0, err
		}
	}
	ok := map[string]bool{}
	for _, f := range plan.Files(index) {
		ok[f] = true
	}
	var approved []string
	if b, found := e.Store.Get(keyApprovedDeviations + hash); found {
		_ = json.Unmarshal(b, &approved)
	}
	for _, f := range approved {
		ok[f] = true
	}
	return ok, plan, hash, index, nil
}

func (e *Env) Deviations(scope, step string) ([]string, string, error) {
	ok, plan, _, _, err := e.allowed(scope, step)
	if err != nil {
		return nil, "", err
	}
	digests, err := worktree.Digests(e.Worktree)
	if err != nil {
		return nil, "", err
	}
	var dev []string
	sub := map[string]string{}
	for f, d := range digests {
		if !ok[f] && !plan.Byproduct(f) {
			dev = append(dev, f)
			sub[f] = d
		}
	}
	sort.Strings(dev)
	return dev, worktree.HashDigests(sub), nil
}

func (e *Env) Commit(scope, step string, approved []string) error {
	ok, plan, hash, index, err := e.allowed(scope, step)
	if err != nil {
		return err
	}
	if len(approved) > 0 {
		var all []string
		if b, found := e.Store.Get(keyApprovedDeviations + hash); found {
			_ = json.Unmarshal(b, &all)
		}
		all = append(all, approved...)
		b, _ := json.Marshal(all)
		if err := e.Store.Put(keyApprovedDeviations+hash, b); err != nil {
			return err
		}
		for _, f := range approved {
			ok[f] = true
		}
	}
	changed, err := worktree.ChangedFiles(e.Worktree)
	if err != nil {
		return err
	}
	var files []string
	for _, f := range changed {
		if ok[f] {
			files = append(files, f)
		}
	}
	msg, err := e.commitMessage(plan, index)
	if err != nil {
		return err
	}
	if _, err := worktree.CommitFiles(e.Worktree, e.RepoRoot, files, msg); err != nil {
		return err
	}
	if scope == "step" {
		return worktree.Tag(e.Worktree, e.stepTag(hash, index))
	}
	return nil
}

const keyUsedCommitMessages = "wf:used-commit-messages"

// commitMessage takes the newest commit-message written since the last
// commit, and marks it used so a later commit does not reuse it
// (ADR-0081). Without one it falls back to the step description.
func (e *Env) commitMessage(plan *data.Plan, index int) (string, error) {
	used := map[string]bool{}
	if b, ok := e.Store.Get(keyUsedCommitMessages); ok {
		_ = json.Unmarshal(b, &used)
	}
	p, found, err := e.data().Latest(def.DataCommitMessage)
	if err != nil {
		return "", err
	}
	if found && !used[p] {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		used[p] = true
		enc, _ := json.Marshal(used)
		if err := e.Store.Put(keyUsedCommitMessages, enc); err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	if index >= 0 {
		first, _, _ := strings.Cut(strings.TrimSpace(plan.Steps[index].Description), "\n")
		return first, nil
	}
	return fmt.Sprintf("masuda: workspace %sの変更", e.WorkspaceID), nil
}

func (e *Env) Publish(export []string) error {
	if err := worktree.Pull(e.RepoRoot, e.WorkspaceID, e.Branch); err != nil {
		return err
	}
	return e.finishWorkspace(export)
}

func (e *Env) Discard(export []string) error { return e.finishWorkspace(export) }

func (e *Env) finishWorkspace(export []string) error {
	if err := e.Export(export); err != nil {
		return err
	}
	if e.Teardown == nil {
		return nil
	}
	return e.Teardown()
}

// Export copies the named outputs and, always, the logs out of the state
// directory before it is removed (ADR-0075, ADR-0078). Only regular files
// are copied: the state directory is writable by the guest, so a symlink
// there must not make the host copy something else.
func (e *Env) Export(names []string) error {
	if e.ExportDir == "" {
		return nil
	}
	if err := os.MkdirAll(e.ExportDir, 0o755); err != nil {
		return err
	}
	for _, name := range names {
		p, ok, err := e.source(name)
		if def.EngineComputed[name] {
			p, ok, err = e.computed(name)
		}
		if err != nil {
			return err
		}
		if ok {
			if err := copyRegular(p, filepath.Join(e.ExportDir, filepath.Base(p))); err != nil {
				return err
			}
		}
	}
	if err := copyRegular(e.logFile(), filepath.Join(e.ExportDir, "execution-log.jsonl")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if e.TranscriptsDir != "" {
		err := filepath.WalkDir(e.TranscriptsDir, func(p string, d os.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			rel, _ := filepath.Rel(e.TranscriptsDir, p)
			return copyRegular(p, filepath.Join(e.ExportDir, "transcripts", rel))
		})
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (e *Env) TargetHash(target string) (string, error) {
	switch target {
	case "plan":
		_, hash, err := e.plan()
		return hash, err
	case "diff":
		fork, err := worktree.ForkPoint(e.Worktree, e.BaseRef)
		if err != nil {
			return "", err
		}
		d, err := worktree.DiffAgainst(e.Worktree, fork)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256([]byte(d))
		return hex.EncodeToString(sum[:]), nil
	}
	return "", fmt.Errorf("unknown approval target %q", target)
}

func (e *Env) Snapshot() (string, error) {
	d, err := worktree.Digests(e.Worktree)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(d)
	name := fmt.Sprintf("snap-%d", time.Now().UnixNano())
	return name, writeFile(e.trusted("snapshots", name+".json"), b)
}

func (e *Env) ChangedSince(snapshot string) ([]string, string, error) {
	var before map[string]string
	b, err := os.ReadFile(e.trusted("snapshots", snapshot+".json"))
	if err != nil {
		return nil, "", err
	}
	if err := json.Unmarshal(b, &before); err != nil {
		return nil, "", err
	}
	now, err := worktree.Digests(e.Worktree)
	if err != nil {
		return nil, "", err
	}
	changed := map[string]string{}
	for f, d := range now {
		if before[f] != d {
			changed[f] = d
		}
	}
	for f := range before {
		if _, still := now[f]; !still {
			changed[f] = "reverted"
		}
	}
	files := make([]string, 0, len(changed))
	for f := range changed {
		files = append(files, f)
	}
	sort.Strings(files)
	return files, worktree.HashDigests(changed), nil
}

func (e *Env) TreeSnapshot() (string, error) { return worktree.SnapshotTree(e.Worktree) }

func (e *Env) DiffSince(tree string) (string, error) {
	p, _, err := e.writeDiff(def.DataFixDiff, tree)
	return p, err
}

func (e *Env) HasOutput(name, occurrence string) bool { return e.data().Has(occurrence, name) }

func (e *Env) OutputsDone(ctx engine.OutputContext, outputs []string) error {
	for _, out := range outputs {
		switch out {
		case def.DataFindings:
			b, err := os.ReadFile(e.data().Path(ctx.Occurrence, def.DataFindings))
			if err != nil {
				return err
			}
			fs, err := data.ParseFindings(b)
			if err != nil {
				return err
			}
			perspective := ""
			if p, ok := ctx.Inputs["perspective"]; ok {
				perspective = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "perspective-"), ".md")
			}
			err = e.ledger().Import(data.Record{
				Workflow: ctx.Workflow, Perspective: perspective, AgentID: ctx.AgentID,
				Occurrence: ctx.Occurrence, Source: ctx.Frame + "/" + ctx.Node,
			}, fs)
			if err != nil {
				return err
			}
		case def.DataPlan:
			// The summary is for humans reading the plan gate; keep it next
			// to the plan as its own file.
			p := e.data().Path(ctx.Occurrence, def.DataPlan)
			plan, _, err := data.ReadPlan(p)
			if err != nil {
				return err
			}
			if err := e.data().WriteBeside(p, "summary.md", []byte(plan.Summary)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Env) WriteFeedback(occurrence, text string) (string, error) {
	return e.Put(filepath.Join("feedback", occurrence+".md"), []byte(text))
}

func (e *Env) Log(ev engine.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	line := struct {
		Time string `json:"time"`
		engine.Event
	}{time.Now().UTC().Format(time.RFC3339Nano), ev}
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(e.logFile()), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(e.logFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

func writeFile(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

func copyRegular(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
