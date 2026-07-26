// Package gate manages the G1 (plan) / G2 (review) approval gates: reading the
// artifact a gate is judging, and writing the approve/reject marker a waiting
// orchestrator loop consumes to resume.
//
// This is the CLI-side half of ADR-0006's file-based fast path
// (`masuda plan/review approve|reject`). The chat path (`masuda plan/review chat`,
// where Claude itself writes the marker mid-conversation) lives in
// internal/sandbox — this package only defines the marker format both sides
// agree on.
//
// All paths here are relative to a workspace's state directory (see
// internal/workspace), not the git worktree: PLAN.md, final_report.md,
// DEVIATION.md, and the gate markers themselves are masuda's own control
// files and live outside the worktree entirely (roadmap step 7).
package gate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Name identifies a gate.
type Name string

const (
	Plan   Name = "plan"
	Review Name = "review"
)

// artifactPaths maps each gate to the file (relative to the workspace state
// directory) that masuda plan/review show prints — the thing a human
// reviews before deciding.
var artifactPaths = map[Name]string{
	Plan:   "PLAN.md",
	Review: "review_results/final_report.md",
}

func (n Name) artifactPath() (string, error) {
	p, ok := artifactPaths[n]
	if !ok {
		return "", fmt.Errorf("unknown gate %q", n)
	}
	return p, nil
}

func (n Name) markerPath(stateDir string) string {
	return filepath.Join(stateDir, ".masuda-gate", string(n)+".json")
}

// Status is one of the marker's possible states.
type Status string

const (
	Pending  Status = "pending"
	Approved Status = "approved"
	Rejected Status = "rejected"
)

// Marker is the on-disk (JSON) record of a gate's human decision.
type Marker struct {
	Status    Status    `json:"status"`
	Feedback  string    `json:"feedback,omitempty"`
	DecidedAt time.Time `json:"decided_at"`
}

// Show returns the contents of the artifact gate n is judging. For the plan
// gate, if phase 4 reopened G1 (ADR-0010 — a self-reported deviation, an
// exhausted build/test retry, or the mechanical file-list backstop), the
// reason recorded in DEVIATION.md is prepended so `masuda plan show` explains
// *why* the gate is open again, not just what the (still-approved-looking)
// PLAN.md says.
func Show(stateDir string, n Name) (string, error) {
	rel, err := n.artifactPath()
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(filepath.Join(stateDir, rel))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", rel, err)
	}
	out := string(content)

	if n == Plan {
		if deviation, err := os.ReadFile(filepath.Join(stateDir, "DEVIATION.md")); err == nil {
			out = "# G1 reopened — deviation reported (ADR-0010)\n\n" + string(deviation) + "\n\n---\n\n" + out
		}
	}
	return out, nil
}

func writeMarker(stateDir string, n Name, m Marker) error {
	path := n.markerPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// clearDeviation removes DEVIATION.md if present — once a human has decided
// on a reopened G1, the reason that reopened it no longer needs to keep
// showing up on `masuda plan show`.
func clearDeviation(stateDir string) error {
	err := os.Remove(filepath.Join(stateDir, "DEVIATION.md"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Approve writes an approved marker for gate n. feedback may be empty.
func Approve(stateDir string, n Name, feedback string) error {
	if err := clearDeviation(stateDir); err != nil {
		return err
	}
	return writeMarker(stateDir, n, Marker{Status: Approved, Feedback: feedback, DecidedAt: time.Now()})
}

// Reject writes a rejected marker for gate n. feedback should explain what needs
// to change, since it's the only input the next investigation/implementation
// pass gets.
func Reject(stateDir string, n Name, feedback string) error {
	if err := clearDeviation(stateDir); err != nil {
		return err
	}
	return writeMarker(stateDir, n, Marker{Status: Rejected, Feedback: feedback, DecidedAt: time.Now()})
}

// Read returns the current marker for gate n, or a zero-value Pending Marker if
// no decision has been recorded yet.
func Read(stateDir string, n Name) (Marker, error) {
	data, err := os.ReadFile(n.markerPath(stateDir))
	if os.IsNotExist(err) {
		return Marker{Status: Pending}, nil
	}
	if err != nil {
		return Marker{}, err
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return Marker{}, fmt.Errorf("parsing marker %s: %w", n.markerPath(stateDir), err)
	}
	return m, nil
}
