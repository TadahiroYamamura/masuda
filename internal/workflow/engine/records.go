// Package engine runs a def.Set. It keeps no position in memory: every
// call to Advance rebuilds where the run is from the records in the store
// and continues from there (ADR-0068). The records are one occurrence per
// node entry and one result per finished occurrence (ADR-0080), so the
// position is derived from what happened rather than stored as such.
package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Store is the subset of the state daemon's KV store the engine needs.
// Keys are "<namespace>:<path>".
type Store interface {
	Get(key string) ([]byte, bool)
	Put(key string, value []byte) error
	Delete(key string) error
	List(prefix string) []string
}

const (
	keySeq         = "wf:seq"
	prefixOcc      = "wf:occ/"
	prefixResult   = "wf:result/"
	prefixFrameEnd = "wf:frame-end/"
	prefixFrame    = "wf:frame/"
	prefixGateOpen = "wf:gate-open/"
	prefixDecision = "wf:gate-decision/"
	keyBlocked     = "wf:blocked"
	rootFrame      = "root"
	idWidth        = 7
	// fuse is the one limit users cannot configure (ADR-0067). The load-time
	// checks already rule out unbounded loops, so reaching it means an
	// engine bug, not a workflow that needs more room.
	fuse = 20000
)

// Occurrence is one entry into a node.
type Occurrence struct {
	ID       string `json:"id"`
	Frame    string `json:"frame"`
	Workflow string `json:"workflow"`
	Node     string `json:"node"`
	// Feedback is what the transition into this node carried, as a path to
	// the file holding it (ADR-0065, ADR-0077). Empty if none.
	Feedback string `json:"feedback,omitempty"`
	// Exhausted marks an entry beyond the node's max: the node does not
	// run and the occurrence finishes as exhausted at once.
	Exhausted bool `json:"exhausted,omitempty"`
	// Uncounted marks a re-entry a human ordered (triage), which does not
	// count toward the node's max.
	Uncounted bool `json:"uncounted,omitempty"`
	// Items is a foreach's item list, fixed when it is entered so that a
	// recomputed position sees the same list.
	Items      []Item `json:"items,omitempty"`
	ItemsFixed bool   `json:"items_fixed,omitempty"`
	// Snapshot is the worktree state recorded when an agent task is handed
	// out, to measure what the agent changed (ADR-0081).
	Snapshot string `json:"snapshot,omitempty"`
}

// Item is one element a foreach iterates over.
type Item struct {
	Key string `json:"key"`
	// Path is the file the item is passed as (ADR-0077).
	Path string `json:"path"`
}

// Result is how an occurrence finished.
type Result struct {
	Outcome string `json:"outcome"`
	// Feedback is the path of a file carrying what the next node should
	// know (a rejection comment, a failing check's log tail...).
	Feedback string `json:"feedback,omitempty"`
	// Invalid marks an agent report the engine refused (an undeclared
	// outcome, a missing output). The node runs again (ADR-0065).
	Invalid bool `json:"invalid,omitempty"`
	// Retry marks a node sent back to work by a triage decision.
	Retry bool `json:"retry,omitempty"`
}

// Frame is one running workflow file: the root, a called workflow, or one
// foreach iteration. Its inputs are resolved to paths when it starts.
type Frame struct {
	ID       string            `json:"id"`
	Workflow string            `json:"workflow"`
	Inputs   map[string]string `json:"inputs,omitempty"`
}

// records is everything the store holds about a run, loaded once per
// Advance.
type records struct {
	occs    []*Occurrence
	byID    map[string]*Occurrence
	byFrame map[string][]*Occurrence
	results map[string]*Result
}

func loadRecords(s Store) (*records, error) {
	r := &records{byID: map[string]*Occurrence{}, byFrame: map[string][]*Occurrence{}, results: map[string]*Result{}}
	for _, k := range s.List(prefixOcc) {
		var o Occurrence
		if err := getJSON(s, k, &o); err != nil {
			return nil, err
		}
		r.occs = append(r.occs, &o)
		r.byID[o.ID] = &o
	}
	sort.Slice(r.occs, func(i, j int) bool { return r.occs[i].ID < r.occs[j].ID })
	for _, o := range r.occs {
		r.byFrame[o.Frame] = append(r.byFrame[o.Frame], o)
	}
	for _, k := range s.List(prefixResult) {
		var res Result
		if err := getJSON(s, k, &res); err != nil {
			return nil, err
		}
		r.results[strings.TrimPrefix(k, prefixResult)] = &res
	}
	return r, nil
}

func (r *records) last(frame string) *Occurrence {
	occs := r.byFrame[frame]
	if len(occs) == 0 {
		return nil
	}
	return occs[len(occs)-1]
}

func getJSON(s Store, key string, v any) error {
	b, ok := s.Get(key)
	if !ok {
		return fmt.Errorf("%s: not found", key)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

func putJSON(s Store, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Put(key, b)
}

func childFrame(occID string) string       { return occID }
func iterFrame(occID string, i int) string { return fmt.Sprintf("%s.%d", occID, i) }
