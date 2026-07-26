// Package hostloop launches the phase 1-2 (investigate -> plan -> G1)
// self-loop directly on the host, without Docker (see ADR-0012 — phase 1-2 is
// read-mostly and doesn't need container isolation the way implementation
// does). Loop instructions are injected via `claude --append-system-prompt-file`
// rather than a CLAUDE.md file: there's no per-container ~/.claude to isolate
// it in here, and writing to the real ~/.claude/CLAUDE.md would clobber the
// user's own global config.
package hostloop

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

//go:embed system_prompt.md.tmpl
var systemPromptSrc string

var systemPromptTemplate = template.Must(template.New("system_prompt").Parse(systemPromptSrc))

const tmuxSessionPrefix = "masuda-plan-"

// allowedTools governs the MAIN session only (the one running the loop
// protocol) — it is deliberately narrower than the Docker sandbox's
// --dangerously-skip-permissions, since this runs unsandboxed on the user's
// real machine. The main session's own job is bounded to fixed, known
// commands (invoke the orchestrator, check TASK.md, kill the tmux session at
// the end), so it gets Bash — but it never reads through arbitrary,
// potentially untrusted repository content itself; that's delegated to the
// investigator/planner subagents (see customAgents), which get no Bash at
// all.
//
// This split exists because Bash can't be scoped down the way Edit/Read can:
// confirmed empirically that Claude Code applies its own per-command risk
// judgment to Bash regardless of --allowedTools — e.g. `ls` runs with no
// prompt even when Bash isn't listed at all, while `rm -rf` still prompts
// even when it is. There is no reliable way to broadly deny Bash while
// carving out exceptions for the 2 fixed commands the loop needs:
// disallowedTools and allowedTools both targeting "Bash" were tried, and
// deny always won, blocking the exceptions too. --print mode would sidestep
// this (a plain non-interactive claude call per loop iteration, no
// standing Bash-capable session) but bills through the Anthropic API rather
// than the subscription session this project exists to stay on (ADR-0001) —
// ruled out for that reason, not for a technical one.
//
// Write access is scoped to exactly the artifact files phase 1-2 produces —
// note the permission-rule name is Edit(path), not Write(path); the CLI
// itself corrects you if you use the tool's own name here, since Edit rules
// cover all file-editing tools including Write. Verified empirically that an
// Edit(./INVESTIGATION.md) rule lets that file be written while blocking
// writes to anything else, and that this composes with a custom agent's
// bare "Edit" tool grant (agent-level = which tools exist at all,
// session-level pattern = which paths those tools may touch).
//
// This intentionally does not add masuda-specific deny rules for secrets
// (.env and friends): --allowedTools only pre-approves tool use, it doesn't
// bypass permission checks, so whatever `permissions.deny` rules the target
// repository's own .claude/settings.json declares still apply on top —
// verified empirically that a repo-level deny still blocks Read even when
// --allowedTools broadly allows Read. Protecting secrets a repo hasn't
// declared as sensitive is that repo's responsibility, not masuda's — though
// note that guarantee is specifically for the Read/Grep/Glob tools; it's
// exactly the kind of thing Bash's looser risk-based gating could bypass,
// which is the other reason investigator/planner never get Bash.
const allowedTools = "Bash,Task,Read,Edit(./INVESTIGATION.md),Edit(./PLAN.md),Edit(./plan_result.json)"

// investigatorAgentName and plannerAgentName are the subagent_type values
// TASK.md instructions (orchestrator/investigate_plan_graph.py) tell the main
// session to delegate to. Both are defined with no Bash tool at all — the
// main session's own Bash access must never leak to the code that actually
// reads through repository content.
const (
	investigatorAgentName = "investigator"
	plannerAgentName      = "planner"
)

type customAgent struct {
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Tools       []string `json:"tools"`
}

