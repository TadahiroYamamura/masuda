package main

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
)

func TestFormatGateTriageAndDeviation(t *testing.T) {
	tri := formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0003", Gate: "triage", Subject: []byte("line one\nline two")}, nil)
	for _, want := range []string{"concern", "  line one\n  line two\n", "masuda gate dismiss abc 0003", "masuda gate halt", "masuda gate redo"} {
		if !strings.Contains(tri, want) {
			t.Fatalf("triage gate lacks %q:\n%s", want, tri)
		}
	}
	dev := formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0004", Gate: "deviation", TargetHash: "h1", Subject: []byte("notes.txt\nsub/x.go\n")}, nil)
	for _, want := range []string{"files changed outside the plan", "  - notes.txt\n  - sub/x.go\n", "--hash h1 --file"} {
		if !strings.Contains(dev, want) {
			t.Fatalf("deviation gate lacks %q:\n%s", want, dev)
		}
	}
	decided := formatGate(&apiv1.Gate{Gate: "triage", Subject: []byte("x"), Decision: &apiv1.Decision{Outcome: "dismiss"}}, nil)
	if strings.Contains(decided, "decide with") || !strings.Contains(decided, "decision:    dismiss") {
		t.Fatalf("a decided gate shows the decision, not the commands:\n%s", decided)
	}
}

// diffとstep-diffはどちらもunified diffなので、承認して確定するものの違いを見出しで出し分ける。
func TestFormatGateDiffTargets(t *testing.T) {
	review := formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0007", Gate: "review", Target: "diff", TargetHash: "h", Subject: []byte("diff --git a/x b/x")}, nil)
	interim := formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0005", Gate: "interim", Target: "step-diff", TargetHash: "h", StagingCommit: "c1", Subject: []byte("diff --git a/y b/y")}, nil)
	for _, tc := range []struct{ out, want, not string }{
		{review, "changes to be published", "this step will commit"},
		{review, "publishされる内容", "これからcommit"},
		{interim, "changes this step will commit", "to be published"},
		{interim, "これからcommitされる内容", "publishされる内容"},
		{interim, "c1（作業ツリーのスナップショット", ""},
	} {
		if !strings.Contains(tc.out, tc.want) || (tc.not != "" && strings.Contains(tc.out, tc.not)) {
			t.Fatalf("want %q and not %q in:\n%s", tc.want, tc.not, tc.out)
		}
	}
}

// staging_commitのあるゲートでは、人間のコメントを差分の後・判断コマンドの前に出す。エージェントの指摘は出さない。
func TestFormatGateHumanComments(t *testing.T) {
	g := &apiv1.Gate{WorkspaceId: "abc", Occurrence: "0007", Gate: "review", Target: "diff", TargetHash: "h", StagingCommit: "c1", Subject: []byte("diff --git a/x b/x")}
	comments := []*apiv1.Comment{
		{Path: "x.go", Line: 3, Author: "human", Body: "ここは消す"},
		{Path: "x.go", Line: 4, Author: "correctness", Body: "agent finding", Severity: "中"},
		{Path: "y.go", Line: 1, Author: "human", Body: "名前を変える"},
	}
	got := formatGate(g, comments)
	want := "diff --git a/x b/x\n" +
		"\ncomments (sent to the agent on reject):\n" +
		"  x.go:3: ここは消す\n" +
		"  y.go:1: 名前を変える\n" +
		"\napprove: masuda gate approve abc 0007 --hash h [--comment <text>]\n" +
		"reject:  masuda gate reject abc 0007 [--comment <text>]\n" +
		"comment: masuda gate comment abc 0007 <path>:<line> <text>\n"
	if !strings.HasSuffix(got, want) || strings.Contains(got, "agent finding") {
		t.Fatalf("got:\n%s\nwant suffix:\n%s", got, want)
	}

	// 人間のコメントが無ければ見出しごと省く。判断済みならcommentのコマンドも出さない
	none := formatGate(g, comments[1:2])
	if strings.Contains(none, "comments (sent") || !strings.Contains(none, "comment: masuda gate comment") {
		t.Fatalf("without human comments:\n%s", none)
	}
	decided := proto.Clone(g).(*apiv1.Gate)
	decided.Decision = &apiv1.Decision{Outcome: "rejected"}
	if out := formatGate(decided, comments); !strings.Contains(out, "x.go:3: ここは消す") || strings.Contains(out, "masuda gate comment") {
		t.Fatalf("decided gate:\n%s", out)
	}
	// staging_commitの無いゲートにはcommentのコマンドを出さない
	if out := formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0007", Gate: "review", Target: "diff", TargetHash: "h"}, nil); strings.Contains(out, "masuda gate comment") {
		t.Fatalf("gate without staging_commit:\n%s", out)
	}
}

