package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

const (
	prefixConcern = "wf:concern/"
	gateTriage    = "triage"
)

type concern struct {
	Description string `json:"description"`
}

// ReportConcern records a security concern an agent raised while working
// (ADR-0029). It is stored on the host side only, so the agent cannot
// rewrite what it reported after the fact.
func (e *Engine) ReportConcern(occurrence, description string) error {
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("describe the concern")
	}
	if _, _, err := e.AgentOccurrence(occurrence); err != nil {
		return err
	}
	e.Env.Log(Event{Kind: "concern", Occurrence: occurrence, Detail: description})
	return putJSON(e.Store, prefixConcern+occurrence, concern{Description: description})
}

// triage handles the oldest unresolved concern. It returns an empty Status
// and progressed=false when there is none.
func (e *Engine) triage(r *records) (Status, bool, error) {
	keys := e.Store.List(prefixConcern)
	if len(keys) == 0 {
		return Status{}, false, nil
	}
	sort.Strings(keys)
	occID := strings.TrimPrefix(keys[0], prefixConcern)
	var c concern
	if err := getJSON(e.Store, keys[0], &c); err != nil {
		return Status{}, false, err
	}
	sum := sha256.Sum256([]byte(occID + "\x00" + c.Description))
	hash := hex.EncodeToString(sum[:])
	dec, decided, err := e.decision(gateTriage, occID, hash)
	if err != nil {
		return Status{}, false, err
	}
	if !decided {
		req := GateRequest{Name: gateTriage, Occurrence: occID, Target: "security concern", Hash: hash, Detail: c.Description}
		return Status{Kind: StatusGate, Gate: &req}, false, e.openGate(req)
	}
	if err := e.Store.Delete(keys[0]); err != nil {
		return Status{}, false, err
	}
	// The agent stopped when it reported; whatever it claimed afterwards
	// no longer stands.
	_ = e.Store.Delete(prefixReport + occID)
	if dec.Halt {
		reason := fmt.Sprintf("halted at triage: %s", c.Description)
		if dec.Comment != "" {
			reason += " — " + dec.Comment
		}
		return Status{}, true, e.Store.Put(keyBlocked, []byte(reason))
	}
	if _, finished := r.results[occID]; finished {
		return Status{}, true, nil
	}
	text := "報告されたセキュリティ上の懸念は、人間が確認して誤検知と判断した。作業を続けよ。"
	if !dec.Approved {
		text = "報告されたセキュリティ上の懸念について、人間から次の指示があった。これに従って作業をやり直せ。\n\n" + dec.Comment
	}
	p, err := e.Env.WriteFeedback(occID, text)
	if err != nil {
		return Status{}, false, err
	}
	o := r.byID[occID]
	e.Env.Log(Event{Kind: "finish", Occurrence: occID, Workflow: o.Workflow, Node: o.Node, Outcome: "triage-retry"})
	return Status{}, true, putJSON(e.Store, prefixResult+occID, Result{Feedback: p, Retry: true})
}
