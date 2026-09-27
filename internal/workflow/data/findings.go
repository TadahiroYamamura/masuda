package data

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding is one review finding as an agent writes it: ADR-0020's location
// fields, plus whether it is a mechanical one to fix automatically
// (ADR-0003, ADR-0082).
type Finding struct {
	File        string `json:"file"`
	StartLine   int    `json:"startLine"`
	EndLine     int    `json:"endLine"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
	Suggestion  string `json:"suggestion,omitempty"`
	Autofix     bool   `json:"autofix"`
}

// Status is where a recorded finding stands.
type Status string

const (
	StatusOpen       Status = "open"
	StatusResolved   Status = "resolved"
	StatusUnresolved Status = "unresolved"
)

// Record is a finding as the engine keeps it in findings.json: what the
// agent wrote plus where it came from and where it stands.
type Record struct {
	ID string `json:"id"`
	Finding
	Workflow    string `json:"workflow"`
	Perspective string `json:"perspective,omitempty"`
	// AgentID is the Claude Code agent that wrote it, used to resume that
	// agent for the recheck (ADR-0074). Empty when unknown.
	AgentID    string `json:"agentId,omitempty"`
	Occurrence string `json:"occurrence"`
	// Source is the frame and node that produced it; a later occurrence of
	// the same source in the same frame replaces its findings (ADR-0082).
	Source string `json:"source"`
	Status Status `json:"status"`
}

var severities = map[string]bool{"高": true, "中": true, "低": true}

// ParseFindings validates what an agent wrote. An empty array is valid:
// finding nothing is a correct result.
func ParseFindings(b []byte) ([]Finding, error) {
	var fs []Finding
	if err := strictJSON(b, &fs); err != nil {
		return nil, fmt.Errorf("findings must be a JSON array of {file, startLine, endLine, severity, description, suggestion, autofix}: %w", err)
	}
	var problems []string
	for i, f := range fs {
		if !cleanRelPath(f.File) {
			problems = append(problems, fmt.Sprintf("[%d].file %q must be a relative path inside the repository", i, f.File))
		}
		if f.StartLine < 1 || f.EndLine < f.StartLine {
			problems = append(problems, fmt.Sprintf("[%d] needs 1 <= startLine <= endLine", i))
		}
		if !severities[f.Severity] {
			problems = append(problems, fmt.Sprintf("[%d].severity must be 高, 中 or 低", i))
		}
		if strings.TrimSpace(f.Description) == "" {
			problems = append(problems, fmt.Sprintf("[%d].description is empty", i))
		}
	}
	if len(problems) > 0 {
		return nil, errors.New("findings are invalid: " + strings.Join(problems, "; "))
	}
	return fs, nil
}

// Ledger is the workspace's findings.json: every finding from every review,
// interim and final, in one place (ADR-0082).
type Ledger struct {
	File string
}

func (l Ledger) Load() ([]Record, error) {
	b, err := os.ReadFile(l.File)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rs []Record
	if err := json.Unmarshal(b, &rs); err != nil {
		return nil, fmt.Errorf("%s: %w", l.File, err)
	}
	return rs, nil
}

func (l Ledger) save(rs []Record) error {
	b, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.File), 0o755); err != nil {
		return err
	}
	tmp := l.File + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.File)
}

// Import adds the findings one occurrence wrote, first dropping those an
// earlier occurrence of the same source wrote: a reviewer sent back as
// inaccurate rewrites its findings rather than adding to them.
func (l Ledger) Import(template Record, fs []Finding) error {
	rs, err := l.Load()
	if err != nil {
		return err
	}
	kept := rs[:0]
	for _, r := range rs {
		if r.Source != template.Source {
			kept = append(kept, r)
		}
	}
	for i, f := range fs {
		r := template
		r.Finding = f
		r.ID = fmt.Sprintf("%s-%03d", template.Occurrence, i+1)
		r.Status = StatusOpen
		kept = append(kept, r)
	}
	return l.save(kept)
}

// SetStatus records how a finding ended up.
func (l Ledger) SetStatus(id string, s Status) error {
	rs, err := l.Load()
	if err != nil {
		return err
	}
	for i := range rs {
		if rs[i].ID == id {
			rs[i].Status = s
			return l.save(rs)
		}
	}
	return fmt.Errorf("no finding %s", id)
}

// ToFix lists the open, auto-fixable findings imported at or after the
// occurrence since (ADR-0082): what `over: findings` iterates.
func (l Ledger) ToFix(since string) ([]Record, error) {
	rs, err := l.Load()
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range rs {
		if r.Status == StatusOpen && r.Autofix && r.Occurrence >= since {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
