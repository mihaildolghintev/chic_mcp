// Package mcpserver exposes the MoySklad aggregation layer as MCP tools over
// the Streamable HTTP transport.
package mcpserver

import (
	"context"
	"reflect"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"mcp.chic.md/internal/moysklad"
)

// MoyskladAPI is the subset of the MoySklad client the tools depend on. Keeping
// it an interface lets the server be tested with a fake, no HTTP required.
type MoyskladAPI interface {
	ListProducts(ctx context.Context, opts moysklad.ListOptions) ([]moysklad.Product, error)
	GetDashboard(ctx context.Context, period string) (*moysklad.Dashboard, error)
	ProfitByProduct(ctx context.Context, variant bool, opts moysklad.ProfitOptions) ([]moysklad.ProfitByProductRow, error)
	ProfitByEntity(ctx context.Context, dimension string, opts moysklad.ProfitOptions) ([]moysklad.ProfitByEntityRow, error)
	GetTurnover(ctx context.Context, opts moysklad.ProfitOptions) ([]moysklad.TurnoverRow, error)
	GetStock(ctx context.Context, opts moysklad.StockOptions) ([]moysklad.StockRow, error)
	GetCounterpartyReport(ctx context.Context, filter []string, limit int) ([]moysklad.CounterpartyRow, error)
	GetMoneySeries(ctx context.Context, from, to, interval string) (*moysklad.MoneySeries, error)
	SearchDocuments(ctx context.Context, docType moysklad.DocumentType, q moysklad.DocumentQuery) ([]moysklad.Document, error)
	GetDocument(ctx context.Context, docType moysklad.DocumentType, id string, expand []string) (*moysklad.Document, error)
	SearchCounterparties(ctx context.Context, opts moysklad.ListOptions) ([]moysklad.Counterparty, error)
	AccountCurrency(ctx context.Context) (*moysklad.Currency, error)
}

// toolRegistrations collects every tool's registration function. Each tool file
// appends to it in an init(); New() applies them all. Adding a tool is a new
// file plus a register() call — no edit to this file.
var toolRegistrations []func(*server.MCPServer, MoyskladAPI)

func register(f func(*server.MCPServer, MoyskladAPI)) {
	toolRegistrations = append(toolRegistrations, f)
}

// New builds an MCP server with all MoySklad tools registered.
func New(api MoyskladAPI) *server.MCPServer {
	s := server.NewMCPServer(
		"moysklad-mcp",
		"0.1.0",
		server.WithToolCapabilities(true),
	)
	for _, reg := range toolRegistrations {
		reg(s, api)
	}
	return s
}

// NewStreamableHTTP wraps the MCP server in a stateless Streamable HTTP handler
// mounted at /mcp — the transport remote clients (Claude, Antigravity) speak.
func NewStreamableHTTP(api MoyskladAPI) *server.StreamableHTTPServer {
	return server.NewStreamableHTTPServer(
		New(api),
		server.WithEndpointPath("/mcp"),
		server.WithStateLess(true),
	)
}

// resultOrError converts a Go value to a JSON tool result, or an err to a tool
// error result (so the model sees the failure rather than a transport error).
func resultOrError[T any](v T, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultErrorFromErr("moysklad request failed", err), nil
	}
	return mcp.NewToolResultJSON(asObject(v))
}

// asObject guarantees the tool's structured content is a JSON object, which the
// MCP spec requires (a top-level array is rejected by clients as not a valid
// dictionary). Slice/array results are wrapped as {items, count}; structs, maps
// and other objects pass through unchanged.
func asObject(v any) any {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return v
		}
		rv = rv.Elem()
	}
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		return map[string]any{"items": v, "count": rv.Len()}
	}
	return v
}
