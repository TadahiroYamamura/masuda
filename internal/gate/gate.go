// Package gate manages the G1 (plan) / G2 (review) approval gates, plus the
// triage gate (ADR-0029): reading the artifact a gate is judging, and writing
// the marker a waiting orchestrator loop consumes to resume.
//
// This is the CLI-side half of ADR-0006's file-based fast path
// (`masuda plan/review approve|reject`). The chat path (`masuda chat`,
// where Claude itself writes the marker mid-conversation) lives in
// internal/sandbox — this package only defines the marker format both sides
// agree on. The triage gate is a deliberate exception to that chat path
// (ADR-0029): its Halted status has no approve/reject equivalent and no
// resolution route back through chat — see Halt below.
//
// Issue #35's phase A moves state to a workspace's daemon (see
// internal/statedaemon/mcpclient) only where every reader and writer is
// already trusted Go code (this package, orchestrator/*.py, or the CLI
// itself) -- gate markers and DEVIATION.md qualify. Artifacts a Claude
// subagent produces via its own Edit tool (plan/summary.md, plan/steps.json,
// INVESTIGATION.md, triage_concern.json, review_results/final_report.md)
// deliberately stay plain files for now: moving them would require giving
// those subagents an MCP write tool instead of Edit, a separate, larger
// piece of design work this phase doesn't attempt. Mixing the two here once
// already caused a real bug (subagents kept writing files while this package
// read from the daemon) — see Issue #35's design notes for the writer/reader
// trust boundary this split enforces.
package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpclient"
)

// Name identifies a gate.
type Name string

const (
	Plan   Name = "plan"
	Review Name = "review"
	Triage Name = "triage"
)

// gatePrefix/artifactPrefix are the daemon key namespaces this package
// writes (gate markers, DEVIATION.md — see the package doc for why the rest
// of the artifacts below stay plain files instead).
const (
	gatePrefix     = "gate:"
	artifactPrefix = "artifact:"
)

func (n Name) gateKey() string { return gatePrefix + string(n) }

// artifactPaths maps the review gate to the file (relative to stateDir) that
// `masuda review show` prints verbatim. The plan gate has no single-file
// equivalent (ADR-0026 split it into plan/summary.md + plan/steps.json) and
// is rendered by renderPlan instead. Written by the synthesize subagent's
// Edit tool (phase 5) — see the package doc — so this stays a plain file.
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

// planDir is the subdirectory (relative to stateDir) holding the plan
// artifacts (ADR-0026). Written by the planner subagent's Edit tool — see
// the package doc for why this stays files rather than daemon keys.
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
// as a side effect, e.g. "*__pycache__*"). Both fields are planner-authored
// and human-approved at G1, unlike anything the implementation subagent
// self-reports later.
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

// triageConcernFile is the self-report a subagent writes directly to the
// workspace state directory (ADR-0029) the moment it notices content that
// looks like it's trying to manipulate its behavior — in place of its normal
// deliverable, from any phase. renderTriageConcern is what `masuda triage
// show` renders from it. Written via Edit (or, in the Docker-sandboxed
// phase 4-5, Bash) by the reporting subagent — see the package doc for why
// this stays a plain file.
const triageConcernFile = "triage_concern.json"

// triageConcern is triageConcernFile's on-disk shape.
type triageConcern struct {
	Agent       string    `json:"agent"`
	Phase       string    `json:"phase"`
	Description string    `json:"description"`
	Evidence    string    `json:"evidence,omitempty"`
	ReportedAt  time.Time `json:"reported_at"`
}

// renderTriageConcern assembles the human-facing Markdown `masuda triage
// show` prints from triageConcernFile. Unlike renderPlan, there is no
// DEVIATION.md-style prepend logic here — the concern file itself is the
// entire artifact this gate is judging.
func renderTriageConcern(stateDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, triageConcernFile))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", triageConcernFile, err)
	}
	var c triageConcern
	if err := json.Unmarshal(data, &c); err != nil {
		return "", fmt.Errorf("parsing %s: %w", triageConcernFile, err)
	}
	var b strings.Builder
	b.WriteString("# Triage concern reported (ADR-0029)\n\n")
	fmt.Fprintf(&b, "**Reported by**: %s (%s)\n", c.Agent, c.Phase)
	fmt.Fprintf(&b, "**Reported at**: %s\n\n", c.ReportedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "## Description\n\n%s\n", c.Description)
	if c.Evidence != "" {
		fmt.Fprintf(&b, "\n## Evidence\n\n%s\n", c.Evidence)
	}
	return b.String(), nil
}

