package microvm

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeProcess starts a long-running process whose argv carries marker, so a
// test can exercise the identity check without a real VM. `sh -c <script>
// <args...>` puts the trailing arguments in $0/$1/... -- they never reach
// the script, but they do land in /proc/<pid>/cmdline, which is what the
// check reads.
//
// The script has to be a loop rather than a bare `sleep 30`: a shell whose
// script is a single command execs it, replacing itself, and the argv the
// test cares about disappears with it. That made this helper's result
// depend on whether /proc was read before or after the exec -- a test that
// asserts "no match" would then pass for entirely the wrong reason.
func fakeProcess(t *testing.T, marker string) *exec.Cmd {
	t.Helper()
	c := exec.Command("sh", "-c", "while :; do sleep 1; done", marker)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Process.Kill()
		_ = c.Wait()
	})
	// Start returns once the fork has happened, which is before the exec
	// that replaces the child's argv -- until then /proc shows a copy of
	// this test binary's own command line, not the one asked for here.
	deadline := time.Now().Add(5 * time.Second)
	for !processCmdlineContains(c.Process.Pid, marker) {
		if time.Now().After(deadline) {
			t.Fatalf("fakeProcess(%q) never became identifiable -- the helper is broken, not the code under test", marker)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return c
}

func recordVMPID(t *testing.T, h Host, id string, pid int) string {
	t.Helper()
	workDir, err := h.WorkDir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chPIDPath(workDir), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	return workDir
}

func TestVMIsRunningWithNoPIDFile(t *testing.T) {
	h := Host{DataDir: t.TempDir()}
	if h.IsRunning("nosuch") {
		t.Fatal("h.IsRunning() = true with no pid file, want false")
	}
}

// TestVMIsRunningRejectsRecycledPID is the reason the identity check exists.
// A host reboot kills the VM and resets the PID space at once, so the PID
// left in cloud-hypervisor.pid can belong to anything by the time masuda
// reads it back. Reported as running, a caller skips Start and reports
// success while starting nothing.
func TestVMIsRunningRejectsRecycledPID(t *testing.T) {
	h := Host{DataDir: t.TempDir()}
	c := fakeProcess(t, "something-unrelated")
	recordVMPID(t, h, "ws0001", c.Process.Pid)

	if h.IsRunning("ws0001") {
		t.Fatal("h.IsRunning() = true for a live but unrelated PID, want false")
	}
}

func TestVMIsRunningAcceptsThisWorkspacesVM(t *testing.T) {
	h := Host{DataDir: t.TempDir()}
	workDir, err := h.WorkDir("ws0002")
	if err != nil {
		t.Fatal(err)
	}
	// The real argv embeds the image path in a composite --disk argument.
	c := fakeProcess(t, "path="+filepath.Join(workDir, "rootfs.img")+",readonly=off,image_type=raw")
	recordVMPID(t, h, "ws0002", c.Process.Pid)

	if !h.IsRunning("ws0002") {
		t.Fatal("h.IsRunning() = false for this workspace's own cloud-hypervisor, want true")
	}
}

// TestVMIsRunningRejectsAnotherWorkspacesVM keeps the marker specific: two
// workspaces' VMs run the same binary, so only the per-workspace image path
// tells them apart.
func TestVMIsRunningRejectsAnotherWorkspacesVM(t *testing.T) {
	h := Host{DataDir: t.TempDir()}
	theirs, err := h.WorkDir("ws0003")
	if err != nil {
		t.Fatal(err)
	}
	c := fakeProcess(t, "path="+filepath.Join(theirs, "rootfs.img")+",readonly=off,image_type=raw")
	recordVMPID(t, h, "ws0004", c.Process.Pid)

	if h.IsRunning("ws0004") {
		t.Fatal("h.IsRunning() = true for another workspace's VM, want false")
	}
}

func TestProcessCmdlineContainsRejectsDeadPID(t *testing.T) {
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if processCmdlineContains(c.Process.Pid, "true") {
		t.Fatal("processCmdlineContains() = true for an exited process, want false")
	}
}

func TestProcessCmdlineContainsRejectsEmptyMarker(t *testing.T) {
	if processCmdlineContains(os.Getpid(), "") {
		t.Fatal("processCmdlineContains() = true for an empty marker, want false")
	}
}

// TestKillStalePIDLeavesUnidentifiedProcess is the one that matters most:
// this path does not merely misjudge, it delivers a SIGTERM. It runs on
// every `masuda sandbox start` against six pid files, all of which can name
// a recycled PID after a host reboot.
func TestKillStalePIDLeavesUnidentifiedProcess(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "some.pid")
	c := fakeProcess(t, "not-the-process-masuda-started")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(c.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	KillStalePID(pidFile, "/run/masuda/virtiofs-workspace.sock")

	if err := c.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("an unrelated process was signalled: %v", err)
	}
}

