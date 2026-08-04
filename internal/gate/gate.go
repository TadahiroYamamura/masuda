// Package gate manages the G1 (plan) / G2 (review) approval gates: reading the
// artifact a gate is judging, and writing the approve/reject marker a waiting
// orchestrator loop consumes to resume.
//
// This is the CLI-side half of ADR-0006's file-based fast path
// (`masuda plan/review approve|reject`). The chat path (`masuda chat`,
// where Claude itself writes the marker mid-conversation) lives in
// internal/sandbox — this package only defines the marker format both sides
// agree on.
//
// All paths here are relative to a workspace's state directory (see
// internal/workspace), not the git worktree: plan/, final_report.md,
// DEVIATION.md, and the gate markers themselves are masuda's own control
// files and live outside the worktree entirely (roadmap step 7).
package gate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Name identifies a gate.
type Name string

const (
	Plan   Name = "plan"
	Review Name = "review"
)

// artifactPaths maps the review gate to the file (relative to the workspace
// state directory) that masuda review show prints verbatim. The plan gate
// has no single-file equivalent (ADR-0026 split it into plan/summary.md +
// plan/steps.json) and is rendered by renderPlan instead.
var artifactPaths = map[Name]string{
	Review: "review_results/final_report.md",
}

func (n Name) artifactPath() (string, error) {
	p, ok := artifactPaths[n]
	if !ok {
		return "", fmt.Errorf("unknown gate %q", n)
	}
	return p, nil
}

// planDir is the subdirectory (relative to a workspace's state directory)
// holding the plan artifacts (ADR-0026).
const planDir = "plan"

// planStepFile is one file plan/steps.json declares a step will touch —
// mirrors what phase 4's mechanical backstop (ADR-0010, scoped per-step by
// ADR-0027) reads on the Python side.
type planStepFile struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}

// planStep is one element of planData.Steps.
type planStep struct {
	Description string         `json:"description"`
	Files       []planStepFile `json:"files"`
}

// planData is the top-level shape of plan/steps.json (ADR-0028 wrapped it in
// an object, from a bare step array, to also carry ExpectedByproducts —
// glob patterns the planner predicts the build/test toolchain may generate
// as a side effect, e.g. "*__pycache__*"). Both fields are
// planner-authored and human-approved at G1, unlike anything the
// implementation subagent self-reports later.
type planData struct {
	Steps              []planStep `json:"steps"`
	ExpectedByproducts []string   `json:"expected_byproducts"`
}

// renderPlan assembles the human-facing Markdown `masuda plan show` prints
// from plan/summary.md's free prose and plan/steps.json's structured data
// (ADR-0026, ADR-0028) — deterministic string concatenation, no LLM
// involved, mirroring _render_plan_text() on the Python side
// (orchestrator/implement_review_graph.py).
func renderPlan(stateDir string) (string, error) {
	summary, err := os.ReadFile(filepath.Join(stateDir, planDir, "summary.md"))
	if err != nil {
		return "", fmt.Errorf("reading plan summary: %w", err)
	}
	stepsData, err := os.ReadFile(filepath.Join(stateDir, planDir, "steps.json"))
	if err != nil {
		return "", fmt.Errorf("reading plan steps: %w", err)
	}
	var data planData
	if err := json.Unmarshal(stepsData, &data); err != nil {
		return "", fmt.Errorf("parsing plan steps: %w", err)
	}
	steps := data.Steps

	var b strings.Builder
	b.Write(summary)

	// "変更するファイル一覧" is the dedup union of every step's files
	// (ADR-0026) rather than a separately-authored list, so it can never
	// drift out of sync with the per-step lists the backstop actually
	// checks against.
	b.WriteString("\n\n## 変更するファイル一覧\n\n")
	seen := make(map[string]bool)
	for _, step := range steps {
		for _, f := range step.Files {
			if seen[f.Path] {
				continue
			}
			seen[f.Path] = true
			fmt.Fprintf(&b, "- `%s`: %s\n", f.Path, f.Description)
		}
	}

	b.WriteString("\n## 実装のステップ分解\n\n")
	for i, step := range steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, step.Description)
		for _, f := range step.Files {
			fmt.Fprintf(&b, "   - `%s`: %s\n", f.Path, f.Description)
		}
	}

	// ADR-0028: shown so a human can sanity-check the prediction at G1,
	// same as the file list above — omitted entirely when the planner
	// didn't predict any (the common case for projects with no build/test
	// side effects worth calling out).
	if len(data.ExpectedByproducts) > 0 {
		b.WriteString("\n## 生成される可能性のある副産物ファイル（機械的バックストップの除外対象）\n\n")
		for _, pattern := range data.ExpectedByproducts {
			fmt.Fprintf(&b, "- `%s`\n", pattern)
		}
	}
	return b.String(), nil
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
// plan says.
func Show(stateDir string, n Name) (string, error) {
	var out string
	if n == Plan {
		rendered, err := renderPlan(stateDir)
		if err != nil {
			return "", err
		}
		out = rendered
	} else {
		rel, err := n.artifactPath()
		if err != nil {
			return "", err
		}
		content, err := os.ReadFile(filepath.Join(stateDir, rel))
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", rel, err)
		}
		out = string(content)
	}

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
