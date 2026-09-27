package check

import (
	"fmt"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
)

// state is what the flow checks track besides the current node. The space
// is node × 4, and a (node, state) pair is visited at most once, so the
// search ends even when the workflow loops (ADR-0081).
type state struct {
	dirty bool // uncommitted changes may exist
	plan  bool // an approved plan exists
}

func (s state) String() string {
	return fmt.Sprintf("{uncommitted changes: %v, approved plan: %v}", s.dirty, s.plan)
}

// violation is a rule broken on some path, with that path for the message.
type violation struct {
	pos  def.Pos
	msg  string
	path []string
}

// summary is what a workflow does when entered in one state: the states it
// can leave in, per outcome, and the rules broken on the way.
type summary struct {
	exits      map[string]map[state]bool
	violations []violation
}

type flowKey struct {
	path  string
	entry state
}

type flow struct {
	c         *checker
	summaries map[flowKey]*summary
}

// flow runs the state-space and data-availability checks from the root.
// It runs only on a set that passed the structural checks, so references
// resolve and every outcome has a destination.
func (c *checker) flow() {
	f := &flow{c: c, summaries: map[flowKey]*summary{}}
	root := c.set.Root
	s := f.summarize(root, state{})
	// A root that writes before any plan approval is not wrong in itself:
	// it is a workflow meant to be started on a workspace that already has
	// an approved plan (e.g. build-step run standalone). `masuda run`
	// enforces that precondition, so check it again with a plan.
	if hasNoPlanWrite(s.violations) {
		s = f.summarize(root, state{plan: true})
	}
	seen := map[string]bool{}
	for _, v := range s.violations {
		key := v.pos.String() + v.msg
		if seen[key] {
			continue
		}
		seen[key] = true
		msg := v.msg
		if len(v.path) > 1 {
			msg += " (path: " + strings.Join(v.path, " → ") + ")"
		}
		c.errs = append(c.errs, &def.Error{Pos: v.pos, Msg: msg})
	}
	f.availability()
}

// RequiresPlan reports whether a workflow can only start on a workspace
// with an approved plan: it reaches a write-capable agent before any
// target: plan approval. `masuda run` checks this before running a node.
func RequiresPlan(set *def.Set) bool {
	c := &checker{set: set}
	f := &flow{c: c, summaries: map[flowKey]*summary{}}
	return hasNoPlanWrite(f.summarize(set.Root, state{}).violations)
}

const msgNoPlanWrite = "a write-capable agent runs without an approved plan"

func hasNoPlanWrite(vs []violation) bool {
	for _, v := range vs {
		if strings.HasPrefix(v.msg, msgNoPlanWrite) {
			return true
		}
	}
	return false
}

type visit struct {
	node string
	st   state
}

func (f *flow) summarize(path string, entry state) *summary {
	key := flowKey{path, entry}
	if s, ok := f.summaries[key]; ok {
		return s
	}
	s := &summary{exits: map[string]map[state]bool{}}
	f.summaries[key] = s
	w := f.c.set.Workflows[path]
	if w == nil {
		return s
	}

	parent := map[visit]visit{}
	trail := func(v visit) []string {
		var out []string
		for {
			out = append(out, v.node)
			p, ok := parent[v]
			if !ok {
				break
			}
			v = p
		}
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		return out
	}
	violate := func(v visit, pos def.Pos, format string, args ...any) {
		s.violations = append(s.violations, violation{pos: pos, msg: fmt.Sprintf(format, args...), path: trail(v)})
	}

	start := visit{w.Start, entry}
	seen := map[visit]bool{start: true}
	queue := []visit{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		n := w.Nodes[cur.node]
		pos := def.Pos{File: path, Node: cur.node}
		for outcome, next := range f.step(n, cur.st, func(format string, args ...any) { violate(cur, pos, format, args...) }, s) {
			t, routed := n.Next[outcome]
			if !routed {
				continue // unrouted exhausted: the run stops as blocked
			}
			for st := range next {
				if t.End {
					o := t.Outcome()
					if s.exits[o] == nil {
						s.exits[o] = map[state]bool{}
					}
					s.exits[o][st] = true
					continue
				}
				nv := visit{t.Node, st}
				if !seen[nv] {
					seen[nv] = true
					parent[nv] = cur
					queue = append(queue, nv)
				}
			}
		}
	}
	return s
}

