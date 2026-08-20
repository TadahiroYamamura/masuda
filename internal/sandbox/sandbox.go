// Package sandbox manages masuda's per-workspace sandbox: a Cloud
// Hypervisor microVM (VMBackend, internal/sandbox/vmbackend.go) built from
// a Docker image (internal/rootfs.Build converts it to a bootable rootfs --
// `docker build`/`docker export` still run, but never `docker run`: nothing
// here executes as a container any more, see the Backend interface's own
// doc comment for the history).
//
// Everything here is keyed by workspace ID, not branch name: two workspaces
// targeting the same branch (roadmap step 7) must get independent sandboxes,
// so naming can't be derived from the branch alone.
package sandbox

import (
	"net"
	"regexp"
)

// tmuxSession must match runtime/entrypoint.sh's SESSION.
const tmuxSession = "claude-work"

// Handle identifies a running sandbox.
type Handle struct {
	ID            string
	ContainerName string
	HostPort      int
}

// nameSanitizer keeps generated names (TapName, and previously
// DockerBackend's ContainerName) within the character sets their respective
// namespaces allow; workspace IDs are already sanitized to a safe subset
// (internal/workspace.NewID), but this stays defensive in case that ever
// changes.
var nameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// freePort asks the OS for an unused TCP port. There's an inherent TOCTOU
// race between closing this listener and whatever binds the port next
// (VMBackend's mcp-relay), but it's an acceptable risk for a
// per-invocation allocation, not a long-lived service.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
