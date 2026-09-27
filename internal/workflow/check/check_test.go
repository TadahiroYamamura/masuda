package check

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
)

// agents used by the fixtures. reader cannot write; writer can.
var fixtureAgents = map[string]string{
	"agents/reader":       "---\ntools: Read, Grep\noutcomes:\n  done: ok\n---\nread\n",
	"agents/writer":       "---\ntools: Read, Edit, Bash\noutcomes:\n  done: ok\n  stuck: cannot\n---\nwrite\n",
	"agents/planner":      "---\ntools: Read\noutputs: [plan]\noutcomes:\n  done: ok\n---\nplan\n",
	"agents/investigator": "---\ntools: Read, Bash\noutputs: [investigation]\noutcomes:\n  done: ok\n---\ninvestigate\n",
	"agents/picker":       "---\ntools: Read\noutputs: [selected-perspectives]\noutcomes:\n  done: ok\n---\npick\n",
	"agents/reviewer":     "---\ntools: Read\noutputs: [findings]\noutcomes:\n  done: ok\n---\nreview\n",
	"agents/synth":        "---\ntools: Read\noutputs: [report]\noutcomes:\n  done: ok\n---\nreport\n",
}

// load builds a set from workflow sources keyed by reference path; root
// is the first key given in order.
func load(t *testing.T, root string, workflows map[string]string) *def.Set {
	t.Helper()
	fsys := fstest.MapFS{}
	for p, body := range fixtureAgents {
		fsys[p+".md"] = &fstest.MapFile{Data: []byte(body)}
	}
	for p, body := range workflows {
		fsys[p+".yaml"] = &fstest.MapFile{Data: []byte(body)}
	}
	set, errs := def.Load(def.Source{Bundled: fsys}, root)
	if len(errs) != 0 {
		t.Fatalf("load errors: %v", errs)
	}
	return set
}

func wf(start string, nodes string) string {
	return "version: 1\nstart: " + start + "\nnodes:\n" + nodes
}

func expectError(t *testing.T, errs []*def.Error, want string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e.Error(), want) {
			return
		}
	}
	t.Fatalf("errors = %v, want one containing %q", errs, want)
}

func expectNone(t *testing.T, errs []*def.Error) {
	t.Helper()
	if len(errs) != 0 {
		t.Fatalf("errors = %v, want none", errs)
	}
}

func TestStructuralRules(t *testing.T) {
	cases := []struct {
		name  string
		nodes string
		want  string
	}{
		{"unrouted outcome", `  a:
    type: agent
    role: agents/writer
    next: end
`, `outcome "stuck" has no destination`},
		{"unknown outcome key", `  a:
    type: agent
    role: agents/reader
    next: {done: end, needs_more: end}
`, `never produces "needs_more"`},
		{"blocked as next key", `  a:
    type: agent
    role: agents/reader
    next: {done: end, blocked: end}
`, "`blocked` is not an outcome"},
		{"reserved end label", `  a:
    type: agent
    role: agents/reader
    next: {done: end, exhausted: end:exhausted}
`, "reserved for the engine and cannot be an end label"},
		{"exhausted on node without max", `  a:
    type: check
    check: test
    next: {done: end, failed: end, exhausted: end}
`, "next.exhausted is unreachable"},
		{"reserved gate", `  a:
    type: approval
    gate: deviation
    target: diff
    next: {approved: end, rejected: end}
`, `gate name "deviation" is reserved`},
		{"from a node that does not pick", `  a:
    type: agent
    role: agents/reader
    next: b
  b:
    type: foreach
    over: perspectives(from=a)
    body: workflows/body
    next: end
`, `does not declare the output "selected-perspectives"`},
		{"export nobody writes", `  a:
    type: discard
    export: [security-notes]
    next: end
`, `export "security-notes"`},
		{"scope step without step input", `  a:
    type: approval
    gate: plan
    target: plan
    next: {approved: b, rejected: end}
  b:
    type: commit
    scope: step
    next: {done: end, rejected: end}
`, "scope: step can only be used"},
		{"with names an input the callee lacks", `  a:
    type: workflow
    workflow: workflows/body
    with: {nope: diff}
    next: end
`, `declares no input "nope"`},
		{"review stage with a writer", `  a:
    type: review
    workflow: workflows/writes
    next: end
`, "must not contain a write-capable agent"},
		{"loop without bound", `  a:
    type: check
    check: test
    next: {done: end, failed: b}
  b:
    type: workflow
    workflow: workflows/body
    next: a
`, "could loop forever"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := load(t, "workflows/top", map[string]string{
				"workflows/top":    wf("a", c.nodes),
				"workflows/body":   "version: 1\ninputs: [perspective]\nstart: r\nnodes:\n  r:\n    type: agent\n    role: agents/reader\n    next: end\n",
				"workflows/writes": wf("w", "  w:\n    type: agent\n    role: agents/writer\n    next: {done: end, stuck: end:stuck}\n"),
			})
			expectError(t, Run(set, Options{}), c.want)
		})
	}
}