// deviationKey is the daemon key holding the reason phase 4's mechanical
// backstop (ADR-0010) or a self-reported deviation reopened G1 — see Show's
// Plan case. Written by write_task_md itself (orchestrator/*.py, Python),
// not by any subagent, so unlike the artifacts above it's safe to keep
// daemon-backed (see the package doc).
const deviationKey = artifactPrefix + "DEVIATION.md"

// Status is the decision a marker records. Halted is triage-only (ADR-0029)
// — a deliberately terminal outcome with no equivalent on the plan/review
// gates.
//
// There is no "pending": a gate that nobody has decided on has no marker at
// all. That is not just a spelling choice — a marker means "a decision is
// waiting to be taken", which is what lets the daemon answer "has this gate
// been resolved" by looking at whether the key exists, and what lets the
// consumer delete it as its acknowledgement.
type Status string

const (
	Approved Status = "approved"
	Rejected Status = "rejected"
	Halted   Status = "halted"
)

// Marker is the (JSON-encoded) value stored at a gate's key, recording a
// human decision.
type Marker struct {
	Status    Status    `json:"status"`
	Feedback  string    `json:"feedback,omitempty"`
	DecidedAt time.Time `json:"decided_at"`
}

func dial(ctx context.Context, stateDir string) (*mcpclient.Client, error) {
	return mcpclient.Dial(ctx, statedaemon.SocketPath(stateDir))
}

// Show returns the contents of the artifact gate n is judging. For the plan
// gate, if phase 4 reopened G1 (ADR-0010 — a self-reported deviation, an
// exhausted build/test retry, or the mechanical file-list backstop), the
// reason recorded at deviationKey is prepended so `masuda plan show` explains
// *why* the gate is open again, not just what the (still-approved-looking)
// plan says.
func Show(ctx context.Context, stateDir string, n Name) (string, error) {
	var out string
	switch n {
	case Plan:
		rendered, err := renderPlan(stateDir)
		if err != nil {
			return "", err
		}
		out = rendered
	case Triage:
		rendered, err := renderTriageConcern(stateDir)
		if err != nil {
			return "", err
		}
		out = rendered
	default:
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
		c, err := dial(ctx, stateDir)
		if err != nil {
			return "", err
		}
		defer c.Close()
		if deviation, found, err := c.Get(ctx, deviationKey); err == nil && found {
			out = "# G1 reopened — deviation reported (ADR-0010)\n\n" + deviation + "\n\n---\n\n" + out
		}
	}
	return out, nil
}

func writeMarker(ctx context.Context, c *mcpclient.Client, n Name, m Marker) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return c.Put(ctx, n.gateKey(), string(data))
}

// Approve writes an approved marker for gate n. feedback may be empty.
//
// It leaves deviationKey alone, as Reject and Halt do. Writing a decision
// and consuming one are different jobs: the reason a reopened G1 is open
// belongs to the decision until whoever acts on that decision takes both
// away together (orchestrator/implement_review_graph.py's
// _resolve_gate_reopen, in one state_apply). Clearing it here used to make
// the orchestrator see a gate with a decision but no reason, read that as
// "not opened yet", and reopen it -- which discarded the human's approval
// outright until ADR-0055, and still cost a wasted round after it.
func Approve(ctx context.Context, stateDir string, n Name, feedback string) error {
	c, err := dial(ctx, stateDir)
	if err != nil {
		return err
	}
	defer c.Close()
	return writeMarker(ctx, c, n, Marker{Status: Approved, Feedback: feedback, DecidedAt: time.Now()})
}

// Reject writes a rejected marker for gate n. feedback should explain what needs
// to change, since it's the only input the next investigation/implementation
// pass gets.
func Reject(ctx context.Context, stateDir string, n Name, feedback string) error {
	c, err := dial(ctx, stateDir)
	if err != nil {
		return err
	}
	defer c.Close()
	return writeMarker(ctx, c, n, Marker{Status: Rejected, Feedback: feedback, DecidedAt: time.Now()})
}

// Halt writes a halted marker for gate n (ADR-0029: triage-only in practice,
// but generic over Name like Approve/Reject). Halt's whole point is a dead
// end a human must investigate manually, so every other piece of on-disk
// state (including the triage_concern.json a human may still want to re-read
// via `masuda triage show`) is left exactly as found.
func Halt(ctx context.Context, stateDir string, n Name, reason string) error {
	c, err := dial(ctx, stateDir)
	if err != nil {
		return err
	}
	defer c.Close()
	return writeMarker(ctx, c, n, Marker{Status: Halted, Feedback: reason, DecidedAt: time.Now()})
}
