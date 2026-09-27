// Package sandbox manages masuda's per-workspace sandbox: a Cloud
// Hypervisor microVM (VMBackend, internal/sandbox/vmbackend.go) built from
// a Docker image (internal/rootfs.Build converts it to a bootable rootfs --
// `docker build`/`docker export` still run, but never `docker run`: nothing
// here executes as a container any more, see the Backend interface's own
// doc comment for the history). Booting and tearing the VM down is
// internal/microvm's job; this package supplies what is masuda's own
// around it -- see that package's doc comment for where the line is.
//
// Everything here is keyed by workspace ID, not branch name: two workspaces
// targeting the same branch (roadmap step 7) must get independent sandboxes,
// so naming can't be derived from the branch alone.
package sandbox

import (
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
)

// tmuxSession must match runtime/entrypoint.sh's SESSION.
const tmuxSession = "claude-work"

// Handle identifies a running sandbox.
type Handle struct {
	ID            string
	ContainerName string
	HostPort      int
}

// relayPortMin/relayPortMax bound the ports a workspace VM's mcp-relay
// listens on at the bridge gateway. A fixed range rather than any free
// port, because scripts/setup-vm-host.sh's host INPUT filter has to name
// the ports guests may reach statically -- the two must match.
const (
	relayPortMin = 39300
	relayPortMax = 39399
)

// freeRelayPort finds an unused port in the relay range by binding each
// candidate on the bridge gateway. The scan starts at a random offset so
// two `masuda sandbox start` runs at once do not both race for the lowest
// free port. Like any bind-then-close allocation there's a TOCTOU window
// before the relay binds it; an acceptable risk for a per-invocation port.
func freeRelayPort() (int, error) {
	n := relayPortMax - relayPortMin + 1
	offset := rand.IntN(n)
	for i := range n {
		port := relayPortMin + (offset+i)%n
		l, err := net.Listen("tcp", net.JoinHostPort(vmBridgeGatewayIP, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		l.Close()
		return port, nil
	}
	return 0, fmt.Errorf("no free mcp-relay port in %d-%d on %s", relayPortMin, relayPortMax, vmBridgeGatewayIP)
}
