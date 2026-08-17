package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// newInternalMCPRelayCommand builds `masuda internal mcp-relay`, a plain
// byte-level TCP<->Unix-domain-socket proxy. It exists so Claude Code's
// --mcp-config can point at a normal http://127.0.0.1:<port> URL (the only
// kind of address that flag understands) while the actual MCP server -- the
// state daemon's curated tool set, statedaemon.CuratedSocketPath -- only
// listens on a Unix domain socket.
//
// A dedicated relay subcommand rather than reaching for socat: masuda is
// already a single self-contained Go binary in both places this needs to
// run (baked into the Docker image, always present on the host) -- socat
// would be a new OS package dependency in both, for something this small.
// The same relay works unchanged whether the UDS socket is local (the
// phase 1-2 host loop, internal/hostloop.Start) or bind-mounted from a
// container's /masuda-state (phase 4-5's entrypoint.sh/start_claude.sh) --
// it has no idea which.
func newInternalMCPRelayCommand() *cobra.Command {
	var socketPath string
	var port int
	cmd := &cobra.Command{
		Use:    "mcp-relay",
		Hidden: true,
		Short:  "Relay TCP connections on 127.0.0.1:<port> to a Unix domain socket, for Claude Code's --mcp-config",
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if socketPath == "" {
				return fmt.Errorf("--socket is required")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runMCPRelay(ctx, socketPath, port)
		},
	}
	cmd.Flags().StringVar(&socketPath, "socket", "", "Unix domain socket to relay to (required)")
	cmd.Flags().IntVar(&port, "port", 0, "TCP port to listen on at 127.0.0.1 (required, non-zero)")
	return cmd
}

// runMCPRelay listens on 127.0.0.1:port and, for every accepted connection,
// dials socketPath and pipes bytes bidirectionally until either side closes.
// Blocks until ctx is cancelled.
func runMCPRelay(ctx context.Context, socketPath string, port int) error {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
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
