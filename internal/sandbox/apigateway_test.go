package sandbox

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/microvm"
)

func TestStartAPIGatewayAndStop(t *testing.T) {
	exe := buildMasudaForTest(t)
	original := resolveMasudaExe
	resolveMasudaExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { resolveMasudaExe = original })
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	workDir := t.TempDir()
	pidFile := filepath.Join(workDir, "api-gateway.pid")
	mac := microvm.MACFor("gwtest")

	gw, err := StartAPIGateway("127.0.0.3", port, filepath.Join(workDir, "token"), filepath.Join(workDir, "gw.log"), pidFile, mac, filepath.Join(workDir, "leases"))
	if err != nil {
		t.Fatalf("StartAPIGateway() error = %v", err)
	}
	t.Cleanup(func() { _ = gw.Stop() })

	// The MAC is what vmStop identifies this process by.
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("pid file not at the requested path: %v", err)
	}
	microvm.KillStalePID(pidFile, mac)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.DialTimeout("tcp", gw.Addr, 200*time.Millisecond); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("api-gateway at %s still accepting connections after KillStalePID with its MAC", gw.Addr)
}
