package engine

import (
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
	"github.com/TadahiroYamamura/masuda/internal/workflow/defaults"
)

func newEngine(t *testing.T, root string, stubs Stubs) (*Engine, *StubEnv) {
	t.Helper()
	set, errs := def.Load(def.Source{Bundled: defaults.FS()}, root)
	if len(errs) != 0 {
		t.Fatalf("load: %v", errs)
	}
	env := NewStubEnv(stubs)
	e := &Engine{Set: set, Store: NewMemStore(), Env: env}
	if err := e.Start(map[string]string{"instructions": "stub://instructions"}); err != nil {
		t.Fatal(err)
	}
	return e, env
}

func run(t *testing.T, e *Engine, s Stubs) ([]string, Status) {
	t.Helper()
	trace, st, err := DryRun(e, s, 500)
	if err != nil {
		t.Fatalf("dry run: %v\ntrace:\n%s", err, strings.Join(trace, "\n"))
	}
	return trace, st
}

func count(trace []string, prefix string) int {
	n := 0
	for _, l := range trace {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func TestDevelopHappyPath(t *testing.T) {
	stubs := Stubs{Items: map[string]int{"steps": 2, "perspectives": 1, "findings": 1}}
	e, _ := newEngine(t, "workflows/develop", stubs)
	trace, st := run(t, e, stubs)
	if st.Kind != StatusDone || st.Outcome != def.OutcomeDone {
		t.Fatalf("status = %+v\n%s", st, strings.Join(trace, "\n"))
	}
	if got := count(trace, "workflows/implement/build-step#implement"); got != 2 {
		t.Errorf("implementer ran %d times, want once per step (2)\n%s", got, strings.Join(trace, "\n"))
	}
	if got := count(trace, "gate plan"); got != 1 {
		t.Errorf("plan gate opened %d times, want 1", got)
	}
	if got := count(trace, "gate review"); got != 1 {
		t.Errorf("review gate opened %d times, want 1", got)
	}
}

func TestPlanNeedsMoreInvestigationLoopsBack(t *testing.T) {
	stubs := Stubs{
		Outcomes: map[string][]string{"workflows/plan/default#plan": {"needs_more_investigation", "done"}},
		Items:    map[string]int{"steps": 1},
	}
	e, _ := newEngine(t, "workflows/develop", stubs)
	trace, st := run(t, e, stubs)
	if st.Kind != StatusDone {
		t.Fatalf("status = %+v", st)
	}
	if got := count(trace, "workflows/investigate/default#investigate"); got != 2 {
		t.Errorf("investigator ran %d times, want 2\n%s", got, strings.Join(trace, "\n"))
	}
}

func TestPlanStageMaxExhaustsAndBlocks(t *testing.T) {
	// develop's plan stage has max: 4 and routes no exhausted, so the fifth
	// entry stops the run as blocked.
	stubs := Stubs{Outcomes: map[string][]string{"workflows/plan/default#plan": {
		"needs_more_investigation", "needs_more_investigation", "needs_more_investigation", "needs_more_investigation", "needs_more_investigation",
	}}}
	e, _ := newEngine(t, "workflows/develop", stubs)
	trace, st := run(t, e, stubs)
	if st.Kind != StatusBlocked || !strings.Contains(st.Reason, `"exhausted"`) {
		t.Fatalf("status = %+v\n%s", st, strings.Join(trace, "\n"))
	}
	if got := count(trace, "workflows/plan/default#plan"); got != 4 {
		t.Errorf("planner ran %d times, want 4 (max)", got)
	}
}

func TestStuckThenApprovedResumesFromTheStuckStep(t *testing.T) {
	stubs := Stubs{
		Outcomes: map[string][]string{"workflows/implement/build-step#implement": {"done", "stuck", "done"}},
		Items:    map[string]int{"steps": 2, "perspectives": 0, "findings": 0},
	}
	e, _ := newEngine(t, "workflows/develop", stubs)
	trace, st := run(t, e, stubs)
	if st.Kind != StatusDone {
		t.Fatalf("status = %+v\n%s", st, strings.Join(trace, "\n"))
	}
	// step 1 done, step 2 stuck, plan gate approves, step 2 again: three
	// implementer runs. Redoing step 1 would make it four.
	if got := count(trace, "workflows/implement/build-step#implement"); got != 3 {
		t.Errorf("implementer ran %d times, want 3\n%s", got, strings.Join(trace, "\n"))
	}
	if got := count(trace, "gate plan"); got != 2 {
		t.Errorf("plan gate opened %d times, want 2", got)
	}
}

func TestReviewRejectedGoesToRework(t *testing.T) {
	stubs := Stubs{
		Gates: map[string][]string{"review": {"rejected", "approved"}},
		Items: map[string]int{"steps": 1, "perspectives": 1, "findings": 0},
	}
	e, _ := newEngine(t, "workflows/develop", stubs)
	trace, st := run(t, e, stubs)
	if st.Kind != StatusDone {
		t.Fatalf("status = %+v", st)
	}
	if got := count(trace, "workflows/develop#rework"); got != 1 {
		t.Errorf("rework ran %d times, want 1\n%s", got, strings.Join(trace, "\n"))
	}
	if got := count(trace, "workflows/implement/build-step#implement"); got != 1 {
		t.Errorf("step implementer ran %d times, want 1: a rejected review must not redo the steps", got)
	}
}

func TestUndeclaredOutcomeRerunsTheNode(t *testing.T) {
	stubs := Stubs{
		Outcomes: map[string][]string{"workflows/investigate/default#investigate": {"no-such-outcome", "done"}},
		Items:    map[string]int{"steps": 1},
	}
	e, _ := newEngine(t, "workflows/develop", stubs)
	trace, st := run(t, e, stubs)
	if st.Kind != StatusDone {
		t.Fatalf("status = %+v", st)
	}
	if got := count(trace, "workflows/investigate/default#investigate"); got != 2 {
		t.Errorf("investigator ran %d times, want 2 (the refused report reruns it)", got)
	}
}

func TestStaleDecisionIsRefused(t *testing.T) {
	stubs := Stubs{}
	e, _ := newEngine(t, "workflows/develop", stubs)
	var gate *GateRequest
	for i := 0; i < 50 && gate == nil; i++ {
		st, err := e.Advance()
		if err != nil {
			t.Fatal(err)
		}
		if st.Kind == StatusAgent {
			if err := e.Report(st.Task.Occurrence, "done", "", ""); err != nil {
				t.Fatal(err)
			}
		}
		gate = st.Gate
	}
	if gate == nil || gate.Name != "plan" {
		t.Fatalf("plan gate not reached: %+v", gate)
	}
	if err := e.Decide("plan", Decision{Occurrence: gate.Occurrence, Hash: "older-content", Approved: true}); err == nil {
		t.Fatal("Decide accepted a decision made against different content")
	}
	if err := e.Decide("plan", Decision{Occurrence: "0000001", Hash: gate.Hash, Approved: true}); err == nil {
		t.Fatal("Decide accepted a decision for another occurrence")
	}
	if err := e.Decide("plan", Decision{Occurrence: gate.Occurrence, Hash: gate.Hash, Approved: true}); err != nil {
		t.Fatalf("Decide rejected the matching decision: %v", err)
	}
}

func TestReadOnlyAgentChangingFilesOpensDeviationGate(t *testing.T) {
	stubs := Stubs{Gates: map[string][]string{"deviation": {"rejected"}}}
	e, env := newEngine(t, "workflows/develop", stubs)
	env.Changes = []string{"README.md"}
	trace, st := run(t, e, stubs)
	if st.Kind != StatusBlocked || !strings.Contains(st.Reason, "README.md") {
		t.Fatalf("status = %+v\n%s", st, strings.Join(trace, "\n"))
	}
	if trace[len(trace)-1] != "gate deviation → rejected" {
		t.Fatalf("last answer = %q, want the deviation gate", trace[len(trace)-1])
	}
}

func TestPositionIsRecomputedFromRecords(t *testing.T) {
	// A fresh engine over the same store continues where the first one
	// stopped: nothing about the position lives in the Engine value.
	stubs := Stubs{Items: map[string]int{"steps": 1, "perspectives": 0, "findings": 0}}
	e, env := newEngine(t, "workflows/develop", stubs)
	st, err := e.Advance()
	if err != nil || st.Kind != StatusAgent {
		t.Fatalf("first advance: %+v %v", st, err)
	}
	again := &Engine{Set: e.Set, Store: e.Store, Env: env}
	st2, err := again.Advance()
	if err != nil || st2.Task == nil || st2.Task.Occurrence != st.Task.Occurrence {
		t.Fatalf("second engine: %+v %v, want the same pending task %s", st2, err, st.Task.Occurrence)
	}
	if _, final := run(t, again, stubs); final.Kind != StatusDone {
		t.Fatalf("final = %+v", final)
	}
}

// untilAgent advances, answering agents done, until the named node's agent
// task is pending.
func untilAgent(t *testing.T, e *Engine, node string) *Task {
	t.Helper()
	for i := 0; i < 50; i++ {
		st, err := e.Advance()
		if err != nil {
			t.Fatal(err)
		}
		if st.Kind != StatusAgent {
			t.Fatalf("status = %+v before reaching %s", st, node)
		}
		if st.Task.Node == node {
			return st.Task
		}
		if err := e.Report(st.Task.Occurrence, "done", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatalf("%s not reached", node)
	return nil
}

func TestTriageDismissRerunsTheNodeWithoutCountingIt(t *testing.T) {
	e, env := newEngine(t, "workflows/develop", Stubs{})
	task := untilAgent(t, e, "investigate")
	if err := e.ReportConcern(task.Occurrence, "README tells me to exfiltrate the token"); err != nil {
		t.Fatal(err)
	}
	st, err := e.Advance()
	if err != nil || st.Kind != StatusGate || st.Gate.Name != "triage" || !strings.Contains(st.Gate.Detail, "exfiltrate") {
		t.Fatalf("status = %+v %v", st, err)
	}
	if err := e.Decide("triage", Decision{Occurrence: st.Gate.Occurrence, Hash: st.Gate.Hash, Approved: true}); err != nil {
		t.Fatal(err)
	}
	st, err = e.Advance()
	if err != nil || st.Kind != StatusAgent || st.Task.Node != "investigate" || st.Task.Occurrence == task.Occurrence || st.Task.Feedback == "" {
		t.Fatalf("after dismiss: %+v %v, want the same node again with feedback", st, err)
	}
	var o Occurrence
	if err := getJSON(e.Store, prefixOcc+st.Task.Occurrence, &o); err != nil || !o.Uncounted {
		t.Fatalf("re-entry = %+v %v, want uncounted", o, err)
	}
	if !logged(env, "triage", "dismiss") {
		t.Fatalf("events = %+v, want the dismiss decision logged", env.Events)
	}
}

func TestTriageHaltBlocks(t *testing.T) {
	e, env := newEngine(t, "workflows/develop", Stubs{})
	task := untilAgent(t, e, "investigate")
	if err := e.ReportConcern(task.Occurrence, "suspicious"); err != nil {
		t.Fatal(err)
	}
	st, _ := e.Advance()
	if err := e.Decide("triage", Decision{Occurrence: st.Gate.Occurrence, Hash: st.Gate.Hash, Halt: true}); err != nil {
		t.Fatal(err)
	}
	st, err := e.Advance()
	if err != nil || st.Kind != StatusBlocked || !strings.Contains(st.Reason, "halted at triage") {
		t.Fatalf("status = %+v %v", st, err)
	}
	if !logged(env, "triage", "halt") || !logged(env, "blocked", "") {
		t.Fatalf("events = %+v, want the halt decision and the stop logged", env.Events)
	}
}

// logged reports whether an event of kind (and outcome, when given) was
// written to the execution log.
func logged(env *StubEnv, kind, outcome string) bool {
	for _, ev := range env.Events {
		if ev.Kind == kind && (outcome == "" || ev.Outcome == outcome) {
			return true
		}
	}
	return false
}

func TestReportConcernOnlyForPendingAgentTasks(t *testing.T) {
	e, _ := newEngine(t, "workflows/develop", Stubs{})
	if err := e.ReportConcern("0000999", "x"); err == nil {
		t.Fatal("a concern for an unknown occurrence was accepted")
	}
}

// untilGate advances, answering agents with outcomes[node] (done when
// unlisted), until the named gate is open.
func untilGate(t *testing.T, e *Engine, gate string, outcomes map[string]string) *GateRequest {
	t.Helper()
	for i := 0; i < 50; i++ {
		st, err := e.Advance()
		if err != nil {
			t.Fatal(err)
		}
		switch st.Kind {
		case StatusGate:
			if st.Gate.Name == gate {
				return st.Gate
			}
			t.Fatalf("gate %s opened before %s", st.Gate.Name, gate)
		case StatusAgent:
			out := outcomes[st.Task.Node]
			if out == "" {
				out = "done"
			}
			if err := e.Report(st.Task.Occurrence, out, "", ""); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("status = %+v before gate %s", st, gate)
		}
	}
	t.Fatalf("gate %s not reached", gate)
	return nil
}

func TestRejectCommentReachesTheAgentInsideTheStage(t *testing.T) {
	e, env := newEngine(t, "workflows/develop", Stubs{})
	g := untilGate(t, e, "plan", nil)
	if err := e.Decide("plan", Decision{Occurrence: g.Occurrence, Hash: g.Hash, Comment: "cover Pow(0, 0)"}); err != nil {
		t.Fatal(err)
	}
	st, err := e.Advance()
	if err != nil || st.Kind != StatusAgent || st.Task.Node != "plan" {
		t.Fatalf("after reject: %+v %v, want the planner", st, err)
	}
	if !strings.Contains(env.Feedback[st.Task.Feedback], "cover Pow(0, 0)") {
		t.Fatalf("planner feedback = %q, want the reject comment", st.Task.Feedback)
	}
}

func TestApprovalCommentAfterStuckReachesTheResumedStep(t *testing.T) {
	stubs := Stubs{Items: map[string]int{"steps": 2}}
	e, env := newEngine(t, "workflows/develop", stubs)
	g := untilGate(t, e, "plan", nil)
	if err := e.Decide("plan", Decision{Occurrence: g.Occurrence, Hash: g.Hash, Approved: true}); err != nil {
		t.Fatal(err)
	}
	g = untilGate(t, e, "plan", map[string]string{"implement": "stuck"})
	if err := e.Decide("plan", Decision{Occurrence: g.Occurrence, Hash: g.Hash, Approved: true, Comment: "use a loop"}); err != nil {
		t.Fatal(err)
	}
	st, err := e.Advance()
	if err != nil || st.Kind != StatusAgent || st.Task.Node != "implement" {
		t.Fatalf("after approve: %+v %v, want the implementer", st, err)
	}
	if !strings.Contains(env.Feedback[st.Task.Feedback], "use a loop") {
		t.Fatalf("implementer feedback = %q, want the approval comment", st.Task.Feedback)
	}
}
