// Package def reads workflow definitions (.masuda/workflows/**.yaml) and
// agent definitions (.masuda/agents/**.md) into typed values. It resolves
// references, checks each file's shape, and nothing more: rules that need
// to see several files at once live in internal/workflow/check.
package def

import "fmt"

// Type is a node's kind. The set is fixed by the engine (ADR-0062,
// ADR-0064); users choose from it and cannot add to it.
type Type string

const (
	TypeAgent    Type = "agent"
	TypeApproval Type = "approval"
	TypeCheck    Type = "check"
	TypeForeach  Type = "foreach"
	TypeWorkflow Type = "workflow"
	TypeCommit   Type = "commit"
	TypePublish  Type = "publish"
	TypeDiscard  Type = "discard"

	TypeInvestigate Type = "investigate"
	TypePlan        Type = "plan"
	TypeImplement   Type = "implement"
	TypeReview      Type = "review"
)

// IsStage reports whether t is a stage kind: a node that runs a workflow
// under an engine-fixed contract (ADR-0082).
func (t Type) IsStage() bool {
	_, ok := StageContracts[t]
	return ok
}

// CallsWorkflow reports whether nodes of type t run another workflow file
// named by their `workflow:` field.
func (t Type) CallsWorkflow() bool {
	return t == TypeWorkflow || t.IsStage()
}

// Outcome names the engine reserves. Users can neither declare them as
// agent outcomes nor use them as end labels (ADR-0065).
const (
	OutcomeDone       = "done"
	OutcomeExhausted  = "exhausted"
	OutcomeBlocked    = "blocked"
	OutcomeApproved   = "approved"
	OutcomeRejected   = "rejected"
	OutcomeFailed     = "failed"
	OutcomeIncomplete = "incomplete"
)

// ReservedOutcomes are names only the engine may produce.
var ReservedOutcomes = map[string]bool{
	OutcomeExhausted: true,
	OutcomeBlocked:   true,
}

// ReservedGates are gate names the engine uses for its own approvals
// (ADR-0066); user workflows cannot name a gate after them.
var ReservedGates = map[string]bool{
	"triage":    true,
	"deviation": true,
}

// DefaultAgentMax is the entry limit an agent node gets when it declares
// no `max` (ADR-0067).
const DefaultAgentMax = 3

// Workflow is one parsed workflow file.
type Workflow struct {
	// Path is the reference path, e.g. "workflows/implement/default".
	Path    string
	Version int
	Inputs  []string
	Start   string
	Nodes   map[string]*Node
	// Order keeps the nodes in file order, for stable error messages
	// and diagrams.
	Order []string
}

// Target is where a transition goes: another node, or the end of the
// workflow. An `end` target finishes with outcome done; `end:<label>`
// finishes with that label.
type Target struct {
	Node  string
	End   bool
	Label string
}

// Outcome returns the outcome the workflow finishes with when it reaches
// this end target.
func (t Target) Outcome() string {
	if t.Label == "" {
		return OutcomeDone
	}
	return t.Label
}

func (t Target) String() string {
	switch {
	case !t.End:
		return t.Node
	case t.Label == "":
		return "end"
	default:
		return "end:" + t.Label
	}
}

// Node is one node of a workflow. Only the fields that belong to its Type
// are set; Parse rejects fields that don't.
type Node struct {
	ID   string
	Type Type
	// Next maps outcome to target. A bare string `next: x` is stored as
	// {done: x}.
	Next map[string]Target
	// NextOrder keeps Next's keys in file order.
	NextOrder []string
	// Max is the entry limit; 0 means none was written.
	Max int

	Role string // agent: reference path of the agent definition

	Gate         string // approval
	GateTarget   string // approval: "plan" or "diff"
	Check        string // check: name of a checks entry in settings.json
	Over         string // foreach: "steps", "perspectives", "findings"
	OverFrom     string // foreach: node id in perspectives(from=<id>)
	Body         string // foreach: reference path of the body workflow
	OnIncomplete string // foreach: "stop" (default) or "continue"
	Workflow     string // workflow and stages: reference path of the callee
	// With binds a callee input name to a data name (ADR-0077).
	With   map[string]string
	Scope  string   // commit: "step" or "plan"
	Export []string // publish, discard: data names to copy out
}

// EffectiveMax returns the node's entry limit, applying the agent default.
func (n *Node) EffectiveMax() int {
	if n.Max == 0 && n.Type == TypeAgent {
		return DefaultAgentMax
	}
	return n.Max
}

// Callee returns the reference path of the workflow this node runs, or ""
// if it runs none.
func (n *Node) Callee() string {
	switch {
	case n.Type == TypeForeach:
		return n.Body
	case n.Type.CallsWorkflow():
		return n.Workflow
	}
	return ""
}

// Pos locates a problem for error messages.
type Pos struct {
	File string
	Node string
}

func (p Pos) String() string {
	if p.Node == "" {
		return p.File
	}
	return fmt.Sprintf("%s: %s", p.File, p.Node)
}

// Error is a definition problem found at load time.
type Error struct {
	Pos Pos
	Msg string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Pos, e.Msg) }
