package microvm

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

// StartBackgroundProcess starts cmd, first killing (best effort) whatever
// process a previous run recorded at pidFilePath, then recording cmd's own
// PID there once it's actually running. Shared by StartVirtiofs and
// StartMCPRelay (Issue #31 M5-3/M5-4): both need the exact same shape --
// allocate a long-lived background process, and if masuda itself crashed
// last time without a clean Stop, the next Start for the same identity
// (socket path, listen address, ...) must clean up the orphan rather than
// leaving it running forever or colliding with the new one. This is the
// same "clear any stale leftover, then create" pattern ensureTap uses for
// TAP devices, translated to processes instead of network interfaces.
//
// The process is started in a session of its own (Setsid), detached from
// the terminal of whichever CLI invocation happened to start it. These
// processes outlive that invocation by design, and without this a Ctrl-C
// typed while a VM start is still building the rootfs or waiting for a
// DHCP lease reaches every one of them already started -- the
// virtiofsd and VMM die mid-boot, and the host-wide egress proxy dies along
// with whichever invocation first launched it. Any SysProcAttr the caller
// set is overwritten.
func StartBackgroundProcess(cmd *exec.Cmd, pidFilePath, marker string) error {
	KillStalePID(pidFilePath, marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
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

// StopBackgroundProcess terminates proc (started via StartBackgroundProcess)
// and removes its pid file. Not an error if the process has already exited
// on its own.
func StopBackgroundProcess(proc *os.Process, pidFilePath string) error {
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

// KillStalePID kills the process recorded at pidFilePath by a previous
// start, if that PID still belongs to it -- marker identifies it in
// /proc, see processCmdlineContains. Best effort: any error here (missing
// pid file, already-dead process, permission issue) is silently ignored --
// it's cleanup so an orphan doesn't keep running forever, not something
// callers need to react to.
//
// The identity check is not optional here. A pid file outlives the process
// it names, and a host reboot resets the PID space, so "whatever holds this
// number now" is a different question from "the virtiofsd we started last
// time". This runs for every background process a VM start launches
// (virtiofsd, cloud-hypervisor, and the caller's own, such as masuda's
// mcp-relay and egress-proxy), and unlike
// a mistaken liveness check it does not merely misjudge -- it delivers a
// SIGTERM to whoever is on the other end (ADR-0061).
//
// Deliberately doesn't call Process.Wait: that PID belongs to a *previous*
// masuda run (masuda itself may have crashed and restarted), not a child of
// the current process, and Wait only works for actual children -- it would
// just fail with ECHILD.
func KillStalePID(pidFilePath, marker string) {
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
