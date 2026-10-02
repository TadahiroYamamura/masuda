package main

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
)

func TestFormatGateTriageAndDeviation(t *testing.T) {
	tri := formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0003", Gate: "triage", Subject: []byte("line one\nline two")})
	for _, want := range []string{"concern", "  line one\n  line two\n", "masuda gate dismiss abc 0003", "masuda gate halt", "masuda gate redo"} {
		if !strings.Contains(tri, want) {
			t.Fatalf("triage gate lacks %q:\n%s", want, tri)
		}
	}
	dev := formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0004", Gate: "deviation", TargetHash: "h1", Subject: []byte("notes.txt\nsub/x.go\n")})
	for _, want := range []string{"files changed outside the plan", "  - notes.txt\n  - sub/x.go\n", "--hash h1 --file"} {
		if !strings.Contains(dev, want) {
			t.Fatalf("deviation gate lacks %q:\n%s", want, dev)
		}
	}
	decided := formatGate(&apiv1.Gate{Gate: "triage", Subject: []byte("x"), Decision: &apiv1.Decision{Outcome: "dismiss"}})
	if strings.Contains(decided, "decide with") || !strings.Contains(decided, "decision:    dismiss") {
		t.Fatalf("a decided gate shows the decision, not the commands:\n%s", decided)
	}
}

func TestListRow(t *testing.T) {
	now := time.Now()
	w := &apiv1.Workspace{
		Id: "abc", Branch: "feat/x", State: apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE,
		Activity:      &apiv1.Activity{Kind: apiv1.ActivityKind_ACTIVITY_KIND_WAITING_GATE, LastActivity: timestamppb.New(now.Add(-3 * time.Minute))},
		Position:      "approval approve (occ 0002)",
		OpenGates:     []string{"plan"},
		OpenQuestions: []string{"0005"},
	}
	got := listRow(w, now)
	want := "abc\tfeat/x\twaiting_gate\twaiting_gate 3m ago\tapproval approve (occ 0002)\tgate:plan,question:0005"
	if got != want {
		t.Fatalf("listRow:\n got %q\nwant %q", got, want)
	}
	for d, want := range map[time.Duration]string{5 * time.Second: "5s", 90 * time.Minute: "1h", 72 * time.Hour: "3d"} {
		if got := since(d); got != want {
			t.Fatalf("since(%v) = %s, want %s", d, got, want)
		}
	}
}
