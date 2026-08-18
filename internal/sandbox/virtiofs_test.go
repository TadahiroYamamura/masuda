package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireVirtiofsd skips the test unless virtiofsd is on PATH. Skipping
// rather than failing keeps `go test ./...` usable without it installed --
// masuda has no CI job that runs `go test` (release.yml only builds/signs
// binaries on tag push), so this is purely a local developer convenience.
func requireVirtiofsd(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(virtiofsdBinary); err != nil {
		t.Skipf("%s not installed", virtiofsdBinary)
	}
}

func TestStartVirtiofsAndStop(t *testing.T) {
	requireVirtiofsd(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("seeding shared dir: %v", err)
	}
	work := t.TempDir()
	socketPath := filepath.Join(work, "virtiofs.sock")
	logPath := filepath.Join(work, "virtiofs.log")

	v, err := StartVirtiofs(dir, socketPath, logPath)
	if err != nil {
		t.Fatalf("StartVirtiofs() error = %v", err)
	}
	t.Cleanup(func() { _ = v.Stop() })

	if _, err := os.Stat(socketPath); err != nil {
		t.Errorf("socket %s not present after Start(): %v", socketPath, err)
	}
	if _, err := os.Stat(pidPath(socketPath)); err != nil {
		t.Errorf("pid file %s not present after Start(): %v", pidPath(socketPath), err)
	}

	if err := v.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Errorf("socket %s still present after Stop()", socketPath)
	}
	if _, err := os.Stat(pidPath(socketPath)); !os.IsNotExist(err) {
		t.Errorf("pid file %s still present after Stop()", pidPath(socketPath))
	}
}

// TestStartVirtiofsReclaimsStaleProcess simulates masuda crashing while
// virtiofsd was running (the process dies without Stop() ever running, so
// the socket and pid file are left behind) and confirms the next
// StartVirtiofs for the same socketPath cleans up and succeeds anyway --
// the same "stale reclaim" guarantee EnsureTap provides for TAP devices.
func TestStartVirtiofsReclaimsStaleProcess(t *testing.T) {
	requireVirtiofsd(t)

	dir := t.TempDir()
	work := t.TempDir()
	socketPath := filepath.Join(work, "virtiofs.sock")
	logPath := filepath.Join(work, "virtiofs.log")

	v, err := StartVirtiofs(dir, socketPath, logPath)
	if err != nil {
		t.Fatalf("first StartVirtiofs() error = %v", err)
	}
	staleSocketPath := v.SocketPath

	// Simulate a crash: kill the process directly, bypassing Stop(), so the
	// socket and pid files are left behind exactly as they'd be after
	// masuda itself died.
	if err := v.cmd.Process.Kill(); err != nil {
		t.Fatalf("killing virtiofsd to simulate a crash: %v", err)
	}
	_, _ = v.cmd.Process.Wait()

	v2, err := StartVirtiofs(dir, staleSocketPath, logPath)
	if err != nil {
		t.Fatalf("second StartVirtiofs() (stale reclaim) error = %v", err)
	}
	t.Cleanup(func() { _ = v2.Stop() })

	if _, err := os.Stat(staleSocketPath); err != nil {
		t.Errorf("socket %s not present after reclaiming Start(): %v", staleSocketPath, err)
	}
}
