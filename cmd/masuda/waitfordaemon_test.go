package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
)

// shortStateDir returns a state directory under /tmp rather than t.TempDir():
// the daemon socket path is appended to it, and pytest's own conftest hit
// AF_UNIX's ~108 byte sun_path limit going the other way.
func shortStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "msd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestWaitForDaemonReturnsOnceTheSocketAccepts(t *testing.T) {
	stateDir := shortStateDir(t)
	l, err := net.Listen("unix", statedaemon.SocketPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if err := waitForDaemon(stateDir); err != nil {
		t.Fatalf("waitForDaemon() = %v, want nil once the socket is listening", err)
	}
}

// A daemon that died instead of listening must not surface as a bare
// timeout: the reason is in its log, and that is the only place a caller
// could find it.
func TestWaitForDaemonReportsTheDaemonLogWhenItNeverListens(t *testing.T) {
	restore := daemonStartupTimeout
	daemonStartupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { daemonStartupTimeout = restore })

	stateDir := shortStateDir(t)
	if err := os.WriteFile(filepath.Join(stateDir, daemonLogName),
		[]byte("Error: listen unix /very/long/path/daemon-curated.sock: bind: invalid argument\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := waitForDaemon(stateDir)
	if err == nil {
		t.Fatal("waitForDaemon() = nil, want an error when nothing ever listens")
	}
	if !strings.Contains(err.Error(), "bind: invalid argument") {
		t.Fatalf("the error must carry the daemon's own log: %v", err)
	}
}
