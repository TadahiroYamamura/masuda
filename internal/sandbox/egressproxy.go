package sandbox

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// egressProxyStartupTimeout bounds how long EnsureEgressProxy waits for
// the proxy to start listening before giving up.
const egressProxyStartupTimeout = 5 * time.Second

// egressProxyBinary is the binary EnsureEgressProxy runs. A plain,
// unprivileged listener -- connections arrive already redirected to it by
// an iptables REDIRECT rule (see cmd/masuda-egress-proxy's own doc
// comment for why this used to need CAP_NET_ADMIN and no longer does).
const egressProxyBinary = "masuda-egress-proxy"

// egressProxyBind/egressProxyPort: the shared egress proxy (Issue #11)
// listens on a single, fixed address for the whole host -- unlike
// mcp-relay/virtiofsd/etc, which are one-per-workspace with a randomly
// chosen port (freePort()), this is intentionally one shared process for
// every VM regardless of workspace (see internal/egressproxy.Proxy's doc
// comment for why: a per-workspace proxy would need netfilter rules added
// and removed dynamically per VM, which can only be done via a
// low-level nftables library with no way to test it outside a privileged
// real-machine environment). A fixed, well-known port means
// scripts/setup-vm-host.sh's REDIRECT iptables rule can be static too --
// nothing needs to discover it at runtime.
const (
	egressProxyBind = vmBridgeGatewayIP
	egressProxyPort = 39218 // arbitrary, fixed; adjacent to mcp-relay's 39217 (runtime/entrypoint.sh)
)

func egressProxyPIDPath() (string, error) {
	dataHome, err := workspace.DataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataHome, "egress-proxy.pid"), nil
}

func egressProxyLogPath() (string, error) {
	dataHome, err := workspace.DataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataHome, "egress-proxy.log"), nil
}

// EnsureEgressProxy starts masuda's shared egress proxy (Issue #11) if it
// isn't already running, and does nothing if it is -- the same
// idempotent, "make sure the shared thing exists" shape as EnsureTap, not
// StartMCPRelay's per-workspace one-shot start. Safe to call on every
// VMBackend.Start: most calls after the first on a given host just find
// the existing process already listening.
//
// VMBackend.Stop deliberately never stops this -- other workspaces' VMs
// may still be relying on it, and there's no per-workspace reference
// count to decide when the last one goes away. It keeps running until the
// host reboots or a human kills it by hand.
func EnsureEgressProxy() error {
	addr := net.JoinHostPort(egressProxyBind, strconv.Itoa(egressProxyPort))
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		conn.Close()
		return nil // already running
	}

	if _, err := exec.LookPath(egressProxyBinary); err != nil {
		return fmt.Errorf("%s not found on PATH (required for VM egress filtering, Issue #11 -- see docs/INSTALLATION.md): %w", egressProxyBinary, err)
	}
	pidPathVal, err := egressProxyPIDPath()
	if err != nil {
		return err
	}
	logPathVal, err := egressProxyLogPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPathVal), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(logPathVal), err)
	}

	logFile, err := os.Create(logPathVal)
	if err != nil {
		return fmt.Errorf("creating egress-proxy log %s: %w", logPathVal, err)
	}
	defer logFile.Close()

	cmd := exec.Command(egressProxyBinary,
		"--bind", egressProxyBind,
		"--port", strconv.Itoa(egressProxyPort),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := startBackgroundProcess(cmd, pidPathVal); err != nil {
		return fmt.Errorf("starting egress-proxy: %w", err)
	}

	if err := waitForTCP(addr, egressProxyStartupTimeout); err != nil {
		_ = stopBackgroundProcess(cmd.Process, pidPathVal)
		return fmt.Errorf("egress-proxy did not start listening in time: %w", err)
	}
	return nil
}

// ResolveWorkspaceByIP finds which workspace's VM currently holds a DHCP
// lease for clientIP (Issue #11's egress proxy sees only a connecting VM's
// IP and has to work backward to "which workspace, and therefore which
// allowlist, is this"). MACFor is one-way (workspace id -> MAC), so this
// checks every currently-known workspace's own MAC against the lease
// rather than computing the answer directly -- cheap in practice, since
// the number of concurrently running workspaces is small (masuda's own
// guidance, docs/backlog-agent.md, is ~3). Returns ok=false if clientIP
// has no current lease, or no known workspace's MAC matches it (e.g. a
// stale lease from an already-removed workspace).
// leaseFilePath is a parameter, not a direct reference to the
// vmDHCPLeaseFile constant, purely so tests can point it at a throwaway
// file instead of the real host's dnsmasq lease file.
func ResolveWorkspaceByIP(clientIP net.IP, leaseFilePath string) (id, repoRoot string, ok bool, err error) {
	mac, err := findLeaseMAC(clientIP.String(), leaseFilePath)
	if err != nil {
		return "", "", false, nil
	}
	infos, err := workspace.ListAll()
	if err != nil {
		return "", "", false, err
	}
	for _, info := range infos {
		if strings.EqualFold(MACFor(info.ID), mac) {
			return info.ID, info.RepoRoot, true, nil
		}
	}
	return "", "", false, nil
}

// NewEgressAllowlistFunc returns an egressproxy.AllowlistFunc (taking that
// exact shape here, rather than importing the package, to keep
// internal/sandbox from depending on internal/egressproxy for a single
// function type) that resolves a connecting client's workspace
// (ResolveWorkspaceByIP) and checks the requested hostname against that
// workspace's own allowlist (resolveEgressAllowlist). A client IP that
// doesn't map to any known workspace is denied outright -- there is no
// host-wide default allowlist to fall back to.
func NewEgressAllowlistFunc() func(clientIP net.IP, hostname string) bool {
	return func(clientIP net.IP, hostname string) bool {
		_, repoRoot, ok, err := ResolveWorkspaceByIP(clientIP, vmDHCPLeaseFile)
		if err != nil || !ok {
			return false
		}
		allowed, err := resolveEgressAllowlist(repoRoot)
		if err != nil {
			return false
		}
		return slices.Contains(allowed, hostname)
	}
}
