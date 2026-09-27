package engine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
)

// Env is everything the engine does outside its own records: git, checks,
// files, the workspace. The daemon supplies the real one; dry runs and
// tests supply stubs, which is what lets the whole engine run without
// agents or a VM (ADR-0077).
type Env interface {
	// Data returns the path of the most recent value of a data name
	// (ADR-0082): an agent output, or diff / step-diff computed now.
	Data(name string) (path string, ok bool, err error)
	// Items lists what a foreach iterates over. from is the occurrence of
	// the node that picked the items (perspectives(from=...)), scope the
	// frame whose findings count (over: findings).
	Items(over, from, scope string) ([]Item, error)
	// StepDone reports whether a step is already committed under the
	// current approved plan (ADR-0080).
	StepDone(key string) (bool, error)
	RunCheck(name string) (passed bool, feedback string, err error)
	// Deviations lists files changed outside what a commit may include.
	// hash identifies that exact set of changes, for the approval.
	Deviations(scope, step string) (files []string, hash string, err error)
	Commit(scope, step string, approved []string) error
	Publish(export []string) error
	Discard(export []string) error
	TargetHash(target string) (string, error)
	Snapshot() (string, error)
	ChangedSince(snapshot string) (files []string, hash string, err error)
	// TreeSnapshot records the worktree as a git tree, and DiffSince
	// writes the diff from such a tree to now, for fix-diff (ADR-0074).
	TreeSnapshot() (string, error)
	DiffSince(tree string) (path string, err error)
	// HasOutput reports whether an agent occurrence wrote the named output.
	HasOutput(name, occurrence string) bool
	// OutputsDone is called when an agent occurrence finishes done, so the
	// environment can take in what it wrote (findings, ADR-0082).
	OutputsDone(ctx OutputContext, outputs []string) error
	// ItemFinished is called once a foreach item's iteration has ended, so
	// the environment can record how (a finding resolved or not).
	ItemFinished(over, key, outcome string) error
	WriteFeedback(occurrence, text string) (path string, err error)
	Log(e Event)
}

// OutputContext says where an agent's outputs came from.
type OutputContext struct {
	Occurrence string
	Frame      string
	Workflow   string
	Node       string
	Role       string
	// Inputs are the frame's inputs; a review's perspective is among them.
	Inputs map[string]string
	// AgentID is the Claude Code agent that did the work, if the session
	// reported it (ADR-0074).
	AgentID string
}

