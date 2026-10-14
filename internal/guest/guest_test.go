package guest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda-engine/engine"
)

func TestAgentFileAddsMCPToolsToRestrictedTools(t *testing.T) {
	got := string(AgentFile(&engine.Agent{Name: "planner", Description: "d", Tools: []string{"Read", "Bash"}, Body: "body"}).Content)
	want := `tools: "Read, Bash, mcp__masuda__write_output, mcp__masuda__report_result, mcp__masuda__report_concern, mcp__masuda__ask_human, mcp__masuda__run_privileged_command"`
	if !strings.Contains(got, want+"\n") {
		t.Fatalf("tools line missing or wrong:\n%s", got)
	}
	if strings.Contains(got, "next_task") {
		t.Fatalf("subagent must not get next_task:\n%s", got)
	}
}

func TestAgentFileWithoutToolsLeavesToolsUnrestricted(t *testing.T) {
	got := string(AgentFile(&engine.Agent{Name: "free", Description: "d", Body: "body"}).Content)
	if strings.Contains(got, "tools:") {
		t.Fatalf("an agent without tools must inherit every tool:\n%s", got)
	}
}

func TestAgentFileModelAndEffort(t *testing.T) {
	t.Run("modelとeffortのある役はfrontmatterにその行を書く", func(t *testing.T) {
		got := string(AgentFile(&engine.Agent{Name: "planner", Description: "d", Model: "sonnet", Effort: "low", Body: "body"}).Content)
		front, _, _ := strings.Cut(strings.TrimPrefix(got, "---\n"), "---\n")
		for _, want := range []string{"model: \"sonnet\"\n", "effort: \"low\"\n"} {
			if !strings.Contains(front, want) {
				t.Errorf("frontmatter lacks %q:\n%s", want, got)
			}
		}
	})
	t.Run("modelとeffortの無い役はfrontmatterにその行を書かない", func(t *testing.T) {
		got := string(AgentFile(&engine.Agent{Name: "free", Description: "d", Body: "body"}).Content)
		if strings.Contains(got, "model:") || strings.Contains(got, "effort:") {
			t.Fatalf("an agent without model/effort must inherit the session's:\n%s", got)
		}
	})
}

// ループ規約は、人間への問いかけをask_humanに限り、会話で問いかけて待たないことを明記する（M8で
// 会話の問いかけのまま止まった）。
func TestLoopRulesForbidAskingInConversation(t *testing.T) {
	for _, want := range []string{"ask_human", "会話で人間に問いかけて返事を待たない", "返ってくるまで他のことをしない"} {
		if !strings.Contains(string(loopRules), want) {
			t.Errorf("loop rules lack %q", want)
		}
	}
}

// ループ規約は、next_taskがwaitingを返したらターンを終え、masudaが送る文（WakeText）を受け取ったら
// next_taskを呼び直し、失敗してもループをやめないことを書く（#85）。
func Testループ規約はwaitingでターンを終えて起こされたら呼び直すことを書く(t *testing.T) {
	for _, want := range []string{`{kind: "waiting"}`, "そのターンを終えてください", "「" + WakeText + "」", "数回呼び直してください", "自分の判断でループをやめないでください"} {
		if !strings.Contains(string(loopRules), want) {
			t.Errorf("loop rules lack %q", want)
		}
	}
}

// ループ規約は、続きのタスクをSendMessageで同じサブエージェントへ送り、送れなければ新しく起動し、
// 委譲したサブエージェントのIDをnext_taskで報告することを書く（docs/guest-protocol.md）。
func TestLoopRulesDescribeContinuation(t *testing.T) {
	for _, want := range []string{"continues", "SendMessage", "ToolSearch", "agent_id", "新しく起動"} {
		if !strings.Contains(string(loopRules), want) {
			t.Errorf("loop rules lack %q", want)
		}
	}
}