func TestCallCycle(t *testing.T) {
	set := load(t, "workflows/a", map[string]string{
		"workflows/a": wf("x", "  x:\n    type: workflow\n    workflow: workflows/b\n    next: end\n"),
		"workflows/b": wf("y", "  y:\n    type: workflow\n    workflow: workflows/a\n    next: end\n"),
	})
	expectError(t, Run(set, Options{}), "workflow calls form a cycle")
}

func TestCheckNamesFromSettings(t *testing.T) {
	set := load(t, "workflows/top", map[string]string{
		"workflows/top": wf("a", "  a:\n    type: check\n    check: lint\n    next: {done: end, failed: end}\n"),
	})
	expectNone(t, Run(set, Options{}))
	expectError(t, Run(set, Options{CheckNames: map[string]bool{"test": true}}), `check "lint" is not declared`)
}

const approveThenWrite = `  approve:
    type: approval
    gate: plan
    target: plan
    next: {approved: work, rejected: end}
  work:
    type: agent
    role: agents/writer
    next: {done: commit, stuck: end:stuck}
  commit:
    type: commit
    scope: plan
    next: {done: publish, rejected: work}
  publish:
    type: publish
    next: end
`

func TestFlowAcceptsApprovedWriteCommitPublish(t *testing.T) {
	set := load(t, "workflows/top", map[string]string{"workflows/top": wf("approve", approveThenWrite)})
	expectNone(t, Run(set, Options{}))
	if RequiresPlan(set) {
		t.Fatal("RequiresPlan() = true for a workflow that approves the plan first")
	}
}

func TestFlowRejectsPublishWithUncommittedChanges(t *testing.T) {
	nodes := strings.Replace(approveThenWrite, "next: {done: commit, stuck: end:stuck}", "next: {done: publish, stuck: end:stuck}", 1)
	set := load(t, "workflows/top", map[string]string{"workflows/top": wf("approve", nodes)})
	expectError(t, Run(set, Options{}), "type: publish can be reached with uncommitted changes")
}

func TestFlowRootThatWritesFirstRequiresPlan(t *testing.T) {
	nodes := `  work:
    type: agent
    role: agents/writer
    next: {done: commit, stuck: end:stuck}
  commit:
    type: commit
    scope: plan
    next: {done: end, rejected: work}
`
	set := load(t, "workflows/top", map[string]string{"workflows/top": wf("work", nodes)})
	expectNone(t, Run(set, Options{}))
	if !RequiresPlan(set) {
		t.Fatal("RequiresPlan() = false for a workflow that writes before any plan approval")
	}
}

func TestFlowWriteAfterReplanNeedsNewApproval(t *testing.T) {
	// A writer reachable after a new plan was written but before it was
	// approved again: the old approval no longer counts.
	nodes := `  approve:
    type: approval
    gate: plan
    target: plan
    next: {approved: replan, rejected: end}
  replan:
    type: agent
    role: agents/planner
    next: work
  work:
    type: agent
    role: agents/writer
    next: {done: commit, stuck: end:stuck}
  commit:
    type: commit
    scope: plan
    next: {done: end, rejected: end}
`
	set := load(t, "workflows/top", map[string]string{"workflows/top": wf("approve", nodes)})
	if !RequiresPlan(set) {
		t.Fatal("RequiresPlan() = false, want true: the writer runs after a new, unapproved plan")
	}
}

func TestFlowCommitWithoutPlan(t *testing.T) {
	set := load(t, "workflows/top", map[string]string{"workflows/top": wf("c", "  c:\n    type: commit\n    scope: plan\n    next: {done: end, rejected: end}\n")})
	expectError(t, Run(set, Options{}), "type: commit needs an approved plan")
}

func TestFlowImplementMustCommitBeforeEnd(t *testing.T) {
	top := `  approve:
    type: approval
    gate: plan
    target: plan
    next: {approved: impl, rejected: end}
  impl:
    type: implement
    workflow: workflows/impl
    next: {done: end, stuck: end:stuck}
`
	impl := "version: 1\ninputs: [plan]\nstart: w\nnodes:\n  w:\n    type: agent\n    role: agents/writer\n    next: {done: end, stuck: end:stuck}\n"
	set := load(t, "workflows/top", map[string]string{"workflows/top": wf("approve", top), "workflows/impl": impl})
	expectError(t, Run(set, Options{}), "type: implement must commit what it changes before it ends")
}

