package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// ensureGateWorkspace resolves a workspace ID to its state directory (where
// gate markers and the artifacts they judge live, per roadmap step 7 — never
// the worktree itself) and makes sure its state daemon is running. The
// workspace ID is the only input: state directories are global (see
// internal/workspace), so this deliberately never consults the cwd, and gate
// commands work from anywhere — including outside a git repository (Issue
// #25).
//
// Every gate command reaches its marker through the daemon
// (internal/gate.Approve/Reject/Halt, and Show for the plan gate's reopen
// reason), so a workspace whose daemon has died since creation could not be
// answered at all: `masuda review approve` failed with "connect: connection
// refused" against a workspace left waiting at G2 across a host reboot
// (Issue #52). startDaemon is idempotent, so this is the same "make sure the
// thing exists" shape `plan start`'s resume path already used — it just had
// no counterpart on the gate side, which is where a long-lived workspace
// actually spends its time.
func ensureGateWorkspace(id string) (string, error) {
	if !workspace.Exists(id) {
		return "", fmt.Errorf("no workspace %q", id)
	}
	if err := ensureDaemon(id); err != nil {
		return "", err
	}
	return workspace.StateDir(id)
}

// attach replaces the current process with an interactive docker exec, so the
// user's terminal (stdin/stdout/stderr, raw mode) is wired straight to tmux
// attach inside the container.
func attach(argv []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}
