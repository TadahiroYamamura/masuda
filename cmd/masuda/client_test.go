package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
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
		{review, "what will be published", "what this step will commit"},
		{interim, "changes this step will commit", "to be published"},
		{interim, "what this step will commit", "what will be published"},
		{interim, "c1 (snapshot of the work tree", ""},
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
	done := &apiv1.Workspace{
		Id: "def", Branch: "fix/y", State: apiv1.WorkspaceState_WORKSPACE_STATE_DONE,
		Position: "done", Outcome: "needs_human", Reason: "どちらの挙動に揃えるか決めてください\n詳細",
	}
	if got, want := listRow(done, now), "def\tfix/y\tdone\t-\toutcome needs_human: どちらの挙動に揃えるか決めてください\t-"; got != want {
		t.Fatalf("listRow(needs_human):\n got %q\nwant %q", got, want)
	}
	// 起動中はPOSITIONが空なので、代わりに起動の段階（activityのdetail）を出す
	starting := &apiv1.Workspace{
		Id: "ghi", Branch: "feat/z", State: apiv1.WorkspaceState_WORKSPACE_STATE_STARTING,
		Activity: &apiv1.Activity{Kind: apiv1.ActivityKind_ACTIVITY_KIND_IDLE, Detail: "building image (log: /r/image-build.log)"},
	}
	if got, want := listRow(starting, now), "ghi\tfeat/z\tstarting\tidle\tbuilding image (log: /r/image-build.log)\t-"; got != want {
		t.Fatalf("listRow(starting):\n got %q\nwant %q", got, want)
	}
	ev := &apiv1.WorkspaceEvent{Seq: 3, Time: timestamppb.New(now), WorkspaceId: "ghi", Event: &apiv1.WorkspaceEvent_Status{Status: starting}}
	if got := formatEvent(ev); !strings.HasSuffix(got, "status starting idle building image (log: /r/image-build.log)") {
		t.Fatalf("formatEvent(starting) = %q", got)
	}
	for d, want := range map[time.Duration]string{5 * time.Second: "5s", 90 * time.Minute: "1h", 72 * time.Hour: "3d"} {
		if got := since(d); got != want {
			t.Fatalf("since(%v) = %s, want %s", d, got, want)
		}
	}
}

const newSchemaPlan = `{"goal":"三角形の面積と周長を求めるモジュールを追加する","summary":"shapes/triangle.py を新規追加する。\nテストは unittest で実行する","steps":[{"number":1,"title":"三角形モジュールの実装","description":"_check で辺を検証する。\narea はヘロンの公式","tests":["3,4,5 で area が 6","負の辺で ValueError"],"files":["shapes/triangle.py","tests/test_triangle.py"]},{"number":2,"title":"READMEの更新","description":"使い方を書く。ドキュメントのみなのでテストは無い","tests":[],"files":["README.md"]}],"alternatives":[{"option":"退化三角形を許容する (<=)","reason":"面積 0 が無意味"}],"risks":["浮動小数の境界誤差は未対応"],"expected_byproducts":["**/__pycache__/**","**/*.pyc"],"checks":[]}`

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

// 計画の問いと答え（checks）はstepsの後、alternativesの前に、statusの印付きで出す。答えが空なら2行目を省く。
func TestFormatGatePlanChecks(t *testing.T) {
	plan := strings.Replace(newSchemaPlan, `"checks":[]`, `"checks":[`+
		`{"id":"SPEC-1","category":"spec","question":"退化三角形（1,2,3）を不正として扱うか","answer":"ステップ1の _check で a+b>c の厳密不等式を要求する","status":"addressed"},`+
		`{"id":"PERFORMANCE-1","category":"performance","question":"大きな入力で遅くならないか","answer":"定数時間の計算なので範囲外。\n入力の大きさに依らない","status":"out_of_scope"},`+
		`{"id":"REGRESSION-2","category":"regression","question":"既存の shapes/circle.py の呼び出し元に影響は無いか","answer":"","status":"open"}]`, 1)
	got := formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0003", Gate: "plan", Target: "plan", TargetHash: "h", Subject: []byte(plan)}, nil)
	want := `     files: README.md

checks (questions raised about the plan, with the planner's answers):
  SPEC-1 [addressed] 退化三角形（1,2,3）を不正として扱うか
      ステップ1の _check で a+b>c の厳密不等式を要求する
  PERFORMANCE-1 [out_of_scope] 大きな入力で遅くならないか
      定数時間の計算なので範囲外。
      入力の大きさに依らない
  REGRESSION-2 [open] 既存の shapes/circle.py の呼び出し元に影響は無いか

alternatives (considered, not taken):
`
	if !strings.Contains(got, want) {
		t.Fatalf("plan gate with checks:\n%s\nwant to contain:\n%s", got, want)
	}
	if strings.Contains(newSchemaPlanGate(t), "checks (") {
		t.Fatal("an empty checks array must omit the section")
	}
}

