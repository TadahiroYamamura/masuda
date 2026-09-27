package sandbox

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/microvm"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// TestManualGuestCannotReachOtherHostListeners checks that a sandbox guest
// can reach, on the bridge gateway, only what it is meant to: its own
// mcp-relay. Every workspace's relay listens on that same gateway address
// (vmBridgeGatewayIP) and relays into that workspace's curated MCP socket,
// so a guest reaching another workspace's relay could drive its gates and
// privileged commands. Two layers are checked separately: the host INPUT
// filter (scripts/setup-vm-host.sh) must drop a port outside the relay
// range outright, and a relay inside the range must refuse a guest whose
// lease is not the one it serves.
func TestManualGuestCannotReachOtherHostListeners(t *testing.T) {
	if os.Getenv("MASUDA_MANUAL_VM_TEST") == "" {
		t.Skip("set MASUDA_MANUAL_VM_TEST=1 to run this real-machine VM test")
	}
	exe := buildMasudaForTest(t)
	originalResolve := resolveMasudaExe
	resolveMasudaExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { resolveMasudaExe = originalResolve })

	other, err := net.Listen("tcp", net.JoinHostPort(vmBridgeGatewayIP, "0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	go func() {
		for {
			c, err := other.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	otherPort := other.Addr().(*net.TCPAddr).Port

	repoRoot := t.TempDir()
	writeImageEntry(t, repoRoot, config.DefaultImageEntry, "FROM masuda-loop:latest\n")
	buildImageEntry(t, repoRoot, config.DefaultImageEntry)

	id := "manvm3"
	if _, err := workspace.Create(repoRoot, id, "manual-test-branch", "develop", ""); err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Remove(id) })
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		t.Fatal(err)
	}

	// No state daemon runs in this test; a bare listener in its place tells
	// whether the guest's own relay actually serves it, which a TCP connect
	// alone cannot -- a refusing relay accepts and then closes.
	ownUDS, err := net.Listen("unix", statedaemon.CuratedSocketPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ownUDS.Close() })
	ownReached := make(chan struct{}, 1)
	go func() {
		if c, err := ownUDS.Accept(); err == nil {
			_ = c.Close()
			ownReached <- struct{}{}
		}
	}()

	var backend Backend = VMBackend{}
	t.Cleanup(func() { _ = backend.Stop(id) })
	if _, err := backend.Start(id, t.TempDir(), stateDir, repoRoot, config.DefaultImageEntry); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h, err := vmHost()
	if err != nil {
		t.Fatal(err)
	}
	run := guestRunner(t, h.AttachArgs, id)
	waitForGuestSSH(t, run)

	workDir, err := h.WorkDir(id)
	if err != nil {
		t.Fatal(err)
	}
	ownPort, err := os.ReadFile(vmRelayPortFile(workDir))
	if err != nil {
		t.Fatal(err)
	}

	// Another workspace's relay, in the range the INPUT filter admits,
	// serving a MAC this guest does not have. What it must not do is reach
	// its curated socket on this guest's behalf.
	otherSocket := filepath.Join(t.TempDir(), "curated.sock")
	otherUDS, err := net.Listen("unix", otherSocket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = otherUDS.Close() })
	reached := make(chan struct{}, 1)
	go func() {
		if c, err := otherUDS.Accept(); err == nil {
			_ = c.Close()
			reached <- struct{}{}
		}
	}()
	otherRelayPort, err := freeRelayPort()
	if err != nil {
		t.Fatal(err)
	}
	otherWork := t.TempDir()
	otherRelay, err := StartMCPRelay(otherSocket, vmBridgeGatewayIP, otherRelayPort,
		filepath.Join(otherWork, "relay.log"), filepath.Join(otherWork, "relay.pid"),
		microvm.MACFor("other-workspace"), vmDHCPLeaseFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = otherRelay.Stop() })

	canConnect := func(port string) bool {
		_, err := run("timeout 5 bash -c 'exec 3<>/dev/tcp/" + vmBridgeGatewayIP + "/" + port + "'")
		return err == nil
	}
	_, _ = run("timeout 5 bash -c 'exec 3<>/dev/tcp/" + vmBridgeGatewayIP + "/" + strings.TrimSpace(string(ownPort)) + "; printf x >&3'")
	select {
	case <-ownReached:
	case <-time.After(5 * time.Second):
		t.Errorf("guest's connection to its own mcp-relay (port %s) never reached its curated socket", strings.TrimSpace(string(ownPort)))
	}
	if canConnect(strconv.Itoa(otherPort)) {
		t.Errorf("guest reached an unrelated host listener on %s:%d, want it blocked", vmBridgeGatewayIP, otherPort)
	}
	_, _ = run("timeout 5 bash -c 'exec 3<>/dev/tcp/" + vmBridgeGatewayIP + "/" + strconv.Itoa(otherRelayPort) + "; printf x >&3; cat <&3'")
	select {
	case <-reached:
		t.Errorf("guest's connection to another workspace's relay (port %d) reached that workspace's curated socket", otherRelayPort)
	case <-time.After(time.Second):
	}
}

// guestRunner returns a function running one shell command in VM id over
// SSH and returning its combined output.
func guestRunner(t *testing.T, attachArgs func(string, ...string) ([]string, error), id string) func(string) (string, error) {
	t.Helper()
	return func(remoteCmd string) (string, error) {
		args, err := attachArgs(id, remoteCmd)
		if err != nil {
			return "", err
		}
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		return string(out), err
	}
}

// waitForGuestSSH blocks until the guest accepts SSH. Start returns once the
// guest has a DHCP lease, but sshd starts only after the guest has
// generated its host keys, which can take several more seconds.
func waitForGuestSSH(t *testing.T, run func(string) (string, error)) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		out, err := run("true")
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("guest sshd never became reachable: %v\n%s", err, out)
		}
		time.Sleep(time.Second)
	}
}