func TestKillStalePIDStopsTheIdentifiedProcess(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "some.pid")
	marker := filepath.Join(dir, "virtiofs-workspace.sock")
	c := fakeProcess(t, "--socket-path="+marker)
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(c.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	KillStalePID(pidFile, marker)

	deadline := time.Now().Add(5 * time.Second)
	for processCmdlineContains(c.Process.Pid, marker) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if processCmdlineContains(c.Process.Pid, marker) {
		t.Error("the identified process is still running after KillStalePID()")
	}
}

func TestKillStalePIDWithoutMarkerDoesNothing(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "some.pid")
	c := fakeProcess(t, "anything")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(c.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	// internal/sandbox passes "" when it cannot resolve the relay's identity; that has
	// to mean "leave it alone", not "signal whatever is there".
	KillStalePID(pidFile, "")

	if err := c.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("a process was signalled despite an empty marker: %v", err)
	}
}

// sessionID reads the session ID from /proc/<pid>/stat. The fields after the
// parenthesised command name are fixed; session is the fourth of them.
func sessionID(t *testing.T, pid int) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))
	sid, err := strconv.Atoi(fields[3])
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

// TestStartBackgroundProcessDetachesFromCallerSession: a Ctrl-C at the
// terminal that started `masuda sandbox start` must not reach the VMM,
// virtiofsd, or egress proxy it launched.
func TestStartBackgroundProcessDetachesFromCallerSession(t *testing.T) {
	dir := t.TempDir()
	c := exec.Command("sh", "-c", "while :; do sleep 1; done")
	if err := StartBackgroundProcess(c, filepath.Join(dir, "p.pid"), "unused"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Process.Kill()
		_ = c.Wait()
	})

	if got := sessionID(t, c.Process.Pid); got != c.Process.Pid {
		t.Errorf("child session = %d, want it to lead its own session (%d)", got, c.Process.Pid)
	}
	if got, own := sessionID(t, c.Process.Pid), sessionID(t, os.Getpid()); got == own {
		t.Errorf("child shares the caller's session %d", own)
	}
}

func TestWaitUntilRetriesUntilSuccess(t *testing.T) {
	calls := 0
	err := waitUntil(func() error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	}, time.Second, time.Millisecond)
	if err != nil || calls != 3 {
		t.Errorf("waitUntil() = %v after %d calls, want nil after 3", err, calls)
	}
}

func TestWaitUntilGivesUpWithTheLastError(t *testing.T) {
	start := time.Now()
	err := waitUntil(func() error { return errors.New("connection refused") }, 50*time.Millisecond, 5*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("waitUntil() = %v, want the last attempt's error", err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Error("waitUntil() gave up before its timeout")
	}
}

// TestWaitForProcessExitSeesAnUnreapedChild: a VMM started by this very
// process stays a zombie after it exits, which a signal-0 probe reports as
// alive -- waitForProcessExit must not wait out its timeout on it.
func TestWaitForProcessExitSeesAnUnreapedChild(t *testing.T) {
	c := exec.Command("true")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Wait() })
	pidFile := filepath.Join(t.TempDir(), "p.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(c.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !isZombie(c.Process.Pid) {
		if time.Now().After(deadline) {
			t.Fatal("the child never became a zombie -- the helper is broken, not the code under test")
		}
		time.Sleep(5 * time.Millisecond)
	}

	begin := time.Now()
	waitForProcessExit(pidFile, 5*time.Second)
	if took := time.Since(begin); took > time.Second {
		t.Errorf("waitForProcessExit() took %s on an exited, unreaped child, want it to return at once", took)
	}
}
