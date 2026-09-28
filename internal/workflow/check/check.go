// Package check runs the load-time checks on a def.Set: the rules that need
// to see a workflow together with the workflows and agents it reaches
// (ADR-0065, ADR-0067, ADR-0081, ADR-0082). The same code runs at daemon
// start and behind `masuda workflow check` (ADR-0068).
package check

import (
	"fmt"
	"sort"

	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
)

// Options carries what the checks need from outside the definitions.
type Options struct {
	// CheckNames are the names declared under settings.json `checks`.
	// Nil skips the check-name rule, e.g. when no settings are at hand.
	CheckNames map[string]bool
}

// Run returns every problem found. It never stops at the first one.
func Run(set *def.Set, opts Options) []*def.Error {
	c := &checker{set: set, opts: opts}
	c.callCycles()
	for _, path := range set.SortedWorkflows() {
		c.workflow(set.Workflows[path])
	}
	if len(c.errs) == 0 {
		c.flow()
	}
	return dedupe(c.errs)
}

// dedupe drops repeats: a callee analysed from several call sites reports
// the same problem more than once.
func dedupe(errs []*def.Error) []*def.Error {
	seen := map[string]bool{}
	var out []*def.Error
	for _, e := range errs {
		if k := e.Error(); !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

type checker struct {
	set  *def.Set
	opts Options
	errs []*def.Error
}

func (c *checker) fail(file, node, format string, args ...any) {
	c.errs = append(c.errs, &def.Error{Pos: def.Pos{File: file, Node: node}, Msg: fmt.Sprintf(format, args...)})
}

// endOutcomes returns the outcomes a workflow can finish with: `done` for
// each `end`, the label for each `end:<label>`.
func (c *checker) endOutcomes(path string) map[string]bool {
	out := map[string]bool{}
	w := c.set.Workflows[path]
	if w == nil {
		return out
	}
	for _, n := range w.Nodes {
		for _, t := range n.Next {
			if t.End {
				out[t.Outcome()] = true
			}
		}
	}
	return out
}

// Outcomes returns the finite set of outcomes a node can produce
// (ADR-0065). It does not include exhausted; see mayExhaust.
func (c *checker) outcomes(n *def.Node) map[string]bool {
	set := func(names ...string) map[string]bool {
		m := map[string]bool{}
		for _, s := range names {
			m[s] = true
		}
		return m
	}
	switch n.Type {
	case def.TypeAgent:
		out := map[string]bool{}
		if a := c.set.Agents[n.Role]; a != nil {
			for o := range a.Outcomes {
				out[o] = true
			}
		}
		return out
	case def.TypeApproval:
		return set(def.OutcomeApproved, def.OutcomeRejected)
	case def.TypeCheck:
		return set(def.OutcomeDone, def.OutcomeFailed)
	case def.TypeCommit:
		return set(def.OutcomeDone, def.OutcomeRejected)
	case def.TypePublish, def.TypeDiscard:
		return set(def.OutcomeDone)
	case def.TypeForeach:
		if n.OnIncomplete == "continue" {
			return set(def.OutcomeDone, def.OutcomeIncomplete)
		}
		out := c.endOutcomes(n.Body)
		out[def.OutcomeDone] = true
		return out
	default: // workflow and stages
		return c.endOutcomes(n.Workflow)
	}
}

func (c *checker) mayExhaust(n *def.Node) bool { return n.EffectiveMax() > 0 }

func (c *checker) workflow(w *def.Workflow) {
	declaresInput := map[string]bool{}
	for _, in := range w.Inputs {
		declaresInput[in] = true
	}
	for _, id := range w.Order {
		n := w.Nodes[id]
		fail := func(format string, args ...any) { c.fail(w.Path, id, format, args...) }

		outs := c.outcomes(n)
		for _, o := range n.NextOrder {
			switch {
			case o == def.OutcomeBlocked:
				fail("`blocked` is not an outcome and cannot be routed; it means the run stops")
			case o == def.OutcomeExhausted && !c.mayExhaust(n):
				fail("next.exhausted is unreachable: this node has no max")
			case o != def.OutcomeExhausted && !outs[o]:
				fail("next.%s: this node never produces %q (it can produce %s)", o, o, list(outs))
			}
			if t := n.Next[o]; t.End && def.ReservedOutcomes[t.Label] {
				fail("next.%s: %q is reserved for the engine and cannot be an end label", o, t.Label)
			}
		}
		for _, o := range sorted(outs) {
			if _, ok := n.Next[o]; !ok {
				fail("outcome %q has no destination in next", o)
			}
		}

		switch n.Type {
		case def.TypeAgent:
			if a := c.set.Agents[n.Role]; a != nil {
				for _, out := range a.Outputs {
					if def.EngineComputed[out] || out == def.DataInstructions {
						fail("agent %s declares output %q, which only the engine provides", n.Role, out)
					}
				}
			}
		case def.TypeApproval:
			if def.ReservedGates[n.Gate] {
				fail("gate name %q is reserved for the engine", n.Gate)
			}
		case def.TypeCheck:
			if c.opts.CheckNames != nil && !c.opts.CheckNames[n.Check] {
				fail(`check %q is not declared under checks in .masuda/settings.json; add it, e.g. "checks": {%q: {"command": "<command>"}}`, n.Check, n.Check)
			}
		case def.TypeCommit:
			if n.Scope == "step" && !declaresInput["step"] {
				fail("scope: step can only be used in a workflow that declares the input `step`")
			}
		case def.TypeForeach:
			if n.OverFrom != "" {
				src := w.Nodes[n.OverFrom]
				switch {
				case src == nil:
					fail("over: perspectives(from=%s): no such node in this workflow", n.OverFrom)
				case src.Type != def.TypeAgent:
					fail("over: perspectives(from=%s) must name an agent node", n.OverFrom)
				default:
					if a := c.set.Agents[src.Role]; a != nil && !a.DeclaresOutput(def.DataSelectedPerspectives) {
						fail("over: perspectives(from=%s): agent %s does not declare the output %q", n.OverFrom, src.Role, def.DataSelectedPerspectives)
					}
				}
			}
		case def.TypePublish, def.TypeDiscard:
			for _, name := range n.Export {
				if name != def.DataFindings && !c.someAgentOutputs(name) {
					fail("export %q: no agent in this workflow set declares it as an output", name)
				}
			}
		}

		if callee := n.Callee(); callee != "" {
			if cw := c.set.Workflows[callee]; cw != nil {
				calleeInputs := map[string]bool{}
				for _, in := range cw.Inputs {
					calleeInputs[in] = true
				}
				for k := range n.With {
					if !calleeInputs[k] {
						fail("with.%s: %s declares no input %q", k, callee, k)
					}
				}
				if n.Type.IsStage() && def.StageContracts[n.Type].ReadOnly {
					for _, a := range c.writeAgentsIn(callee) {
						fail("%s is the content of a type: %s stage, which must not contain a write-capable agent, but reaches %s", callee, n.Type, a)
					}
				}
			}
		}
	}
	c.unboundedCycles(w)
}

func (c *checker) someAgentOutputs(name string) bool {
	for _, a := range c.set.Agents {
		if a.DeclaresOutput(name) {
			return true
		}
	}
	return false
}

// writeAgentsIn lists the write-capable agents reachable from a workflow
// through calls and foreach bodies.
func (c *checker) writeAgentsIn(root string) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(path string)
	walk = func(path string) {
		if seen[path] {
			return
		}
		seen[path] = true
		w := c.set.Workflows[path]
		if w == nil {
			return
		}
		for _, id := range w.Order {
			n := w.Nodes[id]
			if n.Type == def.TypeAgent {
				if a := c.set.Agents[n.Role]; a != nil && a.WriteCapable() {
					out = append(out, fmt.Sprintf("%s (node %s in %s)", n.Role, id, path))
				}
			}
			if callee := n.Callee(); callee != "" {
				walk(callee)
			}
		}
	}
	walk(root)
	return out
}

// callCycles rejects workflows that call themselves directly or through
// others. Summaries of callees (used by the flow checks) need a call
// graph without cycles.
func (c *checker) callCycles() {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var visit func(path string)
	visit = func(path string) {
		color[path] = grey
		stack = append(stack, path)
		w := c.set.Workflows[path]
		if w != nil {
			for _, id := range w.Order {
				callee := w.Nodes[id].Callee()
				if callee == "" || c.set.Workflows[callee] == nil {
					continue
				}
				switch color[callee] {
				case grey:
					i := indexOf(stack, callee)
					c.fail(path, id, "workflow calls form a cycle: %v", append(append([]string{}, stack[i:]...), callee))
				case white:
					visit(callee)
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[path] = black
	}
	for _, p := range c.set.SortedWorkflows() {
		if color[p] == white {
			visit(p)
		}
	}
}

// unboundedCycles requires every cycle in a workflow to pass an approval
// or a node with an entry limit, so nothing loops forever before a human
// is involved (ADR-0067). Agent nodes always have a limit.
func (c *checker) unboundedCycles(w *def.Workflow) {
	bounded := func(n *def.Node) bool {
		return n.Type == def.TypeApproval || n.EffectiveMax() > 0
	}
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var visit func(id string)
	visit = func(id string) {
		color[id] = grey
		stack = append(stack, id)
		n := w.Nodes[id]
		for _, o := range n.NextOrder {
			t := n.Next[o]
			if t.End {
				continue
			}
			next := w.Nodes[t.Node]
			if next == nil || bounded(next) {
				continue
			}
			switch color[t.Node] {
			case grey:
				i := indexOf(stack, t.Node)
				c.fail(w.Path, t.Node, "cycle %v has no approval and no node with max; it could loop forever", append(append([]string{}, stack[i:]...), t.Node))
			case white:
				visit(t.Node)
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
	}
	for _, id := range w.Order {
		if !bounded(w.Nodes[id]) && color[id] == white {
			visit(id)
		}
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return 0
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func list(m map[string]bool) string {
	if len(m) == 0 {
		return "nothing"
	}
	return fmt.Sprint(sorted(m))
}
