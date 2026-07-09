package mcpserver

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestStreamableHTTP_EndToEnd boots the real Streamable HTTP handler over an
// httptest server and drives it with the Streamable HTTP client — proving the
// remote transport (the one Claude/Antigravity speak) is wired correctly.
func TestStreamableHTTP_EndToEnd(t *testing.T) {
	handler := NewStreamableHTTP(&fakeAPI{})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := client.NewStreamableHttpClient(srv.URL + "/mcp")
	if err != nil {
		t.Fatalf("NewStreamableHttpClient: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("client start: %v", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "1.0.0"}
	initRes, err := c.Initialize(ctx, initReq)
	if err != nil {
		t.Fatalf("initialize over HTTP: %v", err)
	}
	if initRes.ServerInfo.Name != "moysklad-mcp" {
		t.Errorf("server name = %q, want moysklad-mcp", initRes.ServerInfo.Name)
	}

	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools over HTTP: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"list_products", "get_profit", "abc_analysis", "receivables_aging"} {
		if !names[want] {
			t.Errorf("tools/list over HTTP missing %q (got %d tools)", want, len(tools.Tools))
		}
	}
}
