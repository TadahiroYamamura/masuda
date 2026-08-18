package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// virtiofsdBinary is the process each VirtiofsProcess wraps. Unlike TAP
// device management (internal/sandbox/vmnet.go), virtiofsd needs no
// elevated privileges to run -- confirmed manually across the Issue #31
// M3-M5-1 VM boots -- so there's no setcap/helper-binary story here.
const virtiofsdBinary = "virtiofsd"

// virtiofsStartupTimeout bounds how long StartVirtiofs waits for virtiofsd
// to create its socket before giving up.
const virtiofsStartupTimeout = 5 * time.Second

// VirtiofsProcess is a running virtiofsd instance serving one directory over
// one vhost-user socket -- masuda runs one per shared directory a VM needs
// (/workspace, /masuda-state; Issue #31 M5-3).
type VirtiofsProcess struct {
	cmd        *exec.Cmd
	SocketPath string
}

// StartVirtiofs launches virtiofsd serving dir over a fresh vhost-user UDS
// at socketPath, logging virtiofsd's own stdout/stderr to logPath for
// diagnostics. Safe to call again for a socketPath a crashed previous run
// left behind: any orphaned virtiofsd process still holding that path (see
// pidPath) is killed first, then the stale socket file is cleared, mirroring
// EnsureTap's "clear any stale leftover, then create" pattern for TAP
// devices -- allocation and stale reclaim are the same code path here too.
//
// --sandbox=none: virtiofsd's own default (--sandbox=namespace) needs
// newuidmap/newgidmap (the uidmap package), which isn't installed and isn't
// yet a documented masuda host dependency (docs/CONTRIBUTING.md). This is a
// known, intentional compromise carried over from the Issue #31 spike, not
// a final security posture -- revisit alongside Issue #11 (sandbox network/
// isolation hardening), which is scheduled right after M1-M6 complete.
func StartVirtiofs(dir, socketPath, logPath string) (*VirtiofsProcess, error) {
	if _, err := exec.LookPath(virtiofsdBinary); err != nil {
		return nil, fmt.Errorf("%s not found on PATH (required for VM shared directories, Issue #31): %w", virtiofsdBinary, err)
	}

	killStalePID(socketPath)

	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("clearing stale socket %s: %w", socketPath, err)
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("creating virtiofsd log %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(virtiofsdBinary,
		"--socket-path="+socketPath,
		"--shared-dir="+dir,
		"--sandbox=none",
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting virtiofsd for %s: %w", dir, err)
	}

	if err := waitForSocket(socketPath, virtiofsStartupTimeout); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("virtiofsd for %s did not create its socket in time: %w", dir, err)
	}

	if err := os.WriteFile(pidPath(socketPath), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("recording virtiofsd pid for %s: %w", dir, err)
	}

	return &VirtiofsProcess{cmd: cmd, SocketPath: socketPath}, nil
}

// Stop terminates the virtiofsd process and removes its socket and pid
// files. Not an error if the process has already exited on its own.
func (v *VirtiofsProcess) Stop() error {
	if v.cmd.Process != nil {
		if err := v.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("stopping virtiofsd: %w", err)
		}
		_, _ = v.cmd.Process.Wait()
	}
	if err := os.Remove(v.SocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing socket %s: %w", v.SocketPath, err)
	}
	if err := os.Remove(pidPath(v.SocketPath)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing pid file for %s: %w", v.SocketPath, err)
	}
	return nil
}

// pidPath derives the file StartVirtiofs records its virtiofsd process's
// PID in, so a later StartVirtiofs for the same socketPath can find and
// kill an orphaned instance from a crashed previous run (masuda's own
// process, or the whole host, dying before Stop ran).
func pidPath(socketPath string) string {
	return socketPath + ".pid"
}

// killStalePID kills any process recorded by a previous StartVirtiofs for
// socketPath, if it's still alive. Best effort: any error here (missing pid
// file, already-dead process, permission issue) is silently ignored -- the
// subsequent os.Remove(socketPath) and fresh virtiofsd start are what
// actually matter, this is just cleanup so an orphan doesn't keep running
// forever in the background.
//
// Deliberately doesn't call Process.Wait: that PID belongs to a *previous*
// masuda run (masuda itself may have crashed and restarted, or this is a
// fresh process entirely), not a child of the current process, and Wait
// only works for actual children -- it would just fail with ECHILD.
func killStalePID(socketPath string) {
	data, err := os.ReadFile(pidPath(socketPath))
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

func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s waiting for %s to appear", timeout, path)
}
