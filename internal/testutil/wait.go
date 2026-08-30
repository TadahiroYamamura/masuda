// Package testutil holds helpers shared by masuda's tests across packages.
//
// Nothing here imports "testing" and nothing takes a *testing.T. A non-test
// package that imports "testing" registers test-only flags into any binary
// that links it, and callers get better failure attribution from their own
// t.Fatal anyway -- so these return errors and the caller decides what to do
// with them.
package testutil

import (
	"fmt"
	"net"
	"time"
)

// DefaultTimeout bounds the waits below. It is generous on purpose: the
// deadline exists so a broken server reports a failure instead of hanging,
// not to assert anything about latency. `go test ./...` runs packages in
// parallel and internal/rootfs builds container images while they run, so a
// tight bound turns a working server into a flake.
const DefaultTimeout = 10 * time.Second

// WaitForUDS blocks until socketPath accepts a connection, and is what a
// test should wait on before dialing a daemon it just started.
//
// Waiting for the socket *file* to appear is not the same thing, which is
// the whole reason this exists. net.Listen creates the file at bind() and
// only starts accepting at listen(), so an os.Stat can hand back a path that
// still answers connect() with ECONNREFUSED. That gap is not theoretical:
// pinning a package to a single CPU turned it into a real failure in
// 6/150 (internal/statedaemon/mcpserver) to 19/40 (internal/gate) of runs,
// and it is what Issue #42 spent a long time looking like.
//
// serveErr short-circuits the wait when the server stopped instead, so the
// failure says why rather than surfacing as a refused connection several
// lines later. A nil channel is fine when the caller has none.
func WaitForUDS(socketPath string, serveErr <-chan error) error {
	return waitForAccept("unix", socketPath, serveErr, false)
}

// WaitForTCP is WaitForUDS's TCP twin, with one extra check the Unix-socket
// side does not need: a dial to an ephemeral port with nothing listening on
// it can connect to *itself*. The kernel is free to pick the destination
// port as this socket's own source port, and loopback TCP completes the
// simultaneous open, which is indistinguishable from success unless the two
// ends are compared.
//
// Note what this still cannot tell you. If the port came from the
// bind-zero-then-close trick (see internal/sandbox.freePort and friends),
// that listener stays in LISTEN for a moment after Close() returns and will
// answer this probe on behalf of a server that has not bound anything yet
// -- confirmed in /proc/net/tcp, where the socket the probe reached carried
// the same inode as the one just closed. When the test can instead retry the
// real thing it is after (opening a session, say), prefer that over probing
// a port whose readiness it cannot actually attribute.
func WaitForTCP(addr string, serveErr <-chan error) error {
	return waitForAccept("tcp", addr, serveErr, true)
}

func waitForAccept(network, addr string, serveErr <-chan error, rejectSelfConnect bool) error {
	deadline := time.Now().Add(DefaultTimeout)
	lastErr := fmt.Errorf("never dialled")
	for time.Now().Before(deadline) {
		select {
		case err := <-serveErr:
			return fmt.Errorf("serving %s stopped before it accepted anything: %w", addr, err)
		default:
		}

		conn, err := net.Dial(network, addr)
		if err != nil {
			lastErr = err
		} else {
			selfConnected := rejectSelfConnect && conn.LocalAddr().String() == conn.RemoteAddr().String()
			conn.Close()
			if !selfConnected {
				return nil
			}
			lastErr = fmt.Errorf("the dial kept connecting to itself, so nothing was listening")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("nothing accepting on %s within %s: %w", addr, DefaultTimeout, lastErr)
}
