package sandbox

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/microvm"
)

const apiGatewayStartupTimeout = 5 * time.Second

// APIGatewayProcess is a running `masuda internal api-gateway` for one
// workspace's VM.
type APIGatewayProcess struct {
	cmd     *exec.Cmd
	pidFile string
	Addr    string // bind:port, what the guest's ANTHROPIC_BASE_URL points at
}

// StartAPIGateway launches `masuda internal api-gateway` for the VM holding
// allowMAC's DHCP lease, re-execing the current binary the same way
// StartMCPRelay does. The token itself is never passed on the command line
// -- only tokenFile, which the gateway reads per request -- so it does not
// show up in `ps`.
//
// allowMAC doubles as the process's identity for KillStalePID: it is the
// one argument unique to this workspace, where the token file path is
// shared by every workspace's gateway.
func StartAPIGateway(bind string, port int, tokenFile, logPath, pidFile, allowMAC, leaseFile string) (*APIGatewayProcess, error) {
	exe, err := resolveMasudaExe()
	if err != nil {
		return nil, fmt.Errorf("locating masuda binary: %w", err)
	}
	addr := net.JoinHostPort(bind, strconv.Itoa(port))

	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("creating api-gateway log %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "internal", "api-gateway",
		"--bind", bind,
		"--port", strconv.Itoa(port),
		"--token-file", tokenFile,
		"--allow-mac", allowMAC,
		"--lease-file", leaseFile,
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := microvm.StartBackgroundProcess(cmd, pidFile, allowMAC); err != nil {
		return nil, fmt.Errorf("starting api-gateway on %s: %w", addr, err)
	}
	if err := waitForTCP(addr, apiGatewayStartupTimeout); err != nil {
		_ = microvm.StopBackgroundProcess(cmd.Process, pidFile)
		return nil, fmt.Errorf("api-gateway on %s did not start listening in time: %w", addr, err)
	}
	return &APIGatewayProcess{cmd: cmd, pidFile: pidFile, Addr: addr}, nil
}

// Stop terminates the gateway and removes its pid file.
func (g *APIGatewayProcess) Stop() error {
	return microvm.StopBackgroundProcess(g.cmd.Process, g.pidFile)
}
