package mcpaggregator

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegisterProxyRecoversFromMalformedSchema(t *testing.T) {
	curated := mcp.NewServer(&mcp.Implementation{Name: "test-curated", Version: "0.0.1"}, nil)

	// A child MCP server is outside masuda's trust boundary (the project
	// only allowlists it by name -- Config.MCPServerDecl.Tools -- it
	// never reviews the schema). AddTool panics on a schema whose "type"
	// isn't "object" (go-sdk's own validation); registerProxy must
	// recover from that rather than let one misbehaving child crash the
	// whole state daemon. session is nil here because the panic happens
	// during registration, before the returned handler closure (which
	// would dereference session) is ever invoked.
	badTool := &mcp.Tool{Name: "broken", InputSchema: map[string]any{"type": "string"}}

	if ok := registerProxy(curated, "childserver", nil, badTool); ok {
		t.Fatal("registerProxy() = true, want false for a malformed schema")
	}
}

func TestRegisterProxySkipsNilSchema(t *testing.T) {
	curated := mcp.NewServer(&mcp.Implementation{Name: "test-curated", Version: "0.0.1"}, nil)
	tool := &mcp.Tool{Name: "no_schema"}

	if ok := registerProxy(curated, "childserver", nil, tool); ok {
		t.Fatal("registerProxy() = true, want false for a nil InputSchema")
	}
}

func TestRegisterProxySkipsTooLongName(t *testing.T) {
	curated := mcp.NewServer(&mcp.Implementation{Name: "test-curated", Version: "0.0.1"}, nil)
	longName := ""
	for len(longName) <= maxToolNameLen {
		longName += "x"
	}
	tool := &mcp.Tool{Name: longName, InputSchema: map[string]any{"type": "object"}}

	if ok := registerProxy(curated, "childserver", nil, tool); ok {
		t.Fatal("registerProxy() = true, want false for a too-long proxied name")
	}
}
