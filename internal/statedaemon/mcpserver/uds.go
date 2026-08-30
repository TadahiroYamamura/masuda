package mcpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServeUDS serves store's trusted MCP tool set (New) over a Unix domain
// socket at socketPath, blocking until ctx is cancelled or the listener
// fails.
//
// The socket file appears at bind(), a moment before this starts accepting,
// so waiting for the file to exist is not the same as waiting for the
// server: a test that does the former dials into that gap and fails with
// ECONNREFUSED under load (Issue #42). Wait with
// internal/testutil.WaitForUDS instead.
func ServeUDS(ctx context.Context, store *statedaemon.Store, socketPath string) error {
	return serveUDS(ctx, New(store), socketPath)
}

// ServeCuratedUDS serves store's curated, Claude-facing MCP tool set
// (NewCurated) over a Unix domain socket at socketPath, blocking until ctx
// is cancelled or the listener fails. ServeUDS's caveat about the socket
// file appearing before the server accepts applies here too. No privileged-command runner, so that
// tool is not offered -- the real daemon builds the server itself and
// passes one (see ServeCuratedServerUDS and runStatedaemon).
func ServeCuratedUDS(ctx context.Context, store *statedaemon.Store, socketPath string) error {
	return ServeCuratedServerUDS(ctx, NewCurated(store, nil), socketPath)
}

// ServeCuratedServerUDS serves an already-constructed curated *mcp.Server
// over socketPath, blocking until ctx is cancelled or the listener fails.
// ServeUDS's caveat about the socket file appearing before the server
// accepts applies here too.
// Unlike ServeCuratedUDS, the caller builds (and may keep mutating) the
// server itself -- e.g. internal/statedaemon/mcpaggregator registering
// child-MCP-server proxy tools onto it, potentially after serving has
// already started (mcp.Server.AddTool is safe to call at any time).
func ServeCuratedServerUDS(ctx context.Context, server *mcp.Server, socketPath string) error {
	return serveUDS(ctx, server, socketPath)
}

// serveUDS binds socketPath and serves server's tool set over it until ctx
// is cancelled or the listener fails.
//
// A stale socket file left by a previous, uncleanly-terminated daemon
// process is removed before binding -- the daemon has no supervisor to clean
// up after itself, the same situation internal/sandbox.Start's "docker rm -f
// the previous container" handles for Exited containers.
func serveUDS(ctx context.Context, server *mcp.Server, socketPath string) error {
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	// Both the trusted and curated sockets are local IPC channels not meant
	// for other users on the same machine -- lock them down regardless of
	// umask. The trusted one carries full read/write access to this
	// workspace's state; the curated one is host-side-only anyway (the
	// guest reaches it via a vsock/UDS relay, not this file directly).
	if err := os.Chmod(socketPath, 0o600); err != nil {
		l.Close()
		return err
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	httpServer := &http.Server{Handler: handler}

	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.Serve(l) }()

	select {
	case <-ctx.Done():
		httpServer.Close()
		<-errCh
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
