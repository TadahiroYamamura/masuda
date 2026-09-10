package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func recordVMPID(t *testing.T, id string, pid int) string {
	t.Helper()
	workDir, err := vmWorkDir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chPIDPath(workDir), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	return workDir
}

func TestVMIsRunningWithNoPIDFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if vmIsRunning("nosuch") {
		t.Fatal("vmIsRunning() = true with no pid file, want false")
	}
}

// TestVMIsRunningRejectsRecycledPID is the reason the identity check exists.
// A host reboot kills the VM and resets the PID space at once, so the PID
// left in cloud-hypervisor.pid can belong to anything by the time masuda
// reads it back. Reported as running, vmStart returns early and `masuda
// sandbox start` reports success while starting nothing.
func TestVMIsRunningRejectsRecycledPID(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	c := fakeProcess(t, "something-unrelated")
	recordVMPID(t, "ws0001", c.Process.Pid)

	if vmIsRunning("ws0001") {
		t.Fatal("vmIsRunning() = true for a live but unrelated PID, want false")
	}
}

func TestVMIsRunningAcceptsThisWorkspacesVM(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir, err := vmWorkDir("ws0002")
	if err != nil {
		t.Fatal(err)
	}
	// The real argv embeds the image path in a composite --disk argument.
	c := fakeProcess(t, "path="+filepath.Join(workDir, "rootfs.img")+",readonly=off,image_type=raw")
	recordVMPID(t, "ws0002", c.Process.Pid)

	if !vmIsRunning("ws0002") {
		t.Fatal("vmIsRunning() = false for this workspace's own cloud-hypervisor, want true")
	}
}

// TestVMIsRunningRejectsAnotherWorkspacesVM keeps the marker specific: two
// workspaces' VMs run the same binary, so only the per-workspace image path
// tells them apart.
func TestVMIsRunningRejectsAnotherWorkspacesVM(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	theirs, err := vmWorkDir("ws0003")
	if err != nil {
		t.Fatal(err)
	}
	c := fakeProcess(t, "path="+filepath.Join(theirs, "rootfs.img")+",readonly=off,image_type=raw")
	recordVMPID(t, "ws0004", c.Process.Pid)

	if vmIsRunning("ws0004") {
		t.Fatal("vmIsRunning() = true for another workspace's VM, want false")
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

	killStalePID(pidFile, "/run/masuda/virtiofs-workspace.sock")

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

	killStalePID(pidFile, marker)

	deadline := time.Now().Add(5 * time.Second)
	for processCmdlineContains(c.Process.Pid, marker) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if processCmdlineContains(c.Process.Pid, marker) {
		t.Error("the identified process is still running after killStalePID()")
	}
}

func TestKillStalePIDWithoutMarkerDoesNothing(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "some.pid")
	c := fakeProcess(t, "anything")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(c.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	// vmStop passes "" when it cannot resolve the relay's identity; that has
	// to mean "leave it alone", not "signal whatever is there".
	killStalePID(pidFile, "")

	if err := c.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("a process was signalled despite an empty marker: %v", err)
	}
}
