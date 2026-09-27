package def

import (
	"strings"
	"testing"
	"testing/fstest"
)

const developHead = `version: 1
inputs: [instructions]
start: investigate
nodes:
  investigate:
    type: investigate
    workflow: workflows/investigate/default
    next: plan
  plan:
    type: plan
    workflow: workflows/plan/default
    max: 4
    next:
      done: approve-plan
      needs_more_investigation: investigate
      out_of_scope: end:out_of_scope
  approve-plan:
    type: approval
    gate: plan
    target: plan
    next:
      approved: end
      rejected: plan
`

func TestParseWorkflowReadsNodesInFileOrder(t *testing.T) {
	w, errs := ParseWorkflow("workflows/develop", []byte(developHead))
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if got := strings.Join(w.Order, ","); got != "investigate,plan,approve-plan" {
		t.Fatalf("Order = %s", got)
	}
	plan := w.Nodes["plan"]
	if plan.Type != TypePlan || plan.Workflow != "workflows/plan/default" || plan.Max != 4 {
		t.Fatalf("plan = %+v", plan)
	}
	if got := plan.Next["out_of_scope"]; !got.End || got.Label != "out_of_scope" {
		t.Fatalf("out_of_scope target = %+v", got)
	}
	approve := w.Nodes["approve-plan"]
	if approve.Gate != "plan" || approve.GateTarget != "plan" {
		t.Fatalf("approve-plan = %+v", approve)
	}
}

func TestParseWorkflowBareNextMeansDone(t *testing.T) {
	w, errs := ParseWorkflow("workflows/develop", []byte(developHead))
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	next := w.Nodes["investigate"].Next
	if len(next) != 1 || next[OutcomeDone].Node != "plan" {
		t.Fatalf("Next = %+v, want {done: plan}", next)
	}
}

func TestParseWorkflowRejects(t *testing.T) {
	cases := []struct {
		name string
		body string // one node named "a", plus whatever else it needs
		want string
	}{
		{"field of another type", "a:\n    type: agent\n    role: agents/x\n    gate: plan\n    next: end\n", `cannot have "gate"`},
		{"approval without target", "a:\n    type: approval\n    gate: plan\n    next: {approved: end, rejected: end}\n", "needs target"},
		{"unknown target value", "a:\n    type: approval\n    gate: plan\n    target: code\n    next: {approved: end, rejected: end}\n", "target must be plan or diff"},
		{"short role reference", "a:\n    type: agent\n    role: planner\n    next: end\n", "must be a path under .masuda/"},
		{"reference with extension", "a:\n    type: workflow\n    workflow: workflows/x.yaml\n    next: end\n", "must be a path under .masuda/"},
		{"unknown over", "a:\n    type: foreach\n    over: review-methods\n    body: workflows/x\n    next: end\n", "over must be"},
		{"end:done", "a:\n    type: agent\n    role: agents/x\n    next: end:done\n", "instead of `end:done`"},
		{"missing next target", "a:\n    type: agent\n    role: agents/x\n    next: nowhere\n", `points to "nowhere"`},
		{"file name in export", "a:\n    type: discard\n    export: [final_report.md]\n    next: end\n", "not file names"},
		{"old extends syntax", "a:\n    extends: masuda/agent\n    role: agents/x\n    next: end\n", "type is missing"},
		{"unknown type", "a:\n    type: planner\n    next: end\n", `unknown type "planner"`},
		{"end as node name", "end:\n    type: agent\n    role: agents/x\n    next: end\n", "invalid node name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := "a"
			if strings.HasPrefix(c.body, "end:") {
				start = "end"
			}
			src := "version: 1\nstart: " + start + "\nnodes:\n  " + c.body
			_, errs := ParseWorkflow("workflows/t", []byte(src))
			if !containsMsg(errs, c.want) {
				t.Fatalf("errs = %v, want one containing %q", errs, c.want)
			}
		})
	}
}

func TestParseWorkflowForeachFromAndDefaults(t *testing.T) {
	src := `version: 1
inputs: [step]
start: pick
nodes:
  pick:
    type: agent
    role: agents/trigger-matcher
    next: find
  find:
    type: foreach
    over: perspectives(from=pick)
    body: workflows/review/perspective-review
    with:
      diff: step-diff
    next: end
`
	w, errs := ParseWorkflow("workflows/implement/build-step", []byte(src))
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	f := w.Nodes["find"]
	if f.Over != OverPerspectives || f.OverFrom != "pick" || f.OnIncomplete != "stop" || f.With["diff"] != DataStepDiff {
		t.Fatalf("find = %+v", f)
	}
}

