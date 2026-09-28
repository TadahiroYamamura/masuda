package engine

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
)

// Stubs script a dry run (ADR-0077): what each agent reports, what each
// gate decides, how each check ends, and how many items each foreach gets.
// Anything not scripted takes the successful answer.
type Stubs struct {
	// Outcomes maps "<workflow>#<node>" to the outcomes an agent node
	// reports on successive entries.
	Outcomes map[string][]string `yaml:"outcomes"`
	// Gates maps a gate name to successive decisions: "approved" or
	// "rejected".
	Gates map[string][]string `yaml:"gates"`
	// Checks maps a check name to successive results: "passed" or
	// "failed".
	Checks map[string][]string `yaml:"checks"`
	// Items maps a foreach set (steps, perspectives, findings) to its
	// item count.
	Items map[string]int `yaml:"items"`
}

// MemStore is an in-memory Store for dry runs and tests.
type MemStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemStore() *MemStore { return &MemStore{m: map[string][]byte{}} }

func (s *MemStore) Get(k string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok
}

func (s *MemStore) Put(k string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = append([]byte(nil), v...)
	return nil
}

func (s *MemStore) Delete(k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, k)
	return nil
}

func (s *MemStore) List(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// StubEnv is an Env that touches nothing. It remembers committed steps so
// a dry run shows the resume behaviour of ADR-0080.
type StubEnv struct {
	Stubs     Stubs
	Events    []Event
	committed map[string]bool
	checkRuns map[string]int
	// Changes, when set, is what the worktree shows changed since the first
	// snapshot, to exercise the check on agents without Write/Edit.
	Changes []string
	// Feedback maps each path WriteFeedback returned to its text.
	Feedback  map[string]string
	snapshots int
}

func NewStubEnv(s Stubs) *StubEnv {
	return &StubEnv{Stubs: s, committed: map[string]bool{}, checkRuns: map[string]int{}, Feedback: map[string]string{}}
}

func (s *StubEnv) Data(name string) (string, bool, error) { return "stub://" + name, true, nil }

func (s *StubEnv) Items(over, from, since string) ([]Item, error) {
	n, ok := s.Stubs.Items[over]
	if !ok {
		n = 1
	}
	items := make([]Item, n)
	for i := range items {
		key := fmt.Sprintf("%s-%d", strings.TrimSuffix(over, "s"), i+1)
		items[i] = Item{Key: key, Path: "stub://" + key}
	}
	return items, nil
}

func (s *StubEnv) StepDone(key string) (bool, error) { return s.committed[key], nil }

func (s *StubEnv) RunCheck(name string) (bool, string, error) {
	seq := s.Stubs.Checks[name]
	i := s.checkRuns[name]
	s.checkRuns[name]++
	if i < len(seq) && seq[i] == "failed" {
		return false, "stub: check " + name + " failed", nil
	}
	return true, "", nil
}

func (s *StubEnv) Deviations(scope, step string) ([]string, string, error) { return nil, "", nil }

func (s *StubEnv) Commit(scope, step string, approved []string) error {
	if scope == "step" {
		s.committed[strings.TrimPrefix(step, "stub://")] = true
	}
	return nil
}

func (s *StubEnv) Publish([]string) error                   { return nil }
func (s *StubEnv) Discard([]string) error                   { return nil }
func (s *StubEnv) TargetHash(target string) (string, error) { return "stub-" + target, nil }
func (s *StubEnv) Snapshot() (string, error) {
	s.snapshots++
	return fmt.Sprintf("snap-%d", s.snapshots), nil
}

func (s *StubEnv) ChangedSince(snapshot string) ([]string, string, error) {
	if snapshot != "snap-1" {
		return nil, "", nil
	}
	return s.Changes, strings.Join(s.Changes, ","), nil
}

func (s *StubEnv) TreeSnapshot() (string, error)         { return "stub-tree", nil }
func (s *StubEnv) DiffSince(tree string) (string, error) { return "stub://fix-diff", nil }

func (s *StubEnv) HasOutput(name, occurrence string) bool       { return true }
func (s *StubEnv) OutputsDone(OutputContext, []string) error    { return nil }
func (s *StubEnv) ItemFinished(over, key, outcome string) error { return nil }
func (s *StubEnv) WriteFeedback(occ, text string) (string, error) {
	p := "stub://feedback/" + occ
	s.Feedback[p] = text
	return p, nil
}
func (s *StubEnv) Log(e Event) { s.Events = append(s.Events, e) }

// DryRun drives an engine to the end, answering every agent task and gate
// from the stubs. It returns one trace line per answer given.
func DryRun(e *Engine, s Stubs, limit int) ([]string, Status, error) {
	var trace []string
	used := map[string]int{}
	take := func(seq []string, key string) (string, bool) {
		i := used[key]
		used[key]++
		if i < len(seq) {
			return seq[i], true
		}
		return "", false
	}
	for i := 0; i < limit; i++ {
		st, err := e.Advance()
		if err != nil {
			return trace, st, err
		}
		switch st.Kind {
		case StatusAgent:
			key := st.Task.Workflow + "#" + st.Task.Node
			outcome, ok := take(s.Outcomes[key], "agent:"+key)
			if !ok {
				outcome = defaultOutcome(e.Set.Agents[st.Task.Role])
			}
			trace = append(trace, fmt.Sprintf("%s → %s", key, outcome))
			if err := e.Report(st.Task.Occurrence, outcome, "", ""); err != nil {
				return trace, st, err
			}
		case StatusGate:
			g := st.Gate
			answer, ok := take(s.Gates[g.Name], "gate:"+g.Name)
			approved := !ok || answer == def.OutcomeApproved
			trace = append(trace, fmt.Sprintf("gate %s → %s", g.Name, map[bool]string{true: "approved", false: "rejected"}[approved]))
			if err := e.Decide(g.Name, Decision{Occurrence: g.Occurrence, Hash: g.Hash, Approved: approved}); err != nil {
				return trace, st, err
			}
		default:
			return trace, st, nil
		}
	}
	return trace, Status{}, fmt.Errorf("dry run did not finish within %d steps", limit)
}

func defaultOutcome(a *def.Agent) string {
	if _, ok := a.Outcomes[def.OutcomeDone]; ok {
		return def.OutcomeDone
	}
	return a.OutcomeOrder[0]
}