func TestFlowImplementWithZeroStepsIsFine(t *testing.T) {
	// A foreach whose body commits may run zero times; the stage then ends
	// with nothing to commit, which satisfies the contract.
	top := `  planner:
    type: agent
    role: agents/planner
    next: approve
  approve:
    type: approval
    gate: plan
    target: plan
    next: {approved: impl, rejected: end}
  impl:
    type: implement
    workflow: workflows/impl
    next: {done: end, stuck: end:stuck}
`
	impl := "version: 1\ninputs: [plan]\nstart: steps\nnodes:\n  steps:\n    type: foreach\n    over: steps\n    body: workflows/step\n    next: {done: end, stuck: end:stuck}\n"
	step := `version: 1
inputs: [step]
start: w
nodes:
  w:
    type: agent
    role: agents/writer
    next: {done: c, stuck: end:stuck}
  c:
    type: commit
    scope: step
    next: {done: end, rejected: w}
`
	set := load(t, "workflows/top", map[string]string{"workflows/top": wf("planner", top), "workflows/impl": impl, "workflows/step": step})
	expectNone(t, Run(set, Options{}))
}

func TestFlowPlanStageMustWritePlan(t *testing.T) {
	top := "version: 1\ninputs: [instructions]\nstart: inv\nnodes:\n  inv:\n    type: investigate\n    workflow: workflows/inv\n    next: p\n  p:\n    type: plan\n    workflow: workflows/p\n    next: end\n"
	inv := "version: 1\ninputs: [instructions]\nstart: i\nnodes:\n  i:\n    type: agent\n    role: agents/investigator\n    next: end\n"
	p := "version: 1\ninputs: [investigation]\nstart: r\nnodes:\n  r:\n    type: agent\n    role: agents/reader\n    next: end\n"
	set := load(t, "workflows/top", map[string]string{"workflows/top": top, "workflows/inv": inv, "workflows/p": p})
	expectError(t, Run(set, Options{}), `without writing "plan"`)
}

func TestFlowPlanWithoutInvestigationIsMissingInput(t *testing.T) {
	top := "version: 1\ninputs: [instructions]\nstart: p\nnodes:\n  p:\n    type: plan\n    workflow: workflows/p\n    next: end\n"
	p := "version: 1\ninputs: [instructions]\nstart: r\nnodes:\n  r:\n    type: agent\n    role: agents/planner\n    next: end\n"
	set := load(t, "workflows/top", map[string]string{"workflows/top": top, "workflows/p": p})
	expectError(t, Run(set, Options{}), `needs "investigation"`)
}

func TestFlowInputOnlyOnSomePathsIsMissing(t *testing.T) {
	// report is written only when the check passes, so the discard's
	// caller-side input is not available on the failed path.
	top := `  chk:
    type: check
    check: test
    next: {done: s, failed: use}
  s:
    type: agent
    role: agents/synth
    next: use
  use:
    type: workflow
    workflow: workflows/needs-report
    next: end
`
	needs := "version: 1\ninputs: [report]\nstart: r\nnodes:\n  r:\n    type: agent\n    role: agents/reader\n    next: end\n"
	set := load(t, "workflows/top", map[string]string{"workflows/top": wf("chk", top), "workflows/needs-report": needs})
	expectError(t, Run(set, Options{}), `needs the input "report"`)
}

func TestFlowWithBindsStepDiff(t *testing.T) {
	top := "version: 1\ninputs: [step]\nstart: f\nnodes:\n  f:\n    type: foreach\n    over: perspectives\n    body: workflows/pr\n    with: {diff: step-diff}\n    next: end\n"
	pr := "version: 1\ninputs: [perspective, diff]\nstart: r\nnodes:\n  r:\n    type: agent\n    role: agents/reviewer\n    next: end\n"
	set := load(t, "workflows/top", map[string]string{"workflows/top": top, "workflows/pr": pr})
	expectNone(t, Run(set, Options{}))
}

func TestFlowImplementResumedWithCallersChangesIsFine(t *testing.T) {
	// stuck leaves the step's changes uncommitted and goes back to the plan
	// gate; approving resumes implement with those changes. The contract
	// judges the content from a clean entry, so this is not a violation.
	top := `  planner:
    type: agent
    role: agents/planner
    next: approve
  approve:
    type: approval
    gate: plan
    target: plan
    next: {approved: impl, rejected: planner}
  impl:
    type: implement
    workflow: workflows/impl
    next: {done: commit, stuck: approve}
  commit:
    type: commit
    scope: plan
    next: {done: publish, rejected: end}
  publish:
    type: publish
    next: end
`
	impl := "version: 1\ninputs: [plan]\nstart: steps\nnodes:\n  steps:\n    type: foreach\n    over: steps\n    body: workflows/step\n    next: {done: end, stuck: end:stuck}\n"
	step := `version: 1
inputs: [step]
start: w
nodes:
  w:
    type: agent
    role: agents/writer
    next: {done: c, stuck: end:stuck}
  c:
    type: commit
    scope: step
    next: {done: end, rejected: w}
`
	set := load(t, "workflows/top", map[string]string{"workflows/top": wf("planner", top), "workflows/impl": impl, "workflows/step": step})
	expectNone(t, Run(set, Options{}))
}
