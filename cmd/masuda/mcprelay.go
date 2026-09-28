package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/microvm"
)

// newInternalMCPRelayCommand builds `masuda internal mcp-relay`, a plain
// byte-level TCP<->Unix-domain-socket proxy. It exists so Claude Code's
// --mcp-config can point at a normal http://127.0.0.1:<port> URL (the only
// kind of address that flag understands) while the actual MCP server -- the
// state daemon's curated tool set, statedaemon.CuratedSocketPath -- only
// listens on a Unix domain socket.
//
// A dedicated relay subcommand rather than reaching for socat: masuda is
// already a single self-contained Go binary on the host, and socat would
// be a new OS package dependency for something this small.
//
// --bind (Issue #31 M5-4): a VM has no shared UDS at all (confirmed live:
// virtiofs can't share a socket file across kernels, a shared socket
// special file just doesn't connect()), so this relay runs on the *host*,
// bound to the TAP/bridge-facing address (the bridge gateway IP,
// internal/sandbox.StartMCPRelay) instead of loopback, and the guest's
// Claude Code points --mcp-config straight at that address -- no relay
// process runs inside the guest at all. The 127.0.0.1 default serves the
// tests.
//
// --allow-mac/--lease-file: a relay bound to the bridge gateway is reachable
// from every guest on the bridge, and it forwards into one workspace's
// curated socket with no authentication of its own. With these set it
// serves only the guest whose DHCP lease maps the connecting IP to that
// MAC, so one workspace's VM cannot drive another workspace's gates or
// privileged commands through its relay.
func newInternalMCPRelayCommand() *cobra.Command {
	var socketPath, bind, allowMAC, leaseFile string
	var port int
	cmd := &cobra.Command{
		Use:    "mcp-relay",
		Hidden: true,
		Short:  "Relay TCP connections on <bind>:<port> to a Unix domain socket, for Claude Code's --mcp-config",
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if socketPath == "" {
				return fmt.Errorf("--socket is required")
			}
			if (allowMAC == "") != (leaseFile == "") {
				return fmt.Errorf("--allow-mac and --lease-file go together")
			}
			var allow func(net.Addr) bool
			if allowMAC != "" {
				allow = func(remote net.Addr) bool { return leaseMatches(remote, allowMAC, leaseFile) }
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runMCPRelay(ctx, socketPath, bind, port, allow)
		},
	}
	cmd.Flags().StringVar(&socketPath, "socket", "", "Unix domain socket to relay to (required)")
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "address to listen on")
	cmd.Flags().IntVar(&port, "port", 0, "TCP port to listen on (required, non-zero)")
	cmd.Flags().StringVar(&allowMAC, "allow-mac", "", "serve only the client whose DHCP lease has this MAC (requires --lease-file)")
	cmd.Flags().StringVar(&leaseFile, "lease-file", "", "dnsmasq lease file --allow-mac is checked against")
	return cmd
}

// leaseMatches reports whether remote's IP is currently leased to mac.
// Read on every connection rather than once at startup: the guest's lease
// can be renewed onto a different address while the relay runs.
func leaseMatches(remote net.Addr, mac, leaseFile string) bool {
	tcpAddr, ok := remote.(*net.TCPAddr)
	if !ok {
		return false
	}
	leased, err := microvm.FindLeaseMAC(tcpAddr.IP.String(), leaseFile)
	if err != nil {
		return false
	}
	return strings.EqualFold(leased, mac)
}

// runMCPRelay listens on bind:port and, for every accepted connection that
// allow admits (all of them when allow is nil), dials socketPath and pipes
// bytes bidirectionally until either side closes. Blocks until ctx is
// cancelled.
//
// The port is chosen by the caller, which every caller does by binding a
// candidate port and closing that listener (freeTCPPort in the tests,
// internal/sandbox.freeRelayPort). Besides the obvious TOCTOU those all
// accept, that leftover listener stays in LISTEN for a moment after Close()
// returns -- long enough to answer a readiness probe on behalf of a relay
// that has not bound anything yet, which is what Issue #42's "connection
// refused" turned out to be. A test waiting on this relay should retry the
// thing it actually wants rather than probe the port.
func runMCPRelay(ctx context.Context, socketPath, bind string, port int, allow func(net.Addr) bool) error {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		l.Close()
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if allow != nil && !allow(conn.RemoteAddr()) {
			conn.Close()
			continue
		}
		go relayConn(socketPath, conn)
	}
}

func relayConn(socketPath string, tcpConn net.Conn) {
	defer tcpConn.Close()
	var d net.Dialer
	udsConn, err := d.Dial("unix", socketPath)
	if err != nil {
		return
	}
	defer udsConn.Close()

	done := make(chan struct{}, 2)
	go func() { io.Copy(udsConn, tcpConn); done <- struct{}{} }()
	go func() { io.Copy(tcpConn, udsConn); done <- struct{}{} }()
	<-done
}
