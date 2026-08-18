package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// startBackgroundProcess starts cmd, first killing (best effort) whatever
// process a previous run recorded at pidFilePath, then recording cmd's own
// PID there once it's actually running. Shared by StartVirtiofs and
// StartMCPRelay (Issue #31 M5-3/M5-4): both need the exact same shape --
// allocate a long-lived background process, and if masuda itself crashed
// last time without a clean Stop, the next Start for the same identity
// (socket path, listen address, ...) must clean up the orphan rather than
// leaving it running forever or colliding with the new one. This is the
// same "clear any stale leftover, then create" pattern EnsureTap uses for
// TAP devices, translated to processes instead of network interfaces.
func startBackgroundProcess(cmd *exec.Cmd, pidFilePath string) error {
	killStalePID(pidFilePath)
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(pidFilePath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return fmt.Errorf("recording pid: %w", err)
	}
	return nil
}

// stopBackgroundProcess terminates proc (started via startBackgroundProcess)
// and removes its pid file. Not an error if the process has already exited
// on its own.
func stopBackgroundProcess(proc *os.Process, pidFilePath string) error {
	if proc != nil {
		if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("stopping process: %w", err)
		}
		_, _ = proc.Wait()
	}
	if err := os.Remove(pidFilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing pid file %s: %w", pidFilePath, err)
	}
	return nil
}

// killStalePID kills any process recorded at pidFilePath by a previous
// start, if it's still alive. Best effort: any error here (missing pid
// file, already-dead process, permission issue) is silently ignored -- it's
// cleanup so an orphan doesn't keep running forever, not something callers
// need to react to.
//
// Deliberately doesn't call Process.Wait: that PID belongs to a *previous*
// masuda run (masuda itself may have crashed and restarted), not a child of
// the current process, and Wait only works for actual children -- it would
// just fail with ECHILD.
func killStalePID(pidFilePath string) {
	data, err := os.ReadFile(pidFilePath)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Signal(syscall.SIGTERM)
}
