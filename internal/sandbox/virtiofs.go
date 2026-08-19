package sandbox

import (
	"fmt"
	"os"
	"os/exec"
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

// pidPath derives the file startBackgroundProcess records a process's PID
// in, from the path that identifies it (a socket path here; a listen
// address for StartMCPRelay). A later Start for the same identity uses this
// to find and kill an orphan from a crashed previous run.
func pidPath(identity string) string {
	return identity + ".pid"
}

// StartVirtiofs launches virtiofsd serving dir over a fresh vhost-user UDS
// at socketPath, logging virtiofsd's own stdout/stderr to logPath for
// diagnostics. Safe to call again for a socketPath a crashed previous run
// left behind -- see startBackgroundProcess.
//
// --sandbox=none: virtiofsd's own default (--sandbox=namespace) needs
// newuidmap/newgidmap (the uidmap package), which isn't a masuda host
// dependency. This is a known, intentional compromise, not a final security
// posture -- the shared directories are served without the namespace
// isolation the default would add. Rationale and the alternative that was
// weighed: docs/adr/0049-virtiofsd-sandbox-none.md
func StartVirtiofs(dir, socketPath, logPath string) (*VirtiofsProcess, error) {
	if _, err := exec.LookPath(virtiofsdBinary); err != nil {
		return nil, fmt.Errorf("%s not found on PATH (required for VM shared directories, Issue #31): %w", virtiofsdBinary, err)
	}

	killStalePID(pidPath(socketPath))

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
	if err := startBackgroundProcess(cmd, pidPath(socketPath)); err != nil {
		return nil, fmt.Errorf("starting virtiofsd for %s: %w", dir, err)
	}

	if err := waitForSocket(socketPath, virtiofsStartupTimeout); err != nil {
		_ = stopBackgroundProcess(cmd.Process, pidPath(socketPath))
		return nil, fmt.Errorf("virtiofsd for %s did not create its socket in time: %w", dir, err)
	}

	return &VirtiofsProcess{cmd: cmd, SocketPath: socketPath}, nil
}

// Stop terminates the virtiofsd process and removes its socket and pid
// files. Not an error if the process has already exited on its own.
func (v *VirtiofsProcess) Stop() error {
	if err := stopBackgroundProcess(v.cmd.Process, pidPath(v.SocketPath)); err != nil {
		return err
	}
	if err := os.Remove(v.SocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing socket %s: %w", v.SocketPath, err)
	}
	return nil
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
