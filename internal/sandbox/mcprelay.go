package sandbox

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// mcpRelayStartupTimeout bounds how long StartMCPRelay waits for the relay
// to start listening before giving up.
const mcpRelayStartupTimeout = 5 * time.Second

// resolveMasudaExe returns the path to the masuda binary StartMCPRelay
// re-execs for `internal mcp-relay` (the same self-exec pattern
// internal/statedaemon's detached daemon uses). A var, not a bare
// os.Executable() call, so tests can point it at a real built masuda binary
// instead of the `go test` binary os.Executable() would otherwise resolve
// to -- that binary doesn't understand `internal mcp-relay` at all.
var resolveMasudaExe = os.Executable

// MCPRelayProcess is a running `masuda internal mcp-relay` instance,
// bridging TCP connections on Addr to a Unix domain socket.
//
// For the VM path (Issue #31 M5-4), this runs on the *host*, bound to the
// TAP/bridge-facing address, not loopback: the guest has no way to reach a
// UDS socket at all -- virtiofs can't share one across kernels (confirmed
// live in M3: a bind-mounted socket special file just doesn't connect()
// from the other side). The guest's Claude Code points --mcp-config
// straight at Addr; no relay process runs inside the guest, unlike the
// Docker path where entrypoint.sh starts one inside the container itself
// (there, the UDS socket really is reachable, via the bind mount, so the
// relay only needs to bridge it to a local TCP port for --mcp-config).
type MCPRelayProcess struct {
	cmd     *exec.Cmd
	pidFile string
	Addr    string // bind:port, for callers to hand to Claude Code's --mcp-config
}

// StartMCPRelay launches `masuda internal mcp-relay --socket socketPath
// --bind bind --port port` (re-invoking the current masuda binary itself,
// the same self-exec pattern internal/statedaemon's detached daemon uses),
// logging its stdout/stderr to logPath. Safe to call again for the same
// bind:port a crashed previous run left behind -- see
// startBackgroundProcess.
//
// pidFile is passed in rather than derived from the listen address the way
// virtiofsd's is derived from its socket path. A socket path is absolute, so
// pidPath lands the file next to it; a bind:port is not, so the same
// derivation dropped "192.168.200.1:44907.pid" into whatever directory the
// CLI happened to run from -- the target repository's root, in practice.
func StartMCPRelay(socketPath, bind string, port int, logPath, pidFile string) (*MCPRelayProcess, error) {
	exe, err := resolveMasudaExe()
	if err != nil {
		return nil, fmt.Errorf("locating masuda binary: %w", err)
	}

	addr := net.JoinHostPort(bind, strconv.Itoa(port))

	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("creating mcp-relay log %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "internal", "mcp-relay",
		"--socket", socketPath,
		"--bind", bind,
		"--port", strconv.Itoa(port),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := startBackgroundProcess(cmd, pidFile, socketPath); err != nil {
		return nil, fmt.Errorf("starting mcp-relay on %s: %w", addr, err)
	}

	if err := waitForTCP(addr, mcpRelayStartupTimeout); err != nil {
		_ = stopBackgroundProcess(cmd.Process, pidFile)
		return nil, fmt.Errorf("mcp-relay on %s did not start listening in time: %w", addr, err)
	}

	return &MCPRelayProcess{cmd: cmd, pidFile: pidFile, Addr: addr}, nil
}

// Stop terminates the mcp-relay process and removes its pid file. Not an
// error if it's already exited on its own.
func (m *MCPRelayProcess) Stop() error {
	return stopBackgroundProcess(m.cmd.Process, m.pidFile)
}

func waitForTCP(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s to accept connections: %w", addr, lastErr)
}
