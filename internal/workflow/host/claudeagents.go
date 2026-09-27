package host

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
)

// ClaudeAgentsDir is where the host leaves the run's subagent definitions
// in the state directory; the guest's entrypoint copies them into
// ~/.claude/agents/.
const ClaudeAgentsDir = "claude-agents"

// workflowTools are the curated MCP tools every agent needs to hand its
// work back, or to raise a security concern. They are added to a restricted tools list, or an agent that
// may only read could not write its outputs or report (ADR-0073).
var workflowTools = []string{"mcp__masuda-gate__write_output", "mcp__masuda-gate__report_result", "mcp__masuda-gate__report_concern"}

// WriteClaudeAgents renders each agent of the set as a Claude Code subagent
// definition. The role's prompt stays in the instruction file of each
// task, so the subagent itself only knows to read and follow it.
func WriteClaudeAgents(set *def.Set, stateDir string) error {
	dir := filepath.Join(stateDir, ClaudeAgentsDir)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, a := range set.Agents {
		name := agentName(a.Path)
		var b strings.Builder
		fmt.Fprintf(&b, "---\nname: %s\n", name)
		desc := a.Description
		if desc == "" {
			desc = name
		}
		fmt.Fprintf(&b, "description: %s\n", yamlQuote(desc))
		if a.Tools != nil {
			tools := append(append([]string{}, a.Tools...), workflowTools...)
			fmt.Fprintf(&b, "tools: %s\n", strings.Join(tools, ", "))
		}
		b.WriteString("---\n")
		b.WriteString("渡された指示ファイルを読み、その指示に従え。指示ファイルに書かれていないことはしない。\n")
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(b.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func yamlQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
