package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
func startBackgroundProcess(cmd *exec.Cmd, pidFilePath, marker string) error {
	killStalePID(pidFilePath, marker)
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

// processCmdlineContains reports whether pid is a live process whose argv
// contains marker inside one of its arguments.
//
// A recorded PID alone cannot be trusted to still mean what it meant when
// it was written: a host reboot resets the PID space, so the number in a
// pid file left over from before may now belong to something else entirely.
// signal 0 only answers "does some process hold this number", which is why
// masuda's state daemon stopped relying on it (ADR-0061); the processes
// managed here have exactly the same exposure, and worse consequences,
// since acting on the answer means sending SIGTERM.
//
// marker has to identify one specific process, not a class of them -- a
// socket path or a disk image path, not a binary name shared by every
// workspace's VM. Matched as a substring of a single argument rather than
// as a whole argument, because the paths that identify these processes are
// embedded in composite arguments (`--disk path=<img>,readonly=off,...`).
//
// Linux-only, like the rest of masuda's process handling. A /proc that
// cannot be read counts as "not a match", which errs toward leaving an
// orphan running rather than signalling a stranger.
func processCmdlineContains(pid int, marker string) bool {
	if marker == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	for _, arg := range strings.Split(string(data), "\x00") {
		if strings.Contains(arg, marker) {
			return true
		}
	}
	return false
}

// killStalePID kills the process recorded at pidFilePath by a previous
// start, if that PID still belongs to it -- marker identifies it in
// /proc, see processCmdlineContains. Best effort: any error here (missing
// pid file, already-dead process, permission issue) is silently ignored --
// it's cleanup so an orphan doesn't keep running forever, not something
// callers need to react to.
//
// The identity check is not optional here. A pid file outlives the process
// it names, and a host reboot resets the PID space, so "whatever holds this
// number now" is a different question from "the virtiofsd we started last
// time". This runs on every `masuda sandbox start` across six pid files
// (three virtiofsd, mcp-relay, egress-proxy, cloud-hypervisor), and unlike
// a mistaken liveness check it does not merely misjudge -- it delivers a
// SIGTERM to whoever is on the other end (ADR-0061).
//
// Deliberately doesn't call Process.Wait: that PID belongs to a *previous*
// masuda run (masuda itself may have crashed and restarted), not a child of
// the current process, and Wait only works for actual children -- it would
// just fail with ECHILD.
func killStalePID(pidFilePath, marker string) {
	data, err := os.ReadFile(pidFilePath)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return
	}
	if !processCmdlineContains(pid, marker) {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Signal(syscall.SIGTERM)
}
