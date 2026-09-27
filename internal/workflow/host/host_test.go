package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/workflow/check"
	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
	"github.com/TadahiroYamamura/masuda/internal/workflow/defaults"
	"github.com/TadahiroYamamura/masuda/internal/workflow/engine"
	"github.com/TadahiroYamamura/masuda/internal/workflow/hostenv"
	"github.com/TadahiroYamamura/masuda/internal/workflow/snapshot"
)

func setup(t *testing.T) (*Host, *hostenv.Env) {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "t"}, {"config", "user.email", "t@e"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	store := engine.NewMemStore()
	if _, err := snapshot.Save(store, def.Source{Bundled: defaults.FS()}, "workflows/develop", check.Options{}); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	instructions := filepath.Join(state, "wf", "inputs", "instructions.md")
	_ = os.MkdirAll(filepath.Dir(instructions), 0o755)
	_ = os.WriteFile(instructions, []byte("READMEを直す"), 0o644)
	env := &hostenv.Env{WorkspaceID: "ws", RepoRoot: repo, Worktree: repo, StateDir: state, BaseRef: "main", Branch: "main"}
	h, err := New(store, env)
	if err != nil || h == nil {
		t.Fatalf("New = %v %v", h, err)
	}
	if err := h.eng.Start(map[string]string{"instructions": instructions}); err != nil {
		t.Fatal(err)
	}
	return h, env
}

func TestNextTaskWritesInstructionsWithGuestPaths(t *testing.T) {
	h, _ := setup(t)
	next, err := h.NextTask("", "")
	if err != nil {
		t.Fatal(err)
	}
	if next.Kind != "agent" || next.Agent != "investigator" || !strings.HasPrefix(next.Instructions, "/masuda-state/wf/tasks/") {
		t.Fatalf("next = %+v", next)
	}
	b, err := os.ReadFile(filepath.Join(h.env.StateDir, strings.TrimPrefix(next.Instructions, "/masuda-state/")))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, want := range []string{"/masuda-state/wf/inputs/instructions.md", "write_output", "report_result", "- done:", next.Occurrence} {
		if !strings.Contains(body, want) {
			t.Errorf("instructions lack %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, h.env.StateDir) {
		t.Errorf("instructions leak a host path:\n%s", body)
	}
}

func TestOutputsReportAndPlanGate(t *testing.T) {
	h, _ := setup(t)
	inv, _ := h.NextTask("", "")
	if _, err := h.WriteOutput(inv.Occurrence, "plan", "{}"); err == nil {
		t.Fatal("the investigator wrote an output it does not declare")
	}
	if _, err := h.WriteOutput(inv.Occurrence, "investigation", "調べた"); err != nil {
		t.Fatal(err)
	}
	if err := h.Report(inv.Occurrence, "done", ""); err != nil {
		t.Fatal(err)
	}
	plan, err := h.NextTask(inv.Occurrence, "agent-1")
	if err != nil || plan.Agent != "planner" {
		t.Fatalf("next = %+v %v", plan, err)
	}
	if _, err := h.WriteOutput(plan.Occurrence, "plan", `{"summary": "x", "steps": []}`); err == nil || !strings.Contains(err.Error(), "steps is empty") {
		t.Fatalf("invalid plan accepted: %v", err)
	}
	valid := `{"summary": "READMEを直す", "steps": [{"description": "直す", "files": [{"path": "README.md", "description": ""}]}]}`
	if _, err := h.WriteOutput(plan.Occurrence, "plan", valid); err != nil {
		t.Fatal(err)
	}
	if err := h.Report(plan.Occurrence, "done", ""); err != nil {
		t.Fatal(err)
	}
	gate, err := h.NextTask(plan.Occurrence, "")
	if err != nil || gate.Kind != "gate" || gate.Gate != "plan" {
		t.Fatalf("next = %+v %v", gate, err)
	}
	if req, ok := h.OpenGate("plan"); !ok || req.Target != "plan" || req.Hash == "" {
		t.Fatalf("open gate = %+v %v", req, ok)
	}
}

func TestLazyReportsNoRun(t *testing.T) {
	l := &Lazy{Store: engine.NewMemStore(), Env: &hostenv.Env{}}
	if _, err := l.Get(); err == nil || !strings.Contains(err.Error(), "masuda run") {
		t.Fatalf("Get = %v", err)
	}
}

func TestWriteClaudeAgentsAddsWorkflowTools(t *testing.T) {
	h, env := setup(t)
	if err := WriteClaudeAgents(h.eng.Set, env.StateDir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(env.StateDir, ClaudeAgentsDir, "reviewer.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"name: reviewer", "tools: Read, Grep, Glob, mcp__masuda-gate__write_output, mcp__masuda-gate__report_result, mcp__masuda-gate__report_concern", "指示ファイルを読み"} {
		if !strings.Contains(s, want) {
			t.Errorf("reviewer.md lacks %q:\n%s", want, s)
		}
	}
}