// 対象リポジトリのCLAUDE.mdはループ規約の後ろに、見出しと優先順位の1行を挟んで連結する。
func TestClaudeMDAppendsProjectRulesAfterLoopRules(t *testing.T) {
	got := string(ClaudeMD([]byte("日本語で書く")))
	if !strings.HasPrefix(got, string(loopRules)) {
		t.Fatalf("loop rules must come first:\n%s", got)
	}
	rest := got[len(loopRules):]
	heading := strings.Index(rest, ProjectRulesHeading)
	priority := strings.Index(rest, "ループ規約が優先")
	body := strings.Index(rest, "日本語で書く")
	if heading < 0 || priority < heading || body < priority || !strings.HasSuffix(got, "\n") {
		t.Fatalf("project rules section:\n%s", rest)
	}
	if string(ClaudeMD(nil)) != string(loopRules) || string(ClaudeMD([]byte(" \n"))) != string(loopRules) {
		t.Fatal("without a project CLAUDE.md the loop rules are placed as they are")
	}
}

// ゲストのフックは送信が詰まってもメインセッションを長く止めない（#65）。どのイベントのフックにも、
// curlの上限とフックのtimeoutが付き、curlの最悪の時間がtimeoutに収まる。
func TestSettingsのフックはすべて送信の上限とtimeoutを持つ(t *testing.T) {
	b, err := Settings(nil)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string
				Timeout int
			}
		}
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	events := []string{"Notification", "PostToolUse", "Stop", "SubagentStop", "SessionEnd", "PermissionRequest"}
	if len(s.Hooks) != len(events) {
		t.Fatalf("hooks = %v", s.Hooks)
	}
	// 再試行を始めない窓（20秒）の直前に始めた1回が上限（15秒）まで走るのが最悪。
	const worstCurl = 20 + 15
	for _, ev := range events {
		h := s.Hooks[ev]
		if len(h) != 1 || len(h[0].Hooks) != 1 {
			t.Fatalf("%s: %+v", ev, h)
		}
		c := h[0].Hooks[0]
		for _, opt := range []string{"--connect-timeout 3", "--max-time 15", "--retry-max-time 20", HooksURL} {
			if !strings.Contains(c.Command, opt) {
				t.Errorf("%s: command %q lacks %q", ev, c.Command, opt)
			}
		}
		if c.Timeout != HookTimeoutSeconds || c.Timeout <= worstCurl {
			t.Errorf("%s: timeout %d must be %d and longer than curl's worst %d", ev, c.Timeout, HookTimeoutSeconds, worstCurl)
		}
	}
}

// 無人のrunでは確認に答える人がいない（#74）。PermissionRequestのフックは、入力をホストへ送った上で、
// 標準出力に拒否だけを出す。curlの応答が混ざるとClaude Codeが判定を読めないので、出力は拒否のJSONだけ。
func TestSettingsのPermissionRequestは入力をホストへ送り拒否と理由だけを出力する(t *testing.T) {
	b, err := Settings(nil)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]struct{ Hooks []struct{ Command string } }
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	command := s.Hooks["PermissionRequest"][0].Hooks[0].Command

	// curlの代わりに、受け取った標準入力を書き残し、応答らしいものを標準出力に出すものを置く
	bin := t.TempDir()
	sent := filepath.Join(bin, "sent")
	fake := "#!/bin/sh\ncat > " + sent + "\necho 'response body'\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	input := `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf /x"}}`
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(sent); string(got) != input {
		t.Errorf("sent to the host = %q, want the hook input as is", got)
	}
	var res struct {
		HookSpecificOutput struct {
			HookEventName string
			Decision      struct{ Behavior, Message string }
		}
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("stdout must be the decision JSON only: %q (%v)", out, err)
	}
	d := res.HookSpecificOutput
	if d.HookEventName != "PermissionRequest" || d.Decision.Behavior != "deny" || !strings.Contains(d.Decision.Message, "mktemp -d") {
		t.Fatalf("decision = %+v", d)
	}
}

func TestReviewPath(t *testing.T) {
	t.Run("集合の中の観点はReviewsDirの下の集合のディレクトリに置く", func(t *testing.T) {
		if p, err := reviewPath("implement-plan/layering.md"); err != nil || p != "/masuda/reviews/implement-plan/layering.md" {
			t.Fatalf("%q %v", p, err)
		}
	})
	for _, bad := range []string{"dead-code.md", "/x.md", "a/b/x.md", ".a/x.md", "a/.x.md", "a/x.txt", "a/"} {
		t.Run("集合の中の.mdでない名前は断る: "+bad, func(t *testing.T) {
			if _, err := reviewPath(bad); err == nil {
				t.Fatal("must be refused")
			}
		})
	}
}
