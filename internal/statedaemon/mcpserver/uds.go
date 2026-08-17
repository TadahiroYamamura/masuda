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
func ServeUDS(ctx context.Context, store *statedaemon.Store, socketPath string) error {
	return serveUDS(ctx, New(store), socketPath)
}

// ServeCuratedUDS serves store's curated, Claude-facing MCP tool set
// (NewCurated) over a Unix domain socket at socketPath, blocking until ctx
// is cancelled or the listener fails.
func ServeCuratedUDS(ctx context.Context, store *statedaemon.Store, socketPath string) error {
	return serveUDS(ctx, NewCurated(store), socketPath)
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
