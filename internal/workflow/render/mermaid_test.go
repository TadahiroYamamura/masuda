package render

import (
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
)

func TestMermaidDrawsUserNodesAndEngineInsertions(t *testing.T) {
	src := `version: 1
start: approve
nodes:
  approve:
    type: approval
    gate: plan
    target: plan
    next: {approved: commit, rejected: end:rejected}
  commit:
    type: commit
    scope: plan
    next: {done: end, rejected: approve}
`
	w, errs := def.ParseWorkflow("workflows/t", []byte(src))
	if len(errs) != 0 {
		t.Fatalf("parse: %v", errs)
	}
	got := Mermaid(w)
	for _, want := range []string{
		"flowchart TD",
		"n_approve -->|approved| n_commit",
		"n_commit -->|done| end_done",
		"end_rejected(((\"end:rejected\")))",
		"deviation approval",
		"triage",
		"class n_approve human",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("diagram lacks %q:\n%s", want, got)
		}
	}
}