func TestParseLocation(t *testing.T) {
	if p, l, err := parseLocation("a:b/x.go:12"); err != nil || p != "a:b/x.go" || l != 12 {
		t.Fatalf("parseLocation: %q %d %v", p, l, err)
	}
	for _, bad := range []string{"x.go", "x.go:0", "x.go:a", ":3"} {
		if _, _, err := parseLocation(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
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

const newSchemaPlan = `{"goal":"三角形の面積と周長を求めるモジュールを追加する","summary":"shapes/triangle.py を新規追加する。\nテストは unittest で実行する","steps":[{"number":1,"title":"三角形モジュールの実装","description":"_check で辺を検証する。\narea はヘロンの公式","tests":["3,4,5 で area が 6","負の辺で ValueError"],"files":["shapes/triangle.py","tests/test_triangle.py"]},{"number":2,"title":"READMEの更新","description":"使い方を書く。ドキュメントのみなのでテストは無い","tests":[],"files":["README.md"]}],"alternatives":[{"option":"退化三角形を許容する (<=)","reason":"面積 0 が無意味"}],"risks":["浮動小数の境界誤差は未対応"],"expected_byproducts":["**/__pycache__/**","**/*.pyc"]}`

func TestFormatGatePlan(t *testing.T) {
	got := formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0003", Gate: "plan", Target: "plan", TargetHash: "h", Subject: []byte(newSchemaPlan)}, nil)
	want := `
goal: 三角形の面積と周長を求めるモジュールを追加する

summary:
  shapes/triangle.py を新規追加する。
  テストは unittest で実行する

steps:
  1. 三角形モジュールの実装
     _check で辺を検証する。
     area はヘロンの公式
     tests:
       - 3,4,5 で area が 6
       - 負の辺で ValueError
     files: shapes/triangle.py, tests/test_triangle.py
  2. READMEの更新
     使い方を書く。ドキュメントのみなのでテストは無い
     files: README.md

alternatives (considered, not taken):
  - 退化三角形を許容する (<=): 面積 0 が無意味

risks:
  - 浮動小数の境界誤差は未対応

expected byproducts: **/__pycache__/**, **/*.pyc

approve: masuda gate approve abc 0003 --hash h [--comment <text>]
`
	if !strings.Contains(got, want) {
		t.Fatalf("plan gate:\n%s\nwant to contain:\n%s", got, want)
	}
	if strings.Contains(got, `"goal"`) {
		t.Fatalf("the raw JSON must not be shown:\n%s", got)
	}
}

// 既存ワークスペースの記録には旧スキーマ（goal・title・tests・alternatives・risksが無い）の計画が残る。
func TestFormatGatePlanOldSchema(t *testing.T) {
	old := `{"summary":"アプローチ: 追加する","steps":[{"number":1,"description":"shapes/triangle.py を新規追加","files":["shapes/triangle.py"]},{"number":2,"description":"テストを追加","files":["tests/test_triangle.py"]}],"expected_byproducts":[]}`
	got := formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0003", Gate: "plan", Target: "plan", TargetHash: "h", Subject: []byte(old)}, nil)
	want := "\nsummary:\n  アプローチ: 追加する\n\nsteps:\n  1.\n     shapes/triangle.py を新規追加\n     files: shapes/triangle.py\n  2.\n     テストを追加\n     files: tests/test_triangle.py\n\napprove:"
	if !strings.Contains(got, want) {
		t.Fatalf("old-schema plan:\n%s\nwant to contain:\n%q", got, want)
	}
	for _, not := range []string{"goal:", "tests:", "alternatives", "risks:", "expected byproducts"} {
		if strings.Contains(got, not) {
			t.Fatalf("absent sections must be omitted (%q):\n%s", not, got)
		}
	}
}

// 計画として解けない中身は、内容を隠さないようそのまま出す。
func TestFormatGatePlanFallback(t *testing.T) {
	for _, subject := range []string{"not json at all", `{"summary":"s"}`} {
		got := formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0003", Gate: "plan", Target: "plan", TargetHash: "h", Subject: []byte(subject)}, nil)
		if !strings.Contains(got, "\n"+subject+"\n") || strings.Contains(got, "steps:") {
			t.Fatalf("fallback for %q:\n%s", subject, got)
		}
	}
}
