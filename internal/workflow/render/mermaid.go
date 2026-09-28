// Package render draws workflow definitions for `masuda workflow show`.
// The drawing is only a view: the YAML stays the source of truth
// (ADR-0062).
package render

import (
	"fmt"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
)

// Mermaid draws one workflow file as a Mermaid flowchart. Besides the
// user's nodes it draws what the engine always inserts, so the diagram
// shows what actually runs (ADR-0063): the deviation approval before each
// commit, and triage, which can interrupt anywhere.
func Mermaid(w *def.Workflow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%%%% %s\nflowchart TD\n", w.Path)
	id := func(node string) string { return "n_" + strings.ReplaceAll(node, "-", "_") }
	ends := map[string]bool{}
	endID := func(t def.Target) string {
		key := "end_" + strings.ReplaceAll(t.Outcome(), "-", "_")
		ends[t.String()] = true
		return key
	}
	fmt.Fprintf(&b, "  start((start)) --> %s\n", id(w.Start))
	for _, name := range w.Order {
		n := w.Nodes[name]
		fmt.Fprintf(&b, "  %s%s\n", id(name), shape(n))
		for _, o := range n.NextOrder {
			t := n.Next[o]
			to := ""
			if t.End {
				to = endID(t)
			} else {
				to = id(t.Node)
			}
			if n.Type == def.TypeCommit && o == def.OutcomeDone {
				dev := id(name) + "_deviation"
				fmt.Fprintf(&b, "  %s -. before commit .-> %s{{\"deviation approval<br/>(engine, if files outside the plan changed)\"}}\n", id(name), dev)
			}
			fmt.Fprintf(&b, "  %s -->|%s| %s\n", id(name), o, to)
		}
	}
	for _, name := range w.Order {
		for _, o := range w.Nodes[name].NextOrder {
			if t := w.Nodes[name].Next[o]; t.End && ends[t.String()] {
				fmt.Fprintf(&b, "  %s(((\"%s\")))\n", endID(t), t.String())
				delete(ends, t.String())
			}
		}
	}
	b.WriteString("  triage{{\"triage (engine; can interrupt any node)\"}}\n")
	b.WriteString("  classDef human fill:#fde68a,stroke:#b45309\n")
	b.WriteString("  classDef engine stroke-dasharray: 4 3\n")
	for _, name := range w.Order {
		if w.Nodes[name].Type == def.TypeApproval {
			fmt.Fprintf(&b, "  class %s human\n", id(name))
		}
	}
	b.WriteString("  class triage engine\n")
	return b.String()
}

func shape(n *def.Node) string {
	label := fmt.Sprintf("%s<br/>type: %s", n.ID, n.Type)
	switch {
	case n.Type == def.TypeAgent:
		label += "<br/>" + n.Role
	case n.Type == def.TypeApproval:
		label += fmt.Sprintf("<br/>gate: %s, target: %s", n.Gate, n.GateTarget)
		return fmt.Sprintf("{\"%s\"}", label)
	case n.Type == def.TypeCheck:
		label += "<br/>check: " + n.Check
	case n.Type == def.TypeForeach:
		over := n.Over
		if n.OverFrom != "" {
			over = fmt.Sprintf("perspectives(from=%s)", n.OverFrom)
		}
		label += fmt.Sprintf("<br/>over: %s<br/>body: %s", over, n.Body)
	case n.Type.CallsWorkflow():
		label += "<br/>" + n.Workflow
	case n.Type == def.TypeCommit:
		label += "<br/>scope: " + n.Scope
	}
	if m := n.EffectiveMax(); m > 0 {
		label += fmt.Sprintf("<br/>max: %d", m)
	}
	return fmt.Sprintf("[\"%s\"]", label)
}
