package sandbox

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpserver"
	"github.com/TadahiroYamamura/masuda/internal/testutil"
)

// buildMasudaForTest builds a real masuda binary once, since StartMCPRelay
// re-execs itself (resolveMasudaExe) to run `internal mcp-relay` -- the
// `go test` binary running this test doesn't understand that subcommand at
// all, so this test points resolveMasudaExe at a real build instead.
func buildMasudaForTest(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH, can't build a masuda binary to self-exec")
	}
	path := filepath.Join(t.TempDir(), "masuda")
	cmd := exec.Command("go", "build", "-o", path, "github.com/TadahiroYamamura/masuda/cmd/masuda")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building masuda for test: %v\n%s", err, out)
	}
	return path
}

func TestStartMCPRelayAndStop(t *testing.T) {
	exe := buildMasudaForTest(t)
	original := resolveMasudaExe
	resolveMasudaExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { resolveMasudaExe = original })

	sockDir := t.TempDir()
	socketPath := filepath.Join(sockDir, "daemon-curated.sock")
	store, err := statedaemon.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serveErr := make(chan error, 1)
	go func() { serveErr <- mcpserver.ServeCuratedUDS(ctx, store, socketPath) }()
	waitForSocketAccepting(t, socketPath, serveErr)

	bind := "127.0.0.3" // loopback alias, needs no host network setup
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	logPath := filepath.Join(t.TempDir(), "relay.log")

	relay, err := StartMCPRelay(socketPath, bind, port, logPath)
	if err != nil {
		t.Fatalf("StartMCPRelay() error = %v", err)
	}
	t.Cleanup(func() { _ = relay.Stop() })

	if want := net.JoinHostPort(bind, strconv.Itoa(port)); relay.Addr != want {
		t.Errorf("relay.Addr = %q, want %q", relay.Addr, want)
	}
	conn, err := net.Dial("tcp", relay.Addr)
	if err != nil {
		t.Fatalf("dialing relay at %s: %v", relay.Addr, err)
	}
	conn.Close()

	if err := relay.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := net.DialTimeout("tcp", relay.Addr, 200*time.Millisecond); err == nil {
		t.Errorf("relay at %s still accepting connections after Stop()", relay.Addr)
	}
}

// waitForSocketAccepting wraps testutil.WaitForUDS so this package's tests
// keep a t.Fatal-shaped call. Waiting for the socket file to appear (what
// this used to do) is not the same as waiting for the server -- see
// testutil.WaitForUDS.
func waitForSocketAccepting(t *testing.T, path string, serveErr <-chan error) {
	t.Helper()
	if err := testutil.WaitForUDS(path, serveErr); err != nil {
		t.Fatal(err)
	}
}