// step returns, per outcome, the states a node can leave in when entered
// in st, and reports rules the node itself breaks.
func (f *flow) step(n *def.Node, st state, violate func(string, ...any), s *summary) map[string]map[state]bool {
	out := map[string]map[state]bool{}
	add := func(outcome string, sts ...state) {
		if out[outcome] == nil {
			out[outcome] = map[state]bool{}
		}
		for _, x := range sts {
			out[outcome][x] = true
		}
	}
	// Exceeding the entry limit means the node does not run, so exhausted
	// leaves the state as it was.
	if n.EffectiveMax() > 0 {
		add(def.OutcomeExhausted, st)
	}
	switch n.Type {
	case def.TypeAgent:
		a := f.c.set.Agents[n.Role]
		after := st
		if a != nil && a.WriteCapable() {
			if !st.plan {
				violate("%s: %s (target: plan approval must come first)", msgNoPlanWrite, n.Role)
			}
			after.dirty = true
		}
		if a != nil && a.DeclaresOutput(def.DataPlan) {
			// A new plan voids the approval of the old one.
			after.plan = false
		}
		for o := range f.c.outcomes(n) {
			add(o, after)
		}
	case def.TypeApproval:
		approved := st
		if n.GateTarget == "plan" {
			approved.plan = true
		}
		add(def.OutcomeApproved, approved)
		add(def.OutcomeRejected, st)
	case def.TypeCheck:
		add(def.OutcomeDone, st)
		add(def.OutcomeFailed, st)
	case def.TypeCommit:
		if !st.plan {
			violate("type: commit needs an approved plan to decide what to commit")
		}
		clean := st
		clean.dirty = false
		add(def.OutcomeDone, clean)
		add(def.OutcomeRejected, st)
	case def.TypePublish:
		if st.dirty {
			violate("type: publish can be reached with uncommitted changes; put a type: commit before it")
		}
		add(def.OutcomeDone, st)
	case def.TypeDiscard:
		add(def.OutcomeDone, st)
	case def.TypeForeach:
		f.foreach(n, st, add, s)
	default: // workflow and stages
		contract, isStage := def.StageContracts[n.Type]
		if isStage && contract.RequiresPlan && !st.plan {
			violate("type: %s must start with an approved plan", n.Type)
		}
		sub := f.summarize(n.Workflow, st)
		f.inherit(n, sub, s)
		for o, sts := range sub.exits {
			for x := range sts {
				add(o, x)
			}
		}
		if isStage && contract.RequiresPlan {
			// The contract judges the content, so it is checked from the
			// entry state the contract promises. Changes the caller brings
			// in (e.g. a stuck step resumed after approval) are not the
			// content's to commit; the caller's own commit-before-publish
			// check covers them.
			promised := f.summarize(n.Workflow, state{plan: true})
			for x := range promised.exits[def.OutcomeDone] {
				if x.dirty {
					violate("%s can end with `end` while changes are uncommitted; type: %s must commit what it changes before it ends", n.Workflow, n.Type)
					break
				}
			}
		}
	}
	return out
}

// foreach runs the body zero or more times. Every state the loop head can
// reach is a state the foreach can finish in, because the item set may be
// exhausted at any point.
func (f *flow) foreach(n *def.Node, st state, add func(string, ...state), s *summary) {
	head := map[state]bool{st: true}
	queue := []state{st}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		sub := f.summarize(n.Body, cur)
		f.inherit(n, sub, s)
		for o, sts := range sub.exits {
			for x := range sts {
				if o != def.OutcomeDone && n.OnIncomplete != "continue" {
					add(o, x)
					continue
				}
				if !head[x] {
					head[x] = true
					queue = append(queue, x)
				}
			}
		}
	}
	for x := range head {
		add(def.OutcomeDone, x)
		if n.OnIncomplete == "continue" {
			add(def.OutcomeIncomplete, x)
		}
	}
}

// inherit carries a callee's violations up, noting where it was called.
func (f *flow) inherit(n *def.Node, sub *summary, s *summary) {
	for _, v := range sub.violations {
		v.msg = fmt.Sprintf("%s (reached through node %s)", v.msg, n.ID)
		s.violations = append(s.violations, v)
	}
}

// availability checks that every input a workflow declares is available
// on every path that reaches it, and that stages whose contract requires
// outputs write them on every path to `end`. It is a must-analysis: at
// merges, only data available on all incoming paths survives.
func (f *flow) availability() {
	a := &avail{c: f.c}
	root := f.c.set.Workflows[f.c.set.Root]
	if root == nil {
		return
	}
	entry := map[string]bool{}
	for _, in := range root.Inputs {
		entry[in] = true
	}
	a.run(root, entry, true)
}

type avail struct {
	c *checker
}

