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
// As of Issue #35's phase A, every read/write here goes through a
// workspace's state daemon (internal/statedaemon/mcpclient) rather than
// touching files directly — the daemon is the sole source of truth for
// plan/, review_results/, gate markers, etc. Every function therefore takes
// stateDir (used only to locate the daemon's socket, via
// statedaemon.SocketPath) and a context to dial it with.
package gate

import (
	"context"
	"encoding/json"
	"fmt"
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
// reads/writes (Issue #35's key convention: "<namespace>:<rest>", rest
// mirroring today's file-relative paths so the mapping stays obvious).
const (
	gatePrefix     = "gate:"
	artifactPrefix = "artifact:"
)

func (n Name) gateKey() string { return gatePrefix + string(n) }

// artifactKeys maps the review gate to the key (see artifactPrefix) whose
// value `masuda review show` prints verbatim. The plan gate has no single-key
// equivalent (ADR-0026 split it into plan/summary.md + plan/steps.json) and
// is rendered by renderPlan instead.
var artifactKeys = map[Name]string{
	Review: artifactPrefix + "review_results/final_report.md",
}

func (n Name) artifactKey() (string, error) {
	k, ok := artifactKeys[n]
	if !ok {
		return "", fmt.Errorf("unknown gate %q", n)
	}
	return k, nil
}

// planSummaryKey/planStepsKey are the daemon keys renderPlan reads (ADR-0026
// split the plan artifact into these two pieces).
const (
	planSummaryKey = artifactPrefix + "plan/summary.md"
	planStepsKey   = artifactPrefix + "plan/steps.json"
)

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
	// Mode is "" (the default single-shot implement/backstop/commit flow) or
	// "tdd" (the Red/Green/Refactor sub-loop, Issue #3) — planner-assigned
	// when `masuda plan start --tdd` was passed, reviewable and correctable
	// by a human at G1 like every other planner judgment call (e.g. step
	// decomposition, file lists), so no separate validation is needed here.
	Mode string `json:"mode,omitempty"`
}

// planData is the top-level shape of the plan/steps.json artifact (ADR-0028
// wrapped it in an object, from a bare step array, to also carry
// ExpectedByproducts — glob patterns the planner predicts the build/test
// toolchain may generate as a side effect, e.g. "*__pycache__*"). Both
// fields are planner-authored and human-approved at G1, unlike anything the
// implementation subagent self-reports later.
type planData struct {
	Steps              []planStep `json:"steps"`
	ExpectedByproducts []string   `json:"expected_byproducts"`
}

