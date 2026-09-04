package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/sandbox"
)

// stubInterpreter writes an executable stand-in for the venv's python that
// runs body with the script path as $1, so runOrchestratorTurn's contract
// (cwd, MASUDA_STATE_DIR, exit status, TASK.md) can be exercised without a
// venv or a real orchestrator.
func stubInterpreter(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The orchestrator gets exactly the two inputs it got inside the sandbox:
// the worktree as cwd (every git command in implement_review_graph.py is
// relative to it) and MASUDA_STATE_DIR.
func TestRunOrchestratorTurnRunsInTheWorktreeAgainstTheStateDir(t *testing.T) {
	worktreeDir := t.TempDir()
	stateDir := t.TempDir()
	python := stubInterpreter(t, `
pwd > "$MASUDA_STATE_DIR/observed-cwd"
printf '%s\n' "$1" > "$MASUDA_STATE_DIR/observed-script"
printf '%s\n' "$MASUDA_GUEST_STATE_DIR" > "$MASUDA_STATE_DIR/observed-guest-state-dir"
printf 'implement step 3\n' > "$MASUDA_STATE_DIR/TASK.md"
`)

	task, err := runOrchestratorTurn(context.Background(), python, "/somewhere/implement_review_graph.py", worktreeDir, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if task != "implement step 3\n" {
		t.Fatalf("task = %q, want the TASK.md the orchestrator wrote", task)
	}

	// t.TempDir can hand back a symlinked path (/var vs /private/var and
	// friends), so compare what the shell resolved against the same
	// resolution rather than the raw string.
	wantCwd, err := filepath.EvalSymlinks(worktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(stateDir, "observed-cwd"))); got != wantCwd {
		t.Fatalf("cwd = %q, want the worktree %q", got, wantCwd)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(stateDir, "observed-script"))); got != "/somewhere/implement_review_graph.py" {
		t.Fatalf("script argument = %q, want the path passed in", got)
	}
	// Without this the orchestrator renders host paths into prompts, and
	// the guest session is told to write to a directory it cannot see.
	if got := strings.TrimSpace(readFile(t, filepath.Join(stateDir, "observed-guest-state-dir"))); got != sandbox.GuestStateDir {
		t.Fatalf("MASUDA_GUEST_STATE_DIR = %q, want the guest's mount point %q", got, sandbox.GuestStateDir)
	}
}

// A non-zero exit has to carry the orchestrator's own output: it is the only
// explanation of why the loop cannot advance.
func TestRunOrchestratorTurnReportsFailureWithOutput(t *testing.T) {
	python := stubInterpreter(t, `
echo "Traceback: something went wrong" >&2
exit 1
`)
	_, err := runOrchestratorTurn(context.Background(), python, "script.py", t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("expected an error from a non-zero exit")
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Fatalf("error did not carry the orchestrator's output: %v", err)
	}
}

// Exiting 0 without writing TASK.md is a broken orchestrator, not an empty
// task -- returning "" would read as "nothing to do" to the session.
func TestRunOrchestratorTurnFailsWhenNoTaskWasWritten(t *testing.T) {
	python := stubInterpreter(t, `exit 0`)
	_, err := runOrchestratorTurn(context.Background(), python, "script.py", t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("expected an error when the orchestrator wrote no TASK.md")
	}
	if !strings.Contains(err.Error(), "TASK.md") {
		t.Fatalf("error did not name the missing file: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
