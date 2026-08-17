package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpserver"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// daemonSocketName/daemonPIDName/daemonLogName/daemonStoreDirName are the
// well-known filenames a workspace's state directory holds for its
// statedaemon process (Issue #35). store/ is where the KV data itself is
// persisted, kept in its own subdirectory so it doesn't mix with
// workspace.json and the other plain files internal/workspace already owns
// there.
const (
	daemonSocketName   = "daemon.sock"
	daemonPIDName      = "daemon.pid"
	daemonLogName      = "daemon.log"
	daemonStoreDirName = "store"
)

func daemonSocketPath(stateDir string) string { return filepath.Join(stateDir, daemonSocketName) }

// runStatedaemon opens workspace id's store and serves it over its UDS
// socket until ctx is cancelled. Extracted from the cobra RunE so it's
// directly testable without spawning a subprocess.
func runStatedaemon(ctx context.Context, id string) error {
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		return err
	}
	store, err := statedaemon.Open(filepath.Join(stateDir, daemonStoreDirName))
	if err != nil {
		return err
	}
	return mcpserver.ServeUDS(ctx, store, daemonSocketPath(stateDir))
}

// newInternalCommand groups plumbing commands masuda spawns for itself
// (e.g. the detached state daemon process) rather than ones a user is meant
// to type.
func newInternalCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "internal",
		Hidden: true,
		Short:  "Internal plumbing commands, not part of the public CLI surface",
	}
	cmd.AddCommand(newInternalStatedaemonCommand())
	cmd.AddCommand(newInternalStateCommand())
	return cmd
}

func newInternalStatedaemonCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "statedaemon <workspace-id>",
		Hidden: true,
		Short:  "Run the per-workspace state daemon in the foreground (normally spawned detached by workspace create)",
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			err := runStatedaemon(ctx, args[0])
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
}

// startDaemon spawns `masuda internal statedaemon id` as a detached
// background process (Setsid, stdout/stderr to daemon.log inside the
// workspace's state directory) and records its PID so stopDaemon can find it
// later. The process must outlive this CLI invocation -- it has to keep
// running across the many short-lived `masuda plan/review approve` etc.
// invocations that follow, the same "fire and forget" shape
// internal/sandbox.Start uses for the sandbox container itself.
func startDaemon(id string) error {
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(stateDir, daemonLogName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "internal", "statedaemon", id)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, daemonPIDName), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
}

// stopDaemon signals workspace id's state daemon (if one is running) to shut
// down. A missing PID file means the daemon was never started -- true for
// every workspace created before this feature existed -- so that's treated
// as success, not an error, the same way internal/sandbox.Start ignores
// "nothing to remove" from its own cleanup step.
func stopDaemon(id string) error {
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(stateDir, daemonPIDName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return err
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
