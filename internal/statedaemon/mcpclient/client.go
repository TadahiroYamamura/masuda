// Package mcpclient is a Go client for the trusted tool set
// internal/statedaemon/mcpserver exposes (state_get/state_put/state_delete/
// state_list/state_apply), reached over the workspace's daemon Unix domain
// socket.
//
// This is the one place that speaks the MCP wire protocol on the client
// side, used both in-process by cmd/masuda's Go commands and, via the
// `masuda internal state` CLI wrapper, out-of-process by
// orchestrator/*.py (which shells out rather than embedding its own MCP
// client -- see Issue #35's design notes).
package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Client is a connected session against a workspace's state daemon.
type Client struct {
	session *mcp.ClientSession
}

// Dial connects to the state daemon listening on the Unix domain socket at
// socketPath.
func Dial(ctx context.Context, socketPath string) (*Client, error) {
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	transport := &mcp.StreamableClientTransport{Endpoint: "http://unix/", HTTPClient: httpClient}
	client := mcp.NewClient(&mcp.Implementation{Name: "masuda-cli", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to state daemon at %s: %w", socketPath, err)
	}
	return &Client{session: session}, nil
}

// Close ends the session.
func (c *Client) Close() error { return c.session.Close() }

func (c *Client) call(ctx context.Context, name string, args, out any) error {
	res, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if res.IsError {
		return fmt.Errorf("%s: %s", name, toolErrorText(res))
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return fmt.Errorf("%s: marshaling result: %w", name, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: decoding result: %w", name, err)
	}
	return nil
}

func toolErrorText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "unknown tool error"
}

// Get returns key's current value and whether it exists.
func (c *Client) Get(ctx context.Context, key string) (value string, found bool, err error) {
	var out struct {
		Value string `json:"value"`
		Found bool   `json:"found"`
	}
	if err := c.call(ctx, "state_get", map[string]any{"key": key}, &out); err != nil {
		return "", false, err
	}
	return out.Value, out.Found, nil
}

// Put sets key's value.
func (c *Client) Put(ctx context.Context, key, value string) error {
	var out struct{}
	return c.call(ctx, "state_put", map[string]any{"key": key, "value": value}, &out)
}

// Delete removes key. Not an error if key doesn't exist.
func (c *Client) Delete(ctx context.Context, key string) error {
	var out struct{}
	return c.call(ctx, "state_delete", map[string]any{"key": key}, &out)
}

// List returns every key currently starting with prefix, sorted.
func (c *Client) List(ctx context.Context, prefix string) ([]string, error) {
	var out struct {
		Keys []string `json:"keys"`
	}
	if err := c.call(ctx, "state_list", map[string]any{"prefix": prefix}, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// Apply performs ops as one atomic step, reporting whether it applied. A
// false return means one of the ops' checks did not hold and nothing was
// changed -- the caller should read the keys again and decide again, not
// treat it as a failure.
func (c *Client) Apply(ctx context.Context, ops []statedaemon.Op) (applied bool, err error) {
	wire := make([]map[string]any, 0, len(ops))
	for _, op := range ops {
		o := map[string]any{"op": string(op.Kind), "key": op.Key}
		// Left out entirely rather than sent as null when nil, so a check
		// keeps meaning "this key must not exist" on the wire.
		if op.Value != nil {
			o["value"] = string(op.Value)
		}
		wire = append(wire, o)
	}
	var out struct {
		Applied bool `json:"applied"`
	}
	if err := c.call(ctx, "state_apply", map[string]any{"ops": wire}, &out); err != nil {
		return false, err
	}
	return out.Applied, nil
}