func newSchemaPlanGate(t *testing.T) string {
	t.Helper()
	return formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0003", Gate: "plan", Target: "plan", TargetHash: "h", Subject: []byte(newSchemaPlan)}, nil)
}

// 既存ワークスペースの記録には旧スキーマ（goal・title・tests・alternatives・risksが無い）の計画が残る。
func TestFormatGatePlanOldSchema(t *testing.T) {
	old := `{"summary":"アプローチ: 追加する","steps":[{"number":1,"description":"shapes/triangle.py を新規追加","files":["shapes/triangle.py"]},{"number":2,"description":"テストを追加","files":["tests/test_triangle.py"]}],"expected_byproducts":[]}`
	got := formatGate(&apiv1.Gate{WorkspaceId: "abc", Occurrence: "0003", Gate: "plan", Target: "plan", TargetHash: "h", Subject: []byte(old)}, nil)
	want := "\nsummary:\n  アプローチ: 追加する\n\nsteps:\n  1.\n     shapes/triangle.py を新規追加\n     files: shapes/triangle.py\n  2.\n     テストを追加\n     files: tests/test_triangle.py\n\napprove:"
	if !strings.Contains(got, want) {
		t.Fatalf("old-schema plan:\n%s\nwant to contain:\n%q", got, want)
	}
	for _, not := range []string{"goal:", "tests:", "checks (", "alternatives", "risks:", "expected byproducts"} {
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

// developのplan-interviewerは計画の問いを1回のask_humanでまとめて聞き、本文は問いと理由の複数行になる。
func TestFormatQuestionWithSeveralItems(t *testing.T) {
	got := formatQuestion(&apiv1.OpenQuestion{WorkspaceId: "abc", Occurrence: "0007", Items: []*apiv1.QuestionItem{
		{Id: "SPEC-1", Text: "退化三角形を不正として扱うか\n理由: 指示書に記述が無い"},
		{Id: "REGRESSION-2", Text: "circle.py の呼び出し元に影響は無いか", Options: []string{"yes", "no"}},
	}})
	want := `  SPEC-1: 退化三角形を不正として扱うか
    理由: 指示書に記述が無い
  REGRESSION-2: circle.py の呼び出し元に影響は無いか
    options: yes | no
answer: masuda question answer abc 0007 'SPEC-1=<answer>' 'REGRESSION-2=<answer>'
`
	if !strings.HasPrefix(got, "abc 0007 (opened ") || !strings.HasSuffix(got, want) {
		t.Fatalf("question:\n%s\nwant to end with:\n%s", got, want)
	}
}

func TestServeに繋がらないときは繋ごうとしたソケットと次の手を返す(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "missing.sock")
	_, err := dial(socket).ws.List(context.Background(), connect.NewRequest(&apiv1.ListWorkspacesRequest{}))
	var unreachable *serveUnreachableError
	if !errors.As(err, &unreachable) {
		t.Fatalf("err = %v, want serveUnreachableError", err)
	}
	if msg := unreachable.Error(); !strings.Contains(msg, socket) || !strings.Contains(msg, "start 'masuda serve'") || strings.Contains(msg, "dial unix") {
		t.Fatalf("message = %q", msg)
	}
}

func TestParseは引数の数の誤りを使い方の1行で知らせる(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"足りない", nil, "masuda chat: missing arguments\nusage: masuda chat <id>\n"},
		{"多すぎる", []string{"a", "b"}, "masuda chat: unexpected argument \"b\"\nusage: masuda chat <id>\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd := newCommand("chat", "chat <id>")
			var out bytes.Buffer
			cmd.fs.SetOutput(&out)
			if _, err := cmd.parse(c.args, 1, 1); !errors.Is(err, errUsage) {
				t.Fatalf("err = %v", err)
			}
			if out.String() != c.want {
				t.Fatalf("output = %q, want %q", out.String(), c.want)
			}
		})
	}
}

func TestParseAnswersは補足を答えの次の行に続ける(t *testing.T) {
	cases := []struct {
		name    string
		kvs     []string
		notes   []string
		want    map[string]string
		wantErr string
	}{
		{"補足なし", []string{"A=(b) yes"}, nil, map[string]string{"A": "(b) yes"}, ""},
		{"補足あり", []string{"A=(b) yes", "B=no"}, []string{"A=exportsが残る前提"}, map[string]string{"A": "(b) yes\nexportsが残る前提", "B": "no"}, ""},
		{"同じ質問に2つの補足", []string{"A=x"}, []string{"A=1つめ", "A=2つめ"}, map[string]string{"A": "x\n1つめ\n2つめ"}, ""},
		{"答えの無い質問への補足", []string{"A=x"}, []string{"B=理由"}, nil, "B has no answer"},
		{"補足の形の誤り", []string{"A=x"}, []string{"理由だけ"}, nil, "want <question-id>=<text>"},
		{"答えの形の誤り", []string{"x"}, nil, nil, "want <question-id>=<answer>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseAnswers(c.kvs, c.notes)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("answers = %q, want %q", got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Fatalf("answers = %q, want %q", got, c.want)
				}
			}
		})
	}
}
