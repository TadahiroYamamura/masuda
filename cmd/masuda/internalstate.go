package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpclient"
)

// newInternalStateCommand builds `masuda internal state`, a thin CLI wrapper
// around internal/statedaemon/mcpclient's trusted tool calls. This exists
// so orchestrator/*.py (Python, inside the sandbox) never has to embed its
// own MCP client -- it shells out to this subcommand once per operation
// instead, keeping the wire protocol implementation in one place (Go). See
// Issue #35's design notes.
func newInternalStateCommand() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:    "state",
		Hidden: true,
		Short:  "Talk to the workspace's state daemon (used by orchestrator/*.py via subprocess)",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if socket == "" {
				stateDir := os.Getenv("MASUDA_STATE_DIR")
				if stateDir == "" {
					return fmt.Errorf("--socket not given and MASUDA_STATE_DIR is not set")
				}
				socket = statedaemon.SocketPath(stateDir)
			}
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(&socket, "socket", "", "path to the daemon's Unix domain socket (default: $MASUDA_STATE_DIR/daemon.sock)")

	cmd.AddCommand(&cobra.Command{
		Use:   "get <key>",
		Short: "Print {\"value\":..,\"found\":..} for key as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, socket, func(ctx context.Context, c *mcpclient.Client) error {
				value, found, err := c.Get(ctx, args[0])
				if err != nil {
					return err
				}
				return printJSON(cmd, map[string]any{"value": value, "found": found})
			})
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "put <key> <value>",
		Short: "Set key's value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, socket, func(ctx context.Context, c *mcpclient.Client) error {
				return c.Put(ctx, args[0], args[1])
			})
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "delete <key>",
		Short: "Delete key (not an error if it doesn't exist)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, socket, func(ctx context.Context, c *mcpclient.Client) error {
				return c.Delete(ctx, args[0])
			})
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "list <prefix>",
		Short: "Print {\"keys\":[..]} for every key starting with prefix, sorted",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, socket, func(ctx context.Context, c *mcpclient.Client) error {
				keys, err := c.List(ctx, args[0])
				if err != nil {
					return err
				}
				return printJSON(cmd, map[string]any{"keys": keys})
			})
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "wait <key>",
		Short: "Block until the next put/delete on key, then print {\"value\":..,\"found\":..} as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, socket, func(ctx context.Context, c *mcpclient.Client) error {
				value, found, err := c.WaitForChange(ctx, args[0])
				if err != nil {
					return err
				}
				return printJSON(cmd, map[string]any{"value": value, "found": found})
			})
		},
	})

	return cmd
}

// withClient connects to socket, runs fn, and always closes the connection
// afterward. ctx is cancelled on SIGINT/SIGTERM so `wait` (otherwise
// unboundedly blocking, like ADR-0017's inotifywait) can be interrupted
// cleanly.
func withClient(cmd *cobra.Command, socket string, fn func(context.Context, *mcpclient.Client) error) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client, err := mcpclient.Dial(ctx, socket)
	if err != nil {
		return err
	}
	defer client.Close()
	return fn(ctx, client)
}

func printJSON(cmd *cobra.Command, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}
