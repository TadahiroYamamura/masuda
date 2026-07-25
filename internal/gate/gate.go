// Package gate manages the G1 (plan) / G2 (review) approval gates: reading the
// artifact a gate is judging, and writing the approve/reject marker a waiting
// orchestrator loop consumes to resume.
//
// This is the CLI-side half of ADR-0006's file-based fast path
// (`masuda plan/review approve|reject`). The chat path (`masuda plan/review chat`,
// where Claude itself writes the marker mid-conversation) lives in
// internal/sandbox — this package only defines the marker format both sides
// agree on.
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

// artifactPaths maps each gate to the file (relative to the worktree root) that
// masuda plan/review show prints — the thing a human reviews before deciding.
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

func (n Name) markerPath(worktreeDir string) string {
	return filepath.Join(worktreeDir, ".masuda-gate", string(n)+".json")
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

// Show returns the contents of the artifact gate n is judging.
func Show(worktreeDir string, n Name) (string, error) {
	rel, err := n.artifactPath()
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(filepath.Join(worktreeDir, rel))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", rel, err)
	}
	return string(content), nil
}

func writeMarker(worktreeDir string, n Name, m Marker) error {
	path := n.markerPath(worktreeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Approve writes an approved marker for gate n. feedback may be empty.
func Approve(worktreeDir string, n Name, feedback string) error {
	return writeMarker(worktreeDir, n, Marker{Status: Approved, Feedback: feedback, DecidedAt: time.Now()})
}

// Reject writes a rejected marker for gate n. feedback should explain what needs
// to change, since it's the only input the next investigation/implementation
// pass gets.
func Reject(worktreeDir string, n Name, feedback string) error {
	return writeMarker(worktreeDir, n, Marker{Status: Rejected, Feedback: feedback, DecidedAt: time.Now()})
}

// Read returns the current marker for gate n, or a zero-value Pending Marker if
// no decision has been recorded yet.
func Read(worktreeDir string, n Name) (Marker, error) {
	data, err := os.ReadFile(n.markerPath(worktreeDir))
	if os.IsNotExist(err) {
		return Marker{Status: Pending}, nil
	}
	if err != nil {
		return Marker{}, err
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return Marker{}, fmt.Errorf("parsing marker %s: %w", n.markerPath(worktreeDir), err)
	}
	return m, nil
}
