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
