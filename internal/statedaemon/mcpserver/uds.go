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

// ServeUDS serves store's trusted MCP tool set over a Unix domain socket at
// socketPath, blocking until ctx is cancelled or the listener fails.
//
// A stale socket file left by a previous, uncleanly-terminated daemon
// process is removed before binding -- the daemon has no supervisor to clean
// up after itself, the same situation internal/sandbox.Start's "docker rm -f
// the previous container" handles for Exited containers.
func ServeUDS(ctx context.Context, store *statedaemon.Store, socketPath string) error {
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	// The socket is a trusted local IPC channel carrying the full,
	// unrestricted tool set -- any local process that can open it gets full
	// read/write access to this workspace's state, so lock it to the current
	// user regardless of umask.
	if err := os.Chmod(socketPath, 0o600); err != nil {
		l.Close()
		return err
	}

	server := New(store)
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
