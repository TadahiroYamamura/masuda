package mcpaggregator

import (
	"context"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxToolNameLen mirrors go-sdk's own validateToolName limit
// (mcp/tool.go), so a too-long prefixed name is caught here with a clear
// log line instead of a name AddTool would silently reject.
const maxToolNameLen = 128

// registerProxy registers a transparent proxy for child tool t onto
// curated, namespaced as "<serverName>__<t.Name>" to avoid collisions
// between child servers (and with curated's own built-in
// wait_for_gate_change/resolve_gate_from_chat, which no server name can
// equal since "__" never appears in a bare tool call). Uses the low-level,
// non-generic (*mcp.Server).AddTool -- t's schema is only known at
// runtime (it comes from ListTools against an external process, not a
// compile-time Go type), so the generic mcp.AddTool[In, Out] used
// elsewhere in this codebase (mcpserver.New/NewCurated) doesn't apply
// here.
//
// t comes from a child MCP server this package only trusts as far as the
// project's declaration went (Config.MCPServerDecl.Tools is an allowlist
// by name, not a schema review) -- AddTool panics on a missing or
// malformed InputSchema (see its doc comment in go-sdk), and one
// misbehaving child must never be able to crash the whole state daemon
// (wait_for_gate_change and friends would go down with it). recover here
// turns that into a skip-and-log instead.
//
// Reports whether registration happened.
func registerProxy(curated *mcp.Server, serverName string, session *mcp.ClientSession, t *mcp.Tool) (ok bool) {
	proxiedName := serverName + "__" + t.Name
	if len(proxiedName) > maxToolNameLen {
		log.Printf("mcp aggregator: %q/%q: proxied name %q exceeds %d chars, skipping",
			serverName, t.Name, proxiedName, maxToolNameLen)
		return false
	}
	if t.InputSchema == nil {
		log.Printf("mcp aggregator: %q/%q: no input schema, skipping", serverName, t.Name)
		return false
	}
	childToolName := t.Name // captured for the closure below

	defer func() {
		if r := recover(); r != nil {
			log.Printf("mcp aggregator: %q/%q: registering proxy tool %q panicked, skipping: %v",
				serverName, t.Name, proxiedName, r)
			ok = false
		}
	}()

	curated.AddTool(&mcp.Tool{
		Name:         proxiedName,
		Description:  t.Description,
		InputSchema:  t.InputSchema,
		OutputSchema: t.OutputSchema,
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return session.CallTool(ctx, &mcp.CallToolParams{
			Name:      childToolName,
			Arguments: req.Params.Arguments,
		})
	})
	return true
}
