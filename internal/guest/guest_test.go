package guest

import (
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

// ループ規約は、人間への問いかけをask_humanに限り、会話で問いかけて待たないことを明記する（M8で
// 会話の問いかけのまま止まった）。
func TestLoopRulesForbidAskingInConversation(t *testing.T) {
	for _, want := range []string{"ask_human", "会話で人間に問いかけて返事を待たない", "返ってくるまで他のことをしない"} {
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
