package def

import (
	"bytes"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Agent is one parsed agent definition: YAML frontmatter plus the prompt
// body (ADR-0064, ADR-0065).
type Agent struct {
	// Path is the reference path, e.g. "agents/planner".
	Path        string
	Name        string
	Description string
	// Tools is nil when the definition lists none, which Claude Code
	// treats as "all tools".
	Tools []string
	// Outcomes maps each declared outcome to the description the engine
	// appends to the prompt; OutcomeOrder keeps file order.
	Outcomes     map[string]string
	OutcomeOrder []string
	Outputs      []string
	// Inputs are data the agent reads besides its workflow's inputs,
	// resolved by name when its task is handed out (ADR-0082).
	Inputs []string
	Resume bool
	Prompt string
}

// writeTools are the tools that let an agent change files directly. An
// agent holding none of them is treated as read-only by the static checks;
// what it does through Bash is measured with git after it runs instead
// (ADR-0081).
var writeTools = map[string]bool{
	"Write":        true,
	"Edit":         true,
	"MultiEdit":    true,
	"NotebookEdit": true,
}

// WriteCapable reports whether the static checks must assume this agent
// changes the worktree.
func (a *Agent) WriteCapable() bool {
	if a.Tools == nil {
		return true
	}
	for _, t := range a.Tools {
		if writeTools[t] {
			return true
		}
	}
	return false
}

// DeclaresOutput reports whether the agent declares the named output.
func (a *Agent) DeclaresOutput(name string) bool {
	for _, o := range a.Outputs {
		if o == name {
			return true
		}
	}
	return false
}

// ParseAgent parses one agent definition file.
func ParseAgent(path string, src []byte) (*Agent, []*Error) {
	var errs []*Error
	fail := func(format string, args ...any) {
		errs = append(errs, &Error{Pos: Pos{File: path}, Msg: fmt.Sprintf(format, args...)})
	}
	front, body, ok := splitFrontmatter(src)
	if !ok {
		fail("an agent definition must start with YAML frontmatter between --- lines")
		return nil, errs
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(front, &doc); err != nil {
		fail("frontmatter is not valid YAML: %v", err)
		return nil, errs
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		fail("frontmatter must be a mapping")
		return nil, errs
	}
	a := &Agent{Path: path, Outcomes: map[string]string{}, Prompt: strings.TrimSpace(string(body))}
	for k, v := range pairs(doc.Content[0]) {
		switch k.Value {
		case "name":
			a.Name = v.Value
		case "description":
			a.Description = v.Value
		case "tools":
			switch v.Kind {
			case yaml.ScalarNode:
				for _, t := range strings.Split(v.Value, ",") {
					if t = strings.TrimSpace(t); t != "" {
						a.Tools = append(a.Tools, t)
					}
				}
			case yaml.SequenceNode:
				names, ok := stringList(v)
				if !ok {
					fail("tools must be a list of tool names")
				}
				a.Tools = names
			default:
				fail("tools must be a list or a comma-separated string")
			}
			if a.Tools == nil {
				// An explicit empty list means no tools, not all tools.
				a.Tools = []string{}
			}
		case "outcomes":
			if v.Kind != yaml.MappingNode {
				fail("outcomes must map each outcome to a description")
				continue
			}
			for ok, ov := range pairs(v) {
				name := ok.Value
				switch {
				case !namePattern.MatchString(name):
					fail("invalid outcome name %q", name)
				case ReservedOutcomes[name]:
					fail("outcome %q is reserved for the engine", name)
				case a.Outcomes[name] != "":
					fail("outcome %q is declared twice", name)
				case strings.TrimSpace(ov.Value) == "":
					fail("outcome %q needs a description", name)
				default:
					a.Outcomes[name] = ov.Value
					a.OutcomeOrder = append(a.OutcomeOrder, name)
				}
			}
		case "outputs":
			names, ok := stringList(v)
			if !ok {
				fail("outputs must be a list of data names")
				continue
			}
			for _, n := range names {
				if !namePattern.MatchString(n) {
					fail("invalid output name %q (data names have no extension)", n)
				}
			}
			a.Outputs = names
		case "inputs":
			names, ok := stringList(v)
			if !ok {
				fail("inputs must be a list of data names")
				continue
			}
			for _, n := range names {
				if !namePattern.MatchString(n) {
					fail("invalid input name %q", n)
				}
			}
			a.Inputs = names
		case "resume":
			a.Resume = v.Value == "true"
			if v.Value != "true" && v.Value != "false" {
				fail("resume must be true or false")
			}
		default:
			fail("unknown key %q", k.Value)
		}
	}
	if len(a.Outcomes) == 0 {
		fail("outcomes must declare at least one outcome")
	} else if _, ok := a.Outcomes[OutcomeDone]; !ok {
		fail("outcomes must include %q", OutcomeDone)
	}
	if a.Prompt == "" {
		fail("the prompt body is empty")
	}
	return a, errs
}

func splitFrontmatter(src []byte) (front, body []byte, ok bool) {
	src = bytes.TrimPrefix(src, []byte{0xEF, 0xBB, 0xBF})
	if !bytes.HasPrefix(src, []byte("---\n")) {
		return nil, nil, false
	}
	rest := src[len("---\n"):]
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		if bytes.HasSuffix(rest, []byte("\n---")) {
			return rest[:len(rest)-len("\n---")], nil, true
		}
		return nil, nil, false
	}
	return rest[:end], rest[end+len("\n---\n"):], true
}