// run analyses w entered with the given data available and returns what
// is available at `end` (outcome done). With report set, it also reports
// missing inputs, in w and in everything it calls.
func (a *avail) run(w *def.Workflow, entry map[string]bool, report bool) map[string]bool {
	in := map[string]map[string]bool{}
	in[w.Start] = copySet(entry)
	var atDone map[string]bool
	changed := true
	for changed {
		changed = false
		for _, id := range w.Order {
			have := in[id]
			if have == nil {
				continue
			}
			n := w.Nodes[id]
			for o, t := range n.Next {
				after := a.effect(w, n, o, have, false)
				if t.End {
					if t.Outcome() == def.OutcomeDone {
						atDone = meet(atDone, after)
					}
					continue
				}
				merged := meet(in[t.Node], after)
				if in[t.Node] == nil || len(merged) != len(in[t.Node]) {
					in[t.Node] = merged
					changed = true
				}
			}
		}
	}
	if report {
		for _, id := range w.Order {
			if have := in[id]; have != nil {
				// One outcome is enough: the inputs a node needs do not
				// depend on how it ends.
				n := w.Nodes[id]
				if len(n.NextOrder) > 0 {
					a.effect(w, n, n.NextOrder[0], have, true)
				}
			}
		}
	}
	if atDone == nil {
		atDone = map[string]bool{}
	}
	return atDone
}

// effect returns the data available after node n finishes with outcome o,
// given have before it. With report set, it also reports missing inputs.
func (a *avail) effect(w *def.Workflow, n *def.Node, o string, have map[string]bool, report bool) map[string]bool {
	after := copySet(have)
	fail := func(format string, args ...any) {
		if report {
			a.c.fail(w.Path, n.ID, format, args...)
		}
	}
	available := func(name string, extra map[string]bool) bool {
		return have[name] || def.EngineComputed[name] || extra[name]
	}
	switch n.Type {
	case def.TypeAgent:
		if o == def.OutcomeDone {
			if ag := a.c.set.Agents[n.Role]; ag != nil {
				for _, out := range ag.Outputs {
					after[out] = true
				}
			}
		}
	case def.TypeForeach:
		body := a.c.set.Workflows[n.Body]
		if body == nil {
			break
		}
		item := map[string]bool{def.OverItemInput[n.Over]: true}
		entry := a.bindInputs(body, n, have, item, available, fail)
		if report {
			a.run(body, entry, true)
		}
	case def.TypeWorkflow, def.TypeInvestigate, def.TypePlan, def.TypeImplement, def.TypeReview:
		callee := a.c.set.Workflows[n.Workflow]
		if callee == nil {
			break
		}
		contract, isStage := def.StageContracts[n.Type]
		provided := map[string]bool{}
		if isStage {
			for _, in := range contract.Inputs {
				if report && !available(in, nil) {
					fail("type: %s needs %q, which is not available on every path to this node", n.Type, in)
				}
				provided[in] = true
			}
		}
		entry := a.bindInputs(callee, n, have, provided, available, fail)
		done := a.run(callee, entry, report)
		if isStage && contract.MustWrite && report {
			for _, out := range contract.Outputs {
				if !done[out] {
					fail("%s can end with `end` without writing %q, which type: %s must produce", n.Workflow, out, n.Type)
				}
			}
		}
		if o == def.OutcomeDone {
			for d := range done {
				after[d] = true
			}
			if isStage {
				for _, out := range contract.Outputs {
					after[out] = true
				}
			}
		}
	}
	return after
}

// bindInputs resolves each input the callee declares to a data name (its
// own name unless `with:` binds it elsewhere), reports those not
// available, and returns the callee's entry set in its own input names.
func (a *avail) bindInputs(callee *def.Workflow, n *def.Node, have, extra map[string]bool, available func(string, map[string]bool) bool, fail func(string, ...any)) map[string]bool {
	entry := map[string]bool{}
	for _, in := range callee.Inputs {
		src := in
		if b, ok := n.With[in]; ok {
			src = b
		}
		if !available(src, extra) {
			if src == in {
				fail("%s needs the input %q, which is not available on every path to this node", callee.Path, in)
			} else {
				fail("%s needs the input %q (bound to %q), which is not available on every path to this node", callee.Path, in, src)
			}
		}
		entry[in] = true
	}
	return entry
}

// meet intersects two availability sets; nil stands for "not reached yet"
// and is the identity.
func meet(a, b map[string]bool) map[string]bool {
	if a == nil {
		return copySet(b)
	}
	out := map[string]bool{}
	for k := range a {
		if b[k] {
			out[k] = true
		}
	}
	return out
}

func copySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		if v {
			out[k] = true
		}
	}
	return out
}
