package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// ensureGateWorkspace resolves a workspace ID to its state directory and
// makes sure its state daemon is running. The workspace ID is the only
// input: state directories are global (see internal/workspace), so this
// deliberately never consults the cwd, and gate commands work from anywhere
// — including outside a git repository (Issue #25).
//
// Every gate command reads the open request and writes its decision through
// the daemon, so a workspace whose daemon has died since creation — e.g. one
// left waiting at a gate across a host reboot (Issue #52) — could not be
// answered at all without this.
func ensureGateWorkspace(id string) (string, error) {
	if !workspace.Exists(id) {
		return "", fmt.Errorf("no workspace %q", id)
	}
	if err := ensureDaemon(id); err != nil {
		return "", err
	}
	return workspace.StateDir(id)
}

// attach replaces the current process with an interactive ssh, so the
// user's terminal (stdin/stdout/stderr, raw mode) is wired straight to tmux
// attach inside the VM.
func attach(argv []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}