// Event is one line of the execution record (ADR-0075).
type Event struct {
	Kind       string `json:"kind"`
	Occurrence string `json:"occurrence"`
	Workflow   string `json:"workflow"`
	Node       string `json:"node"`
	Outcome    string `json:"outcome,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// StatusKind says why Advance returned.
type StatusKind string

const (
	StatusAgent   StatusKind = "agent"   // an agent task waits for the main session
	StatusGate    StatusKind = "gate"    // a human decision is needed
	StatusDone    StatusKind = "done"    // the root workflow finished
	StatusBlocked StatusKind = "blocked" // an outcome had no destination
)

// Status is where the run stands after Advance.
type Status struct {
	Kind    StatusKind
	Task    *Task
	Gate    *GateRequest
	Outcome string // StatusDone: the root's outcome
	Reason  string // StatusBlocked
}

// Task is an agent task for the main session.
type Task struct {
	Occurrence string            `json:"occurrence"`
	Workflow   string            `json:"workflow"`
	Node       string            `json:"node"`
	Role       string            `json:"role"`
	Inputs     map[string]string `json:"inputs,omitempty"`
	Feedback   string            `json:"feedback,omitempty"`
}

// GateRequest is an open approval (ADR-0066).
type GateRequest struct {
	Name       string   `json:"name"`
	Occurrence string   `json:"occurrence"`
	Target     string   `json:"target"`
	Hash       string   `json:"hash"`
	Files      []string `json:"files,omitempty"`
	// Detail is what the human is asked to judge, when it is text rather
	// than content the engine hashes elsewhere (a triage concern).
	Detail string `json:"detail,omitempty"`
}

// Decision is a human's answer to a GateRequest. It names the occurrence
// and hash it was made against; a decision for anything else is refused.
type Decision struct {
	Occurrence string `json:"occurrence"`
	Hash       string `json:"hash"`
	Approved   bool   `json:"approved"`
	// Halt stops the run for good; only the triage gate offers it
	// (ADR-0029).
	Halt    bool   `json:"halt,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// Engine runs one workflow set against one store and environment.
type Engine struct {
	Set   *def.Set
	Store Store
	Env   Env
}

// ErrFuse means the run exceeded the fixed total number of node entries.
var ErrFuse = errors.New("workflow engine fuse blown: too many node entries (engine bug)")

// Start records the root frame with its inputs. It is a no-op once the
// run has started.
func (e *Engine) Start(inputs map[string]string) error {
	if _, ok := e.Store.Get(prefixFrame + rootFrame); ok {
		return nil
	}
	return putJSON(e.Store, prefixFrame+rootFrame, Frame{ID: rootFrame, Workflow: e.Set.Root, Inputs: inputs})
}

// Advance moves the run as far as it can without an agent or a human, and
// reports what it waits for.
func (e *Engine) Advance() (Status, error) {
	for {
		if _, blocked := e.Store.Get(keyBlocked); blocked {
			b, _ := e.Store.Get(keyBlocked)
			return Status{Kind: StatusBlocked, Reason: string(b)}, nil
		}
		r, err := loadRecords(e.Store)
		if err != nil {
			return Status{}, err
		}
		if len(r.occs) > fuse {
			return Status{}, ErrFuse
		}
		if end, ok := e.frameEnd(rootFrame); ok {
			return Status{Kind: StatusDone, Outcome: end}, nil
		}
		// A reported concern comes before anything else (ADR-0029,
		// ADR-0063): no node moves until a human has looked at it.
		st, progressed, err := e.triage(r)
		if err != nil {
			return Status{}, err
		}
		if st.Kind != "" || progressed {
			if !progressed {
				return st, nil
			}
			continue
		}
		st, progressed, err = e.stepFrame(r, rootFrame)
		if err != nil {
			return Status{}, err
		}
		if !progressed {
			return st, nil
		}
	}
}

// stepFrame advances the given frame by one move, descending into running
// calls. It returns progressed=true when it changed the records, in which
// case the caller reloads and tries again.
func (e *Engine) stepFrame(r *records, frameID string) (Status, bool, error) {
	var fr Frame
	if err := getJSON(e.Store, prefixFrame+frameID, &fr); err != nil {
		return Status{}, false, err
	}
	w := e.Set.Workflows[fr.Workflow]
	if w == nil {
		return Status{}, false, fmt.Errorf("workflow %s is not loaded", fr.Workflow)
	}
	cur := r.last(frameID)
	if cur == nil {
		return Status{}, true, e.enter(r, fr, w, w.Start, "")
	}
	n := w.Nodes[cur.Node]
	if res, done := r.results[cur.ID]; done {
		return e.transition(r, fr, w, cur, n, res)
	}
	if cur.Exhausted {
		return Status{}, true, e.finish(cur, def.OutcomeExhausted, "")
	}
	switch n.Type {
	case def.TypeAgent:
		return e.agent(cur, n, fr)
	case def.TypeApproval:
		return e.approval(cur, n)
	case def.TypeCheck:
		passed, feedback, err := e.Env.RunCheck(n.Check)
		if err != nil {
			return Status{}, false, err
		}
		if passed {
			return Status{}, true, e.finish(cur, def.OutcomeDone, "")
		}
		return Status{}, true, e.finish(cur, def.OutcomeFailed, feedback)
	case def.TypeCommit:
		return e.commit(cur, n, fr)
	case def.TypePublish:
		if err := e.Env.Publish(n.Export); err != nil {
			return Status{}, false, err
		}
		return Status{}, true, e.finish(cur, def.OutcomeDone, "")
	case def.TypeDiscard:
		if err := e.Env.Discard(n.Export); err != nil {
			return Status{}, false, err
		}
		return Status{}, true, e.finish(cur, def.OutcomeDone, "")
	case def.TypeForeach:
		return e.foreach(r, cur, n, fr)
	default: // workflow and stages
		child := childFrame(cur.ID)
		if out, ok := e.frameEnd(child); ok {
			return Status{}, true, e.finish(cur, out, "")
		}
		if _, ok := e.Store.Get(prefixFrame + child); !ok {
			callee := e.Set.Workflows[n.Workflow]
			inputs, err := e.bind(fr, n, callee, nil)
			if err != nil {
				return Status{}, false, err
			}
			if err := putJSON(e.Store, prefixFrame+child, Frame{ID: child, Workflow: callee.Path, Inputs: inputs}); err != nil {
				return Status{}, false, err
			}
			return Status{}, true, nil
		}
		return e.stepFrame(r, child)
	}
}

// bind resolves each input the callee declares to a path: the item for a
// foreach, else the data it is bound to with `with:` (or its own name),
// looked up in the caller's inputs first and the environment second.
func (e *Engine) bind(caller Frame, n *def.Node, callee *def.Workflow, item *Item) (map[string]string, error) {
	out := map[string]string{}
	itemInput := ""
	if n.Type == def.TypeForeach {
		itemInput = def.OverItemInput[n.Over]
	}
	for _, in := range callee.Inputs {
		if item != nil && in == itemInput {
			out[in] = item.Path
			continue
		}
		name := in
		if b, ok := n.With[in]; ok {
			name = b
		}
		if p, ok := caller.Inputs[name]; ok {
			out[in] = p
			continue
		}
		p, ok, err := e.Env.Data(name)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%s: node %s: input %q (data %q) is not available", caller.Workflow, n.ID, in, name)
		}
		out[in] = p
	}
	return out, nil
}

