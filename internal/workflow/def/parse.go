package def

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FormatVersion is the only workflow format version this engine reads.
const FormatVersion = 1

// allowedFields lists, per node type, the keys a node may carry besides
// `type` and `next`. Anything else is an error, so a key meant for
// another type (e.g. `gate` on an agent node) is caught at load time.
var allowedFields = map[Type][]string{
	TypeAgent:       {"role", "max"},
	TypeApproval:    {"gate", "target"},
	TypeCheck:       {"check", "max"},
	TypeForeach:     {"over", "body", "on_incomplete", "with", "max"},
	TypeWorkflow:    {"workflow", "with", "max"},
	TypeCommit:      {"scope"},
	TypePublish:     {"export"},
	TypeDiscard:     {"export"},
	TypeInvestigate: {"workflow", "with", "max"},
	TypePlan:        {"workflow", "with", "max"},
	TypeImplement:   {"workflow", "with", "max"},
	TypeReview:      {"workflow", "with", "max"},
}

var (
	nodeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	namePattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	overFromRe    = regexp.MustCompile(`^perspectives\(from=([a-z][a-z0-9-]*)\)$`)
)

// ParseWorkflow parses one workflow file. path is its reference path
// (e.g. "workflows/develop"), used in errors and kept on the result.
// It checks only what can be judged from this file alone.
func ParseWorkflow(path string, src []byte) (*Workflow, []*Error) {
	var errs []*Error
	fail := func(node, format string, args ...any) {
		errs = append(errs, &Error{Pos: Pos{File: path, Node: node}, Msg: fmt.Sprintf(format, args...)})
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		fail("", "not valid YAML: %v", err)
		return nil, errs
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		fail("", "the top level must be a mapping")
		return nil, errs
	}
	w := &Workflow{Path: path, Nodes: map[string]*Node{}}
	var nodesNode *yaml.Node
	for k, v := range pairs(doc.Content[0]) {
		switch k.Value {
		case "version":
			n, err := strconv.Atoi(v.Value)
			if err != nil || v.Kind != yaml.ScalarNode {
				fail("", "version must be an integer")
				continue
			}
			w.Version = n
		case "inputs":
			names, ok := stringList(v)
			if !ok {
				fail("", "inputs must be a list of names")
				continue
			}
			w.Inputs = names
		case "start":
			w.Start = v.Value
		case "nodes":
			nodesNode = v
		default:
			fail("", "unknown key %q", k.Value)
		}
	}
	if w.Version != FormatVersion {
		fail("", "version must be %d, got %d", FormatVersion, w.Version)
	}
	seenInput := map[string]bool{}
	for _, in := range w.Inputs {
		if !namePattern.MatchString(in) {
			fail("", "invalid input name %q", in)
		}
		if seenInput[in] {
			fail("", "input %q is declared twice", in)
		}
		seenInput[in] = true
	}
	if nodesNode == nil || nodesNode.Kind != yaml.MappingNode || len(nodesNode.Content) == 0 {
		fail("", "nodes is empty")
		return w, errs
	}
	for k, v := range pairs(nodesNode) {
		id := k.Value
		if !nodeIDPattern.MatchString(id) || id == "end" {
			fail(id, "invalid node name (lowercase letters, digits and hyphens; `end` is reserved)")
			continue
		}
		if _, dup := w.Nodes[id]; dup {
			fail(id, "node is defined twice")
			continue
		}
		n, nerrs := parseNode(path, id, v)
		errs = append(errs, nerrs...)
		if n != nil {
			w.Nodes[id] = n
			w.Order = append(w.Order, id)
		}
	}
	if w.Start == "" {
		fail("", "start is missing")
	} else if _, ok := w.Nodes[w.Start]; !ok {
		fail("", "start node %q does not exist", w.Start)
	}
	for _, id := range w.Order {
		n := w.Nodes[id]
		for _, o := range n.NextOrder {
			t := n.Next[o]
			if !t.End {
				if _, ok := w.Nodes[t.Node]; !ok {
					fail(id, "next.%s points to %q, which is not a node", o, t.Node)
				}
			}
		}
	}
	return w, errs
}