// renderPlan assembles the human-facing Markdown `masuda plan show` prints
// from the plan summary key's free prose and the plan steps key's structured
// data (ADR-0026, ADR-0028) — deterministic string concatenation, no LLM
// involved, mirroring _render_plan_text() on the Python side
// (orchestrator/implement_review_graph.py).
func renderPlan(ctx context.Context, c *mcpclient.Client) (string, error) {
	summary, found, err := c.Get(ctx, planSummaryKey)
	if err != nil {
		return "", fmt.Errorf("reading plan summary: %w", err)
	}
	if !found {
		return "", fmt.Errorf("reading plan summary: not found")
	}
	stepsData, found, err := c.Get(ctx, planStepsKey)
	if err != nil {
		return "", fmt.Errorf("reading plan steps: %w", err)
	}
	if !found {
		return "", fmt.Errorf("reading plan steps: not found")
	}
	var data planData
	if err := json.Unmarshal([]byte(stepsData), &data); err != nil {
		return "", fmt.Errorf("parsing plan steps: %w", err)
	}
	steps := data.Steps

	var b strings.Builder
	b.WriteString(summary)

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
		label := step.Description
		if step.Mode == "tdd" {
			label += "（TDDモード）"
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, label)
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

// triageConcernKey is the self-report a subagent writes directly (ADR-0029)
// the moment it notices content that looks like it's trying to manipulate
// its behavior — in place of its normal deliverable, from any phase.
// renderTriageConcern is what `masuda triage show` renders from it.
const triageConcernKey = artifactPrefix + "triage_concern.json"

// triageConcern is triageConcernKey's value shape.
type triageConcern struct {
	Agent       string    `json:"agent"`
	Phase       string    `json:"phase"`
	Description string    `json:"description"`
	Evidence    string    `json:"evidence,omitempty"`
	ReportedAt  time.Time `json:"reported_at"`
}

// renderTriageConcern assembles the human-facing Markdown `masuda triage
// show` prints from triageConcernKey. Unlike renderPlan, there is no
// DEVIATION.md-style prepend logic here — the concern itself is the entire
// artifact this gate is judging.
func renderTriageConcern(ctx context.Context, c *mcpclient.Client) (string, error) {
	data, found, err := c.Get(ctx, triageConcernKey)
	if err != nil {
		return "", fmt.Errorf("reading triage concern: %w", err)
	}
	if !found {
		return "", fmt.Errorf("reading triage concern: not found")
	}
	var tc triageConcern
	if err := json.Unmarshal([]byte(data), &tc); err != nil {
		return "", fmt.Errorf("parsing triage concern: %w", err)
	}
	var b strings.Builder
	b.WriteString("# Triage concern reported (ADR-0029)\n\n")
	fmt.Fprintf(&b, "**Reported by**: %s (%s)\n", tc.Agent, tc.Phase)
	fmt.Fprintf(&b, "**Reported at**: %s\n\n", tc.ReportedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "## Description\n\n%s\n", tc.Description)
	if tc.Evidence != "" {
		fmt.Fprintf(&b, "\n## Evidence\n\n%s\n", tc.Evidence)
	}
	return b.String(), nil
}

// deviationKey is the reason phase 4's mechanical backstop (ADR-0010) or a
// self-reported deviation reopened G1 — see Show's Plan case.
const deviationKey = artifactPrefix + "DEVIATION.md"

// Status is one of the marker's possible states. Halted is triage-only
// (ADR-0029) — a fourth, deliberately terminal state with no equivalent on
// the plan/review gates.
type Status string

const (
	Pending  Status = "pending"
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
	c, err := dial(ctx, stateDir)
	if err != nil {
		return "", err
	}
	defer c.Close()

	var out string
	switch n {
	case Plan:
		rendered, err := renderPlan(ctx, c)
		if err != nil {
			return "", err
		}
		out = rendered
	case Triage:
		rendered, err := renderTriageConcern(ctx, c)
		if err != nil {
			return "", err
		}
		out = rendered
	default:
		key, err := n.artifactKey()
		if err != nil {
			return "", err
		}
		content, found, err := c.Get(ctx, key)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", key, err)
		}
		if !found {
			return "", fmt.Errorf("reading %s: not found", key)
		}
		out = content
	}

	if n == Plan {
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

// clearDeviation removes deviationKey if present — once a human has decided
// on a reopened G1, the reason that reopened it no longer needs to keep
// showing up on `masuda plan show`.
func clearDeviation(ctx context.Context, c *mcpclient.Client) error {
	return c.Delete(ctx, deviationKey)
}

// Approve writes an approved marker for gate n. feedback may be empty.
func Approve(ctx context.Context, stateDir string, n Name, feedback string) error {
	c, err := dial(ctx, stateDir)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := clearDeviation(ctx, c); err != nil {
		return err
	}
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
	if err := clearDeviation(ctx, c); err != nil {
		return err
	}
	return writeMarker(ctx, c, n, Marker{Status: Rejected, Feedback: feedback, DecidedAt: time.Now()})
}

// Halt writes a halted marker for gate n (ADR-0029: triage-only in practice,
// but generic over Name like Approve/Reject). Unlike Approve/Reject, this
// deliberately clears nothing — halt's whole point is a dead end a human
// must investigate manually, so every other piece of state (including the
// triage concern a human may still want to re-read via `masuda triage
// show`) is left exactly as found.
func Halt(ctx context.Context, stateDir string, n Name, reason string) error {
	c, err := dial(ctx, stateDir)
	if err != nil {
		return err
	}
	defer c.Close()
	return writeMarker(ctx, c, n, Marker{Status: Halted, Feedback: reason, DecidedAt: time.Now()})
}

// Read returns the current marker for gate n, or a zero-value Pending Marker
// if no decision has been recorded yet.
func Read(ctx context.Context, stateDir string, n Name) (Marker, error) {
	c, err := dial(ctx, stateDir)
	if err != nil {
		return Marker{}, err
	}
	defer c.Close()

	data, found, err := c.Get(ctx, n.gateKey())
	if err != nil {
		return Marker{}, err
	}
	if !found {
		return Marker{Status: Pending}, nil
	}
	var m Marker
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return Marker{}, fmt.Errorf("parsing marker %s: %w", n.gateKey(), err)
	}
	return m, nil
}