// enter records an entry into node id of the frame, or an exhausted entry
// when the node's max is reached. Entries are counted since the frame's
// last decided approval (ADR-0067).
func (e *Engine) enter(r *records, fr Frame, w *def.Workflow, id, feedback string) error {
	return e.enterWith(r, fr, w, id, feedback, false)
}

// enterUncounted is enter for a re-entry a human ordered, which must not
// use up the node's entries.
func (e *Engine) enterUncounted(r *records, fr Frame, w *def.Workflow, id, feedback string) error {
	return e.enterWith(r, fr, w, id, feedback, true)
}

func (e *Engine) enterWith(r *records, fr Frame, w *def.Workflow, id, feedback string, uncounted bool) error {
	if len(r.occs) >= fuse {
		return ErrFuse
	}
	n := w.Nodes[id]
	occ := &Occurrence{ID: e.nextID(), Frame: fr.ID, Workflow: w.Path, Node: id, Feedback: feedback, Uncounted: uncounted}
	if max := n.EffectiveMax(); max > 0 && !uncounted {
		count := 0
		for _, o := range r.byFrame[fr.ID] {
			if w.Nodes[o.Node].Type == def.TypeApproval && r.results[o.ID] != nil {
				count = 0
				continue
			}
			if o.Node == id && !o.Exhausted && !o.Uncounted {
				count++
			}
		}
		occ.Exhausted = count >= max
	}
	e.Env.Log(Event{Kind: "enter", Occurrence: occ.ID, Workflow: w.Path, Node: id, Detail: exhaustedDetail(occ.Exhausted)})
	return putJSON(e.Store, prefixOcc+occ.ID, occ)
}

func exhaustedDetail(ex bool) string {
	if ex {
		return "max reached; not run"
	}
	return ""
}

func (e *Engine) nextID() string {
	n := 0
	if b, ok := e.Store.Get(keySeq); ok {
		n, _ = strconv.Atoi(string(b))
	}
	n++
	_ = e.Store.Put(keySeq, []byte(strconv.Itoa(n)))
	return fmt.Sprintf("%0*d", idWidth, n)
}

func (e *Engine) finish(o *Occurrence, outcome, feedbackText string) error {
	res := Result{Outcome: outcome}
	if feedbackText != "" {
		p, err := e.Env.WriteFeedback(o.ID, feedbackText)
		if err != nil {
			return err
		}
		res.Feedback = p
	}
	e.Env.Log(Event{Kind: "finish", Occurrence: o.ID, Workflow: o.Workflow, Node: o.Node, Outcome: outcome})
	return putJSON(e.Store, prefixResult+o.ID, res)
}

func (e *Engine) frameEnd(frameID string) (string, bool) {
	b, ok := e.Store.Get(prefixFrameEnd + frameID)
	return string(b), ok
}

