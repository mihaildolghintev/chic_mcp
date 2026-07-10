package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"mcp.chic.md/internal/aggregate"
	"mcp.chic.md/internal/moysklad"
)

func callJSON(t *testing.T, api MoyskladAPI, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	c := newInProcess(t, api)
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		return res
	}
	if out != nil {
		if err := json.Unmarshal([]byte(textContent(t, res)), out); err != nil {
			t.Fatalf("unmarshal %s result: %v", name, err)
		}
	}
	return res
}

func TestGetDashboard_ConvertsMinorUnits(t *testing.T) {
	api := &fakeAPI{dashboard: &moysklad.Dashboard{
		Sales: moysklad.DashboardCount{Count: 12, Amount: 1_500_00},
		Money: moysklad.DashboardMoney{Income: 2_000_00, Outcome: 800_00, Balance: 1_200_00},
	}}
	var got aggregate.DashboardSummary
	callJSON(t, api, "get_dashboard", map[string]any{"period": "week"}, &got)

	if got.Period != "week" {
		t.Errorf("period = %q, want week", got.Period)
	}
	if got.SalesAmount != 1500 || got.MoneyIncome != 2000 || got.MoneyBalance != 1200 {
		t.Errorf("minor-unit conversion wrong: %+v", got)
	}
}

func TestGetAccountCurrency(t *testing.T) {
	api := &fakeAPI{currency: &moysklad.Currency{ISOCode: "MDL", Name: "лей", Code: "498", Default: true}}
	var got currencyOut
	callJSON(t, api, "get_account_currency", nil, &got)

	if got.ISOCode != "MDL" || got.Name != "лей" || got.Code != "498" {
		t.Errorf("currency payload wrong: %+v", got)
	}
}

func TestGetProfit_RoutesByGroupBy(t *testing.T) {
	api := &fakeAPI{profitEntity: []moysklad.ProfitByEntityRow{
		{Counterparty: &moysklad.NamedRef{Name: "ACME"}, SellSum: 500_00, Profit: 100_00, SalesCount: 3},
	}}
	var got aggregate.Report[aggregate.ProfitEntityLine, aggregate.ProfitTotals]
	callJSON(t, api, "get_profit", map[string]any{"group_by": "counterparty"}, &got)

	if api.gotProfitDimension != "counterparty" {
		t.Errorf("dimension passed to client = %q, want counterparty", api.gotProfitDimension)
	}
	if len(got.Rows) != 1 || got.Rows[0].Name != "ACME" || got.Rows[0].Revenue != 500 {
		t.Fatalf("entity line wrong: %+v", got.Rows)
	}
	// margin = profit/revenue*100 = 100/500 = 20%.
	if got.Rows[0].MarginPct != 20 {
		t.Errorf("marginPct = %v, want 20", got.Rows[0].MarginPct)
	}
	// Totals must be present and match the single row.
	if got.Totals.Revenue != 500 || got.Totals.Profit != 100 || got.Totals.MarginPct != 20 {
		t.Errorf("totals wrong: %+v", got.Totals)
	}
	if got.RowCount != 1 || got.Truncated {
		t.Errorf("rowCount=%d truncated=%v, want 1/false", got.RowCount, got.Truncated)
	}
}

func TestGetProfit_BadGroupByErrors(t *testing.T) {
	res := callJSON(t, &fakeAPI{}, "get_profit", map[string]any{"group_by": "nonsense"}, nil)
	if !res.IsError {
		t.Error("expected error result for invalid group_by")
	}
}

func TestSearchDocuments_ValidatesType(t *testing.T) {
	res := callJSON(t, &fakeAPI{}, "search_documents", map[string]any{"type": "notathing"}, nil)
	if !res.IsError {
		t.Error("expected error result for invalid document type")
	}
}

func TestReceivablesAging_UsesInvoiceOut(t *testing.T) {
	api := &fakeAPI{documents: []moysklad.Document{
		{Name: "INV-1", Sum: 1000_00, PayedSum: 400_00, PaymentPlannedMoment: "2020-01-01 00:00:00", Agent: &moysklad.NamedRef{Name: "LateCo"}},
	}}
	var got aggregate.Aging
	callJSON(t, api, "receivables_aging", map[string]any{}, &got)

	if api.gotDocType != moysklad.DocInvoiceOut {
		t.Errorf("document type = %q, want invoiceout", api.gotDocType)
	}
	if got.TotalOutstanding != 600 {
		t.Errorf("outstanding = %v, want 600", got.TotalOutstanding)
	}
	if got.TotalOverdue != 600 {
		t.Errorf("overdue = %v, want 600 (due date far in the past)", got.TotalOverdue)
	}
}

func TestSegmentCounterparties_EndToEnd(t *testing.T) {
	api := &fakeAPI{counterpartyRows: []moysklad.CounterpartyRow{
		{Counterparty: moysklad.NamedRef{Name: "Debtor"}, Balance: 5_000_00, DemandsSum: 1_000_00},
	}}
	var got struct {
		Items []aggregate.CounterpartySegment `json:"items"`
	}
	callJSON(t, api, "segment_counterparties", map[string]any{}, &got)

	if len(got.Items) != 1 {
		t.Fatalf("got %d segments, want 1", len(got.Items))
	}
	if !contains(got.Items[0].Segments, "debtor") {
		t.Errorf("segments = %v, want debtor", got.Items[0].Segments)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestAsObject guards the fix for MCP's requirement that structuredContent be an
// object: a top-level array must be wrapped, structs must pass through.
func TestAsObject(t *testing.T) {
	// Slice -> {items, count}.
	wrapped := asObject([]int{1, 2, 3})
	m, ok := wrapped.(map[string]any)
	if !ok {
		t.Fatalf("slice not wrapped in a map: %T", wrapped)
	}
	if m["count"] != 3 {
		t.Errorf("count = %v, want 3", m["count"])
	}

	// Struct passes through unchanged.
	type s struct{ A int }
	if got := asObject(s{A: 1}); got != (s{A: 1}) {
		t.Errorf("struct changed: %#v", got)
	}

	// Pointer to slice is also wrapped (deref then detect).
	xs := []string{"a"}
	if _, ok := asObject(&xs).(map[string]any); !ok {
		t.Error("pointer-to-slice not wrapped")
	}

	// Nil pointer passes through without panic.
	var np *s
	_ = asObject(np)
}