func parseNode(path, id string, v *yaml.Node) (*Node, []*Error) {
	var errs []*Error
	fail := func(format string, args ...any) {
		errs = append(errs, &Error{Pos: Pos{File: path, Node: id}, Msg: fmt.Sprintf(format, args...)})
	}
	if v.Kind != yaml.MappingNode {
		fail("a node must be a mapping")
		return nil, errs
	}
	n := &Node{ID: id}
	fields := map[string]*yaml.Node{}
	for k, val := range pairs(v) {
		fields[k.Value] = val
	}
	tn, ok := fields["type"]
	if !ok {
		fail("type is missing")
		return nil, errs
	}
	n.Type = Type(tn.Value)
	allowed, known := allowedFields[n.Type]
	if !known {
		fail("unknown type %q", tn.Value)
		return nil, errs
	}
	allowedSet := map[string]bool{"type": true, "next": true}
	for _, f := range allowed {
		allowedSet[f] = true
	}
	for k := range pairs(v) {
		if !allowedSet[k.Value] {
			fail("a type: %s node cannot have %q", n.Type, k.Value)
		}
	}

	if nx, ok := fields["next"]; ok {
		n.Next = map[string]Target{}
		switch nx.Kind {
		case yaml.ScalarNode:
			t, err := parseTarget(nx.Value)
			if err != nil {
				fail("next: %v", err)
			} else {
				n.Next[OutcomeDone] = t
				n.NextOrder = []string{OutcomeDone}
			}
		case yaml.MappingNode:
			for k, val := range pairs(nx) {
				if val.Kind != yaml.ScalarNode {
					fail("next.%s must be a string", k.Value)
					continue
				}
				if _, dup := n.Next[k.Value]; dup {
					fail("next.%s is written twice", k.Value)
					continue
				}
				t, err := parseTarget(val.Value)
				if err != nil {
					fail("next.%s: %v", k.Value, err)
					continue
				}
				n.Next[k.Value] = t
				n.NextOrder = append(n.NextOrder, k.Value)
			}
		default:
			fail("next must be a string or a mapping")
		}
	}

	str := func(key string) string {
		if f, ok := fields[key]; ok {
			if f.Kind != yaml.ScalarNode {
				fail("%s must be a string", key)
				return ""
			}
			return f.Value
		}
		return ""
	}
	require := func(key, val string) {
		if val == "" {
			fail("a type: %s node needs %s", n.Type, key)
		}
	}
	ref := func(key, prefix string) string {
		val := str(key)
		require(key, val)
		if val != "" && !validRef(val, prefix) {
			fail("%s must be a path under .masuda/ without extension, starting with %s (got %q)", key, prefix, val)
		}
		return val
	}

	if f, ok := fields["max"]; ok {
		m, err := strconv.Atoi(f.Value)
		if err != nil || m < 1 {
			fail("max must be an integer of at least 1")
		} else {
			n.Max = m
		}
	}
	if f, ok := fields["with"]; ok {
		if f.Kind != yaml.MappingNode {
			fail("with must be a mapping")
		} else {
			n.With = map[string]string{}
			for k, val := range pairs(f) {
				n.With[k.Value] = val.Value
			}
		}
	}

	switch n.Type {
	case TypeAgent:
		n.Role = ref("role", "agents/")
	case TypeApproval:
		n.Gate = str("gate")
		require("gate", n.Gate)
		if n.Gate != "" && !namePattern.MatchString(n.Gate) {
			fail("invalid gate name %q", n.Gate)
		}
		n.GateTarget = str("target")
		require("target", n.GateTarget)
		if n.GateTarget != "" && n.GateTarget != "plan" && n.GateTarget != "diff" {
			fail("target must be plan or diff (got %q)", n.GateTarget)
		}
	case TypeCheck:
		n.Check = str("check")
		require("check", n.Check)
	case TypeForeach:
		over := str("over")
		require("over", over)
		if m := overFromRe.FindStringSubmatch(over); m != nil {
			n.Over, n.OverFrom = OverPerspectives, m[1]
		} else if _, ok := OverItemInput[over]; ok || over == "" {
			n.Over = over
		} else {
			fail("over must be steps, perspectives, perspectives(from=<node>) or findings (got %q)", over)
		}
		n.Body = ref("body", "workflows/")
		n.OnIncomplete = str("on_incomplete")
		if n.OnIncomplete == "" {
			n.OnIncomplete = "stop"
		} else if n.OnIncomplete != "stop" && n.OnIncomplete != "continue" {
			fail("on_incomplete must be stop or continue (got %q)", n.OnIncomplete)
		}
	case TypeWorkflow, TypeInvestigate, TypePlan, TypeImplement, TypeReview:
		n.Workflow = ref("workflow", "workflows/")
	case TypeCommit:
		n.Scope = str("scope")
		require("scope", n.Scope)
		if n.Scope != "" && n.Scope != "step" && n.Scope != "plan" {
			fail("scope must be step or plan (got %q)", n.Scope)
		}
	case TypePublish, TypeDiscard:
		if f, ok := fields["export"]; ok {
			names, ok := stringList(f)
			if !ok {
				fail("export must be a list of data names")
			}
			for _, name := range names {
				if strings.Contains(name, ".") || strings.Contains(name, "/") {
					fail("export takes data names, not file names (got %q)", name)
				}
			}
			n.Export = names
		}
	}
	return n, errs
}

func parseTarget(s string) (Target, error) {
	switch {
	case s == "end":
		return Target{End: true}, nil
	case strings.HasPrefix(s, "end:"):
		label := strings.TrimPrefix(s, "end:")
		if !namePattern.MatchString(label) {
			return Target{}, fmt.Errorf("invalid end label %q", label)
		}
		if label == OutcomeDone {
			return Target{}, fmt.Errorf("write `end` instead of `end:done`")
		}
		return Target{End: true, Label: label}, nil
	case nodeIDPattern.MatchString(s):
		return Target{Node: s}, nil
	}
	return Target{}, fmt.Errorf("invalid target %q", s)
}

// validRef reports whether s is a reference path under prefix: no
// extension, no leading slash, no parent references.
func validRef(s, prefix string) bool {
	if !strings.HasPrefix(s, prefix) || len(s) == len(prefix) {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.Contains(seg, ".") {
			return false
		}
	}
	return true
}

// pairs iterates a mapping node's key/value pairs in file order.
func pairs(m *yaml.Node) func(yield func(k, v *yaml.Node) bool) {
	return func(yield func(k, v *yaml.Node) bool) {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if !yield(m.Content[i], m.Content[i+1]) {
				return
			}
		}
	}
}

func stringList(n *yaml.Node) ([]string, bool) {
	if n.Kind != yaml.SequenceNode {
		return nil, false
	}
	var out []string
	for _, c := range n.Content {
		if c.Kind != yaml.ScalarNode {
			return nil, false
		}
		out = append(out, c.Value)
	}
	return out, true
}