// transition follows the finished occurrence's outcome: into the next
// node, out of the frame, or to a stop when nothing is routed.
func (e *Engine) transition(r *records, fr Frame, w *def.Workflow, cur *Occurrence, n *def.Node, res *Result) (Status, bool, error) {
	if res.Invalid {
		// The agent's report was refused: run the same node again. The
		// entry counts toward max, so a node that keeps failing stops.
		return Status{}, true, e.enter(r, fr, w, n.ID, res.Feedback)
	}
	if res.Retry {
		// A human decided on a triage concern and sent the node back to
		// work. Like passing an approval, that does not use up the node's
		// entries.
		return Status{}, true, e.enterUncounted(r, fr, w, n.ID, res.Feedback)
	}
	t, ok := n.Next[res.Outcome]
	if !ok {
		reason := fmt.Sprintf("%s: node %s finished %q, which has no destination", w.Path, n.ID, res.Outcome)
		e.Env.Log(Event{Kind: "blocked", Occurrence: cur.ID, Workflow: w.Path, Node: n.ID, Outcome: res.Outcome})
		return Status{}, true, e.Store.Put(keyBlocked, []byte(reason))
	}
	if t.End {
		e.Env.Log(Event{Kind: "end", Workflow: w.Path, Node: n.ID, Outcome: t.Outcome(), Detail: fr.ID})
		return Status{}, true, e.Store.Put(prefixFrameEnd+fr.ID, []byte(t.Outcome()))
	}
	return Status{}, true, e.enter(r, fr, w, t.Node, res.Feedback)
}

// Report records what an agent says it did (the curated MCP tool calls
// this). An outcome the agent does not declare, or a done without the
// declared outputs, is refused and the node runs again (ADR-0065,
// ADR-0073).
func (e *Engine) Report(occurrence, outcome, feedback, agentID string) error {
	var o Occurrence
	if err := getJSON(e.Store, prefixOcc+occurrence, &o); err != nil {
		return err
	}
	if _, done := e.Store.Get(prefixResult + occurrence); done {
		return fmt.Errorf("occurrence %s has already finished", occurrence)
	}
	n := e.Set.Workflows[o.Workflow].Nodes[o.Node]
	if n.Type != def.TypeAgent {
		return fmt.Errorf("occurrence %s is a type: %s node, not an agent", occurrence, n.Type)
	}
	a := e.Set.Agents[n.Role]
	reason := ""
	if _, declared := a.Outcomes[outcome]; !declared {
		reason = fmt.Sprintf("outcome %q is not one %s declares (%s)", outcome, n.Role, strings.Join(a.OutcomeOrder, ", "))
	} else if outcome == def.OutcomeDone {
		for _, out := range a.Outputs {
			if !e.Env.HasOutput(out, occurrence) {
				reason = fmt.Sprintf("finished done without writing the declared output %q", out)
				break
			}
		}
	}
	if reason != "" {
		p, err := e.Env.WriteFeedback(occurrence, "前回の報告は受け付けられなかった: "+reason)
		if err != nil {
			return err
		}
		e.Env.Log(Event{Kind: "invalid", Occurrence: occurrence, Workflow: o.Workflow, Node: o.Node, Detail: reason})
		return putJSON(e.Store, prefixResult+occurrence, Result{Outcome: outcome, Feedback: p, Invalid: true})
	}
	if !a.WriteCapable() {
		// Stored as a claim until the worktree check in agent() has run.
		return putJSON(e.Store, prefixReport+occurrence, pendingReport{Outcome: outcome, Feedback: feedback, AgentID: agentID})
	}
	return e.accept(&o, a, outcome, feedback, agentID)
}

const prefixReport = "wf:report/"

type pendingReport struct {
	Outcome  string `json:"outcome"`
	Feedback string `json:"feedback,omitempty"`
	AgentID  string `json:"agentId,omitempty"`
}

func (e *Engine) accept(o *Occurrence, a *def.Agent, outcome, feedback, agentID string) error {
	if outcome == def.OutcomeDone && len(a.Outputs) > 0 {
		var fr Frame
		if err := getJSON(e.Store, prefixFrame+o.Frame, &fr); err != nil {
			return err
		}
		ctx := OutputContext{Occurrence: o.ID, Frame: o.Frame, Workflow: o.Workflow, Node: o.Node, Role: a.Path, Inputs: fr.Inputs, AgentID: agentID}
		if err := e.Env.OutputsDone(ctx, a.Outputs); err != nil {
			return err
		}
	}
	return e.finish(o, outcome, feedback)
}