func TestParseAgent(t *testing.T) {
	src := `---
name: reviewer
tools: Read, Grep, Glob
outputs: [findings]
resume: true
outcomes:
  done: 観点に沿って差分を読み、指摘を書き終えた
---
差分を読んで、観点に沿って指摘せよ。
`
	a, errs := ParseAgent("agents/reviewer", []byte(src))
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if a.WriteCapable() {
		t.Fatal("WriteCapable() = true for Read/Grep/Glob")
	}
	if !a.Resume || !a.DeclaresOutput(DataFindings) || a.Prompt != "差分を読んで、観点に沿って指摘せよ。" {
		t.Fatalf("agent = %+v", a)
	}
}

func TestAgentWriteCapable(t *testing.T) {
	cases := map[string]bool{
		"":                            true, // no tools line: Claude Code gives all tools
		"tools: Read, Bash\n":         false,
		"tools: [Read, Edit]\n":       true,
		"tools: Read, NotebookEdit\n": true,
		"tools: []\n":                 false,
	}
	for tools, want := range cases {
		src := "---\n" + tools + "outcomes:\n  done: ok\n---\nbody\n"
		a, errs := ParseAgent("agents/x", []byte(src))
		if len(errs) != 0 {
			t.Fatalf("%q: errs = %v", tools, errs)
		}
		if got := a.WriteCapable(); got != want {
			t.Errorf("%q: WriteCapable() = %v, want %v", tools, got, want)
		}
	}
}

func TestParseAgentRejects(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"no frontmatter", "just text\n", "must start with YAML frontmatter"},
		{"reserved outcome", "---\noutcomes:\n  done: ok\n  exhausted: gave up\n---\nbody\n", "reserved for the engine"},
		{"no done", "---\noutcomes:\n  stuck: cannot\n---\nbody\n", `must include "done"`},
		{"file name output", "---\noutputs: [final_report.md]\noutcomes:\n  done: ok\n---\nbody\n", "invalid output name"},
		{"empty body", "---\noutcomes:\n  done: ok\n---\n\n", "prompt body is empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, errs := ParseAgent("agents/x", []byte(c.src))
			if !containsMsg(errs, c.want) {
				t.Fatalf("errs = %v, want one containing %q", errs, c.want)
			}
		})
	}
}

func TestSourceRepoReplacesBundledWhole(t *testing.T) {
	bundled := fstest.MapFS{
		"workflows/a.yaml": {Data: []byte("bundled a")},
		"workflows/b.yaml": {Data: []byte("bundled b")},
	}
	repo := fstest.MapFS{"workflows/a.yaml": {Data: []byte("repo a")}}
	src := Source{Repo: repo, Bundled: bundled}
	if b, o, err := src.Read("workflows/a"); err != nil || string(b) != "repo a" || o != OriginRepo {
		t.Fatalf("a = %q %v %v", b, o, err)
	}
	if b, o, err := src.Read("workflows/b"); err != nil || string(b) != "bundled b" || o != OriginBundled {
		t.Fatalf("b = %q %v %v", b, o, err)
	}
	if _, _, err := src.Read("workflows/c"); err == nil {
		t.Fatal("missing file: err = nil")
	}
}

func TestLoadFollowsReferences(t *testing.T) {
	agent := "---\noutcomes:\n  done: ok\n---\nbody\n"
	fsys := fstest.MapFS{
		"workflows/top.yaml": {Data: []byte("version: 1\nstart: a\nnodes:\n  a:\n    type: workflow\n    workflow: workflows/sub\n    next: end\n")},
		"workflows/sub.yaml": {Data: []byte("version: 1\nstart: b\nnodes:\n  b:\n    type: agent\n    role: agents/worker\n    next: end\n")},
		"agents/worker.md":   {Data: []byte(agent)},
	}
	set, errs := Load(Source{Bundled: fsys}, "workflows/top")
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(set.Workflows) != 2 || set.Agents["agents/worker"] == nil {
		t.Fatalf("set = %+v", set)
	}
}

func TestLoadReportsMissingReferenceAtCaller(t *testing.T) {
	fsys := fstest.MapFS{
		"workflows/top.yaml": {Data: []byte("version: 1\nstart: a\nnodes:\n  a:\n    type: agent\n    role: agents/missing\n    next: end\n")},
	}
	_, errs := Load(Source{Bundled: fsys}, "workflows/top")
	if len(errs) != 1 || errs[0].Pos.File != "workflows/top" || errs[0].Pos.Node != "a" {
		t.Fatalf("errs = %v, want one at workflows/top: a", errs)
	}
}

func containsMsg(errs []*Error, want string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), want) {
			return true
		}
	}
	return false
}
