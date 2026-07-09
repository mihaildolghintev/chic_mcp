package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"mcp.chic.md/internal/aggregate"
	"mcp.chic.md/internal/moysklad"
)

// fakeAPI records the options it was called with and returns canned data. It
// implements the full MoyskladAPI; unset fields return zero values so each test
// only populates what it needs.
type fakeAPI struct {
	gotOpts  moysklad.ListOptions
	products []moysklad.Product
	err      error

	dashboard        *moysklad.Dashboard
	profitProduct    []moysklad.ProfitByProductRow
	profitEntity     []moysklad.ProfitByEntityRow
	turnover         []moysklad.TurnoverRow
	stock            []moysklad.StockRow
	counterpartyRows []moysklad.CounterpartyRow
	money            *moysklad.MoneySeries
	documents        []moysklad.Document
	document         *moysklad.Document
	counterparties   []moysklad.Counterparty

	gotProfitDimension string
	gotDocType         moysklad.DocumentType
	gotDocQuery        moysklad.DocumentQuery
}

func (f *fakeAPI) ListProducts(_ context.Context, opts moysklad.ListOptions) ([]moysklad.Product, error) {
	f.gotOpts = opts
	return f.products, f.err
}

func (f *fakeAPI) GetDashboard(context.Context, string) (*moysklad.Dashboard, error) {
	return f.dashboard, f.err
}

func (f *fakeAPI) ProfitByProduct(_ context.Context, _ bool, _ moysklad.ProfitOptions) ([]moysklad.ProfitByProductRow, error) {
	return f.profitProduct, f.err
}

func (f *fakeAPI) ProfitByEntity(_ context.Context, dimension string, _ moysklad.ProfitOptions) ([]moysklad.ProfitByEntityRow, error) {
	f.gotProfitDimension = dimension
	return f.profitEntity, f.err
}

func (f *fakeAPI) GetTurnover(context.Context, moysklad.ProfitOptions) ([]moysklad.TurnoverRow, error) {
	return f.turnover, f.err
}

func (f *fakeAPI) GetStock(context.Context, moysklad.StockOptions) ([]moysklad.StockRow, error) {
	return f.stock, f.err
}

func (f *fakeAPI) GetCounterpartyReport(context.Context, []string, int) ([]moysklad.CounterpartyRow, error) {
	return f.counterpartyRows, f.err
}

func (f *fakeAPI) GetMoneySeries(context.Context, string, string, string) (*moysklad.MoneySeries, error) {
	return f.money, f.err
}

func (f *fakeAPI) SearchDocuments(_ context.Context, docType moysklad.DocumentType, q moysklad.DocumentQuery) ([]moysklad.Document, error) {
	f.gotDocType = docType
	f.gotDocQuery = q
	return f.documents, f.err
}

func (f *fakeAPI) GetDocument(_ context.Context, _ moysklad.DocumentType, _ string, _ []string) (*moysklad.Document, error) {
	return f.document, f.err
}

func (f *fakeAPI) SearchCounterparties(_ context.Context, opts moysklad.ListOptions) ([]moysklad.Counterparty, error) {
	f.gotOpts = opts
	return f.counterparties, f.err
}

func newInProcess(t *testing.T, api MoyskladAPI) *client.Client {
	t.Helper()
	c, err := client.NewInProcessClient(New(api))
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("client start: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "1.0.0"}
	if _, err := c.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return c
}

func TestToolsList_ExposesListProducts(t *testing.T) {
	c := newInProcess(t, &fakeAPI{})
	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var found *mcp.Tool
	for i := range res.Tools {
		if res.Tools[i].Name == "list_products" {
			found = &res.Tools[i]
		}
	}
	if found == nil {
		t.Fatal("list_products not advertised in tools/list")
	}
	// JSON Schema for inputs must be present and well-formed.
	if found.InputSchema.Type != "object" {
		t.Errorf("input schema type = %q, want object", found.InputSchema.Type)
	}
	if _, ok := found.InputSchema.Properties["query"]; !ok {
		t.Errorf("input schema missing 'query' property: %+v", found.InputSchema.Properties)
	}
}

func TestCallTool_ListProducts_HappyPath(t *testing.T) {
	api := &fakeAPI{products: []moysklad.Product{
		{
			ID:         "11111111-1111-1111-1111-111111111111",
			Name:       "Кофе зерновой 1кг",
			Code:       "COFFEE-1KG",
			SalePrices: []moysklad.SalePrice{{Value: 129900}},
			BuyPrice:   &moysklad.BuyPrice{Value: 75000},
		},
	}}
	c := newInProcess(t, api)

	req := mcp.CallToolRequest{}
	req.Params.Name = "list_products"
	req.Params.Arguments = map[string]any{"query": "кофе", "limit": 50}

	res, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error result: %+v", res.Content)
	}

	// StructuredContent must be an object, not a bare array — this is exactly
	// what Claude validates ("structured_content: valid dictionary").
	if res.StructuredContent != nil {
		if _, ok := res.StructuredContent.(map[string]any); !ok {
			t.Errorf("StructuredContent is %T, want a JSON object", res.StructuredContent)
		}
	}

	// The default filter must exclude archived, search + limit must pass through.
	if api.gotOpts.Search != "кофе" {
		t.Errorf("Search = %q, want кофе", api.gotOpts.Search)
	}
	if api.gotOpts.Limit != 50 {
		t.Errorf("Limit = %d, want 50", api.gotOpts.Limit)
	}
	if len(api.gotOpts.Filter) != 1 || api.gotOpts.Filter[0] != "archived=false" {
		t.Errorf("Filter = %v, want [archived=false]", api.gotOpts.Filter)
	}

	// The structured payload must be an object (MCP requires a dict, not a bare
	// array) wrapping the rows, and carry rubles, not kopecks.
	text := textContent(t, res)
	var got struct {
		Items []aggregate.ProductSummary `json:"items"`
		Count int                        `json:"count"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal tool result %q: %v", text, err)
	}
	if got.Count != 1 || len(got.Items) != 1 {
		t.Fatalf("got count=%d items=%d, want 1/1", got.Count, len(got.Items))
	}
	if got.Items[0].SalePrice != 1299.00 {
		t.Errorf("SalePrice = %v, want 1299.00 (rubles)", got.Items[0].SalePrice)
	}
	if got.Items[0].BuyPrice != 750.00 {
		t.Errorf("BuyPrice = %v, want 750.00 (rubles)", got.Items[0].BuyPrice)
	}
}

func TestCallTool_IncludeArchived_DropsFilter(t *testing.T) {
	api := &fakeAPI{}
	c := newInProcess(t, api)

	req := mcp.CallToolRequest{}
	req.Params.Name = "list_products"
	req.Params.Arguments = map[string]any{"include_archived": true}
	if _, err := c.CallTool(context.Background(), req); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(api.gotOpts.Filter) != 0 {
		t.Errorf("Filter = %v, want empty when include_archived=true", api.gotOpts.Filter)
	}
}

func textContent(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	t.Fatalf("no text content in result: %+v", res.Content)
	return ""
}