// agent hands out the task, and for agents without Write/Edit, checks the
// worktree after they report (ADR-0081): an agent the static checks
// treated as read-only must not have changed anything.
func (e *Engine) agent(cur *Occurrence, n *def.Node, fr Frame) (Status, bool, error) {
	a := e.Set.Agents[n.Role]
	if cur.Snapshot == "" && !a.WriteCapable() {
		snap, err := e.Env.Snapshot()
		if err != nil {
			return Status{}, false, err
		}
		cur.Snapshot = snap
		if err := putJSON(e.Store, prefixOcc+cur.ID, cur); err != nil {
			return Status{}, false, err
		}
	}
	var claim pendingReport
	if err := getJSON(e.Store, prefixReport+cur.ID, &claim); err == nil {
		files, hash, err := e.Env.ChangedSince(cur.Snapshot)
		if err != nil {
			return Status{}, false, err
		}
		if len(files) == 0 {
			_ = e.Store.Delete(prefixReport + cur.ID)
			return Status{}, true, e.accept(cur, a, claim.Outcome, claim.Feedback, claim.AgentID)
		}
		dec, decided, err := e.decision(gateDeviation, cur.ID, hash)
		if err != nil {
			return Status{}, false, err
		}
		if !decided {
			req := GateRequest{Name: gateDeviation, Occurrence: cur.ID, Target: "changes by an agent without Write/Edit", Hash: hash, Files: files}
			return Status{Kind: StatusGate, Gate: &req}, false, e.openGate(req)
		}
		_ = e.Store.Delete(prefixReport + cur.ID)
		if !dec.Approved {
			reason := fmt.Sprintf("%s changed %v without Write/Edit, and the change was rejected", n.Role, files)
			return Status{}, true, e.Store.Put(keyBlocked, []byte(reason))
		}
		return Status{}, true, e.accept(cur, a, claim.Outcome, claim.Feedback, claim.AgentID)
	}
	inputs, err := e.agentInputs(fr, a)
	if err != nil {
		return Status{}, false, err
	}
	return Status{Kind: StatusAgent, Task: &Task{
		Occurrence: cur.ID, Workflow: cur.Workflow, Node: cur.Node, Role: n.Role,
		Inputs: inputs, Feedback: cur.Feedback,
	}}, false, nil
}

// agentInputs is what an agent's task gets to read: its workflow's inputs
// plus the data its definition names, resolved now (ADR-0082).
func (e *Engine) agentInputs(fr Frame, a *def.Agent) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range fr.Inputs {
		out[k] = v
	}
	for _, name := range a.Inputs {
		if _, ok := out[name]; ok {
			continue
		}
		if name == def.DataFixDiff {
			if fr.Tree == "" {
				return nil, fmt.Errorf("%s reads fix-diff, which only exists inside a foreach over findings", a.Path)
			}
			p, err := e.Env.DiffSince(fr.Tree)
			if err != nil {
				return nil, err
			}
			out[name] = p
			continue
		}
		p, ok, err := e.Env.Data(name)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%s reads %q, which has not been written", a.Path, name)
		}
		out[name] = p
	}
	return out, nil
}

const gateDeviation = "deviation"

func (e *Engine) approval(cur *Occurrence, n *def.Node) (Status, bool, error) {
	hash, err := e.Env.TargetHash(n.GateTarget)
	if err != nil {
		return Status{}, false, err
	}
	dec, decided, err := e.decision(n.Gate, cur.ID, hash)
	if err != nil {
		return Status{}, false, err
	}
	if !decided {
		req := GateRequest{Name: n.Gate, Occurrence: cur.ID, Target: n.GateTarget, Hash: hash}
		return Status{Kind: StatusGate, Gate: &req}, false, e.openGate(req)
	}
	outcome := def.OutcomeRejected
	if dec.Approved {
		outcome = def.OutcomeApproved
	}
	return Status{}, true, e.finish(cur, outcome, dec.Comment)
}