func customAgentsJSON() (string, error) {
	agents := map[string]customAgent{
		investigatorAgentName: {
			Description: "Read-only codebase investigator (masuda phase 1). No Bash access.",
			Prompt: "あなたはコードベースを調査するサブエージェントです。Bashツールを持たないため、" +
				"Read・Grep・Globのみで調査を行い、指示されたファイル（INVESTIGATION.md）をEditツールで書き出してください。",
			Tools: []string{"Read", "Grep", "Glob", "Edit"},
		},
		plannerAgentName: {
			Description: "Read-only planner (masuda phase 2). No Bash access.",
			Prompt: "あなたは調査結果からプランを作成するサブエージェントです。Bashツールを持たないため、" +
				"Read・Grep・Globのみで自己解決可能な範囲の追加調査を行い、指示されたファイル（PLAN.mdまたはplan_result.json）をEditツールで書き出してください。",
			Tools: []string{"Read", "Grep", "Glob", "Edit"},
		},
	}
	data, err := json.Marshal(agents)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

var sessionNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// SessionName derives the tmux session name for branch.
func SessionName(branch string) string {
	return tmuxSessionPrefix + sessionNameSanitizer.ReplaceAllString(branch, "-")
}

// WriteTaskBrief writes the task description masuda plan start was given into
// the worktree, for investigate_plan_graph.py to read as .masuda-task.md.
func WriteTaskBrief(worktreeDir, task string) error {
	return os.WriteFile(filepath.Join(worktreeDir, ".masuda-task.md"), []byte(task), 0o644)
}

func renderSystemPrompt(worktreeDir, repoRoot string) (string, error) {
	var buf []byte
	w := &sliceWriter{buf: &buf}
	err := systemPromptTemplate.Execute(w, struct{ Python, Orchestrator string }{
		Python:       filepath.Join(repoRoot, "venv", "bin", "python"),
		Orchestrator: filepath.Join(repoRoot, "orchestrator", "investigate_plan_graph.py"),
	})
	if err != nil {
		return "", err
	}
	path := filepath.Join(worktreeDir, ".masuda-plan-system-prompt.md")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// sliceWriter avoids pulling in bytes.Buffer just for template.Execute.
type sliceWriter struct{ buf *[]byte }

func (w *sliceWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}

// IsRunning reports whether the phase 1-2 tmux session for branch is alive.
func IsRunning(branch string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", SessionName(branch))
	return cmd.Run() == nil
}

// AttachArgs returns the argv for interactively attaching to the phase 1-2
// tmux session (`masuda plan chat`) — a plain `tmux attach`, unlike
// sandbox.AttachArgs' `docker exec -it ... tmux attach`, since this session
// runs directly on the host, not in a container.
func AttachArgs(branch string) []string {
	return []string{"tmux", "attach", "-t", SessionName(branch)}
}

// Start launches the phase 1-2 tmux session for branch, writing the task
// brief and system prompt into worktreeDir first if this is the first run.
// It's a no-op (returns nil) if a session for branch is already running.
//
// task may be empty on a resume (after a G1 approve/reject, `masuda plan
// start <branch>` restarts the loop without needing the task description
// again) but is required the first time, when no task brief exists yet.
func Start(repoRoot, branch, worktreeDir, task string) error {
	if IsRunning(branch) {
		return nil
	}

	briefPath := filepath.Join(worktreeDir, ".masuda-task.md")
	if _, err := os.Stat(briefPath); os.IsNotExist(err) {
		if task == "" {
			return fmt.Errorf("no task description on file yet for %q — pass one: masuda plan start %s \"<task>\"", branch, branch)
		}
		if err := WriteTaskBrief(worktreeDir, task); err != nil {
			return fmt.Errorf("writing task brief: %w", err)
		}
	}

	// The loop protocol only re-invokes the orchestrator when TASK.md is
	// *absent* (see system_prompt.md.tmpl's rule 1) — appropriate for one
	// continuous session, but on a resume (after a G1 approve/reject) the
	// TASK.md left behind by the previous session already says "DONE" and
	// would make a fresh session exit immediately without ever re-checking
	// the gate marker. Removing it here forces the new session to start by
	// re-deriving phase from current on-disk state, which is the point of
	// resuming at all.
	if err := os.Remove(filepath.Join(worktreeDir, "TASK.md")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clearing stale TASK.md before resume: %w", err)
	}

	promptPath, err := renderSystemPrompt(worktreeDir, repoRoot)
	if err != nil {
		return fmt.Errorf("rendering system prompt: %w", err)
	}

	agentsJSON, err := customAgentsJSON()
	if err != nil {
		return fmt.Errorf("building --agents JSON: %w", err)
	}

	claudeCmd := fmt.Sprintf(
		"claude --allowedTools %s --agents %s --append-system-prompt-file %s '作業を開始せよ'",
		shellQuote(allowedTools), shellQuote(agentsJSON), shellQuote(promptPath),
	)

	cmd := exec.Command("tmux", "new-session", "-d", "-s", SessionName(branch), "-c", worktreeDir, claudeCmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux new-session: %w\n%s", err, out)
	}
	return nil
}

// shellQuote produces a POSIX single-quoted token safe to splice into the
// shell command line tmux hands off to `sh -c` — worktreeDir (and therefore
// the prompt path) is derived from a user-supplied branch name, so this must
// hold even if that name contains quotes or other shell metacharacters.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