func (e *Engine) commit(cur *Occurrence, n *def.Node, fr Frame) (Status, bool, error) {
	step := ""
	if n.Scope == "step" {
		step = fr.Inputs["step"]
	}
	files, hash, err := e.Env.Deviations(n.Scope, step)
	if err != nil {
		return Status{}, false, err
	}
	if len(files) == 0 {
		if err := e.Env.Commit(n.Scope, step, nil); err != nil {
			return Status{}, false, err
		}
		return Status{}, true, e.finish(cur, def.OutcomeDone, "")
	}
	dec, decided, err := e.decision(gateDeviation, cur.ID, hash)
	if err != nil {
		return Status{}, false, err
	}
	if !decided {
		req := GateRequest{Name: gateDeviation, Occurrence: cur.ID, Target: "changes outside the plan", Hash: hash, Files: files}
		return Status{Kind: StatusGate, Gate: &req}, false, e.openGate(req)
	}
	if !dec.Approved {
		return Status{}, true, e.finish(cur, def.OutcomeRejected, dec.Comment)
	}
	if err := e.Env.Commit(n.Scope, step, files); err != nil {
		return Status{}, false, err
	}
	return Status{}, true, e.finish(cur, def.OutcomeDone, dec.Comment)
}

func (e *Engine) openGate(req GateRequest) error {
	var cur GateRequest
	if err := getJSON(e.Store, prefixGateOpen+req.Name, &cur); err == nil && cur.Occurrence == req.Occurrence && cur.Hash == req.Hash {
		return nil
	}
	e.Env.Log(Event{Kind: "gate-open", Occurrence: req.Occurrence, Detail: req.Name})
	return putJSON(e.Store, prefixGateOpen+req.Name, req)
}

// decision takes the decision for gate if it answers this occurrence and
// this exact content. A decision made against anything else is dropped:
// it answered a question that is no longer being asked (ADR-0066).
func (e *Engine) decision(gate, occurrence, hash string) (Decision, bool, error) {
	var d Decision
	if err := getJSON(e.Store, prefixDecision+gate, &d); err != nil {
		return Decision{}, false, nil
	}
	if d.Occurrence != occurrence || d.Hash != hash {
		e.Env.Log(Event{Kind: "decision-refused", Occurrence: occurrence, Detail: gate})
		return Decision{}, false, e.Store.Delete(prefixDecision + gate)
	}
	if err := e.Store.Delete(prefixDecision + gate); err != nil {
		return Decision{}, false, err
	}
	return d, true, e.Store.Delete(prefixGateOpen + gate)
}

// Decide records a human's decision on the gate that is open now. It
// refuses a decision whose occurrence or hash does not match the open
// request, so an approval given while looking at older content never
// resolves a newer question.
func (e *Engine) Decide(gate string, d Decision) error {
	var req GateRequest
	if err := getJSON(e.Store, prefixGateOpen+gate, &req); err != nil {
		return fmt.Errorf("gate %s is not open", gate)
	}
	if req.Occurrence != d.Occurrence || req.Hash != d.Hash {
		return fmt.Errorf("gate %s: the decision was made against occurrence %s / %s, but the open request is %s / %s", gate, d.Occurrence, d.Hash, req.Occurrence, req.Hash)
	}
	return putJSON(e.Store, prefixDecision+gate, d)
}

// OpenGate returns the open request for a gate, for `masuda gate show`.
func (e *Engine) OpenGate(gate string) (GateRequest, bool) {
	var req GateRequest
	if err := getJSON(e.Store, prefixGateOpen+gate, &req); err != nil {
		return GateRequest{}, false
	}
	return req, true
}

// AgentOccurrence returns an agent occurrence that is still waiting for its
// report, with its agent definition. Outputs may only be written against
// such an occurrence.
func (e *Engine) AgentOccurrence(id string) (*Occurrence, *def.Agent, error) {
	var o Occurrence
	if err := getJSON(e.Store, prefixOcc+id, &o); err != nil {
		return nil, nil, fmt.Errorf("no occurrence %s", id)
	}
	if _, done := e.Store.Get(prefixResult + id); done {
		return nil, nil, fmt.Errorf("occurrence %s has already finished", id)
	}
	w := e.Set.Workflows[o.Workflow]
	n := w.Nodes[o.Node]
	if n.Type != def.TypeAgent {
		return nil, nil, fmt.Errorf("occurrence %s is not an agent task", id)
	}
	return &o, e.Set.Agents[n.Role], nil
}
