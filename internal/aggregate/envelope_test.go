package aggregate

import (
	"testing"

	"mcp.chic.md/internal/moysklad"
)

// ppRow builds a raw profit-by-product row with kopeck sums.
func ppRow(name string, qty, sellKop, costKop, profitKop float64) moysklad.ProfitByProductRow {
	return moysklad.ProfitByProductRow{
		Assortment:   moysklad.NamedRef{Name: name},
		SellQuantity: qty,
		SellSum:      moysklad.Amount(sellKop),
		SellCostSum:  moysklad.Amount(costKop),
		Profit:       moysklad.Amount(profitKop),
	}
}

// This is the regression test for the bug that motivated the whole change: the
// model kept re-summing a 100-row-truncated detail list and reporting a partial
// revenue. The totals must cover EVERY row, independent of the detail limit.
func TestProfitProductReport_TotalsCoverAllRowsDespiteTruncation(t *testing.T) {
	rows := []moysklad.ProfitByProductRow{
		ppRow("A", 10, 100_00, 60_00, 40_00),
		ppRow("B", 5, 300_00, 200_00, 100_00), // highest revenue -> sorts first
		ppRow("C", 2, 50_00, 30_00, 20_00),
	}

	rep := ProfitProductReport(rows, 1) // display only the single top row

	// Totals are over all three rows: 100+300+50 = 450 revenue, 40+100+20 = 160 profit.
	if rep.Totals.Revenue != 450 {
		t.Errorf("totals revenue = %v, want 450", rep.Totals.Revenue)
	}
	if rep.Totals.Cost != 290 {
		t.Errorf("totals cost = %v, want 290", rep.Totals.Cost)
	}
	if rep.Totals.Profit != 160 {
		t.Errorf("totals profit = %v, want 160", rep.Totals.Profit)
	}
	if rep.Totals.SellQuantity != 17 {
		t.Errorf("totals sellQuantity = %v, want 17", rep.Totals.SellQuantity)
	}
	// margin = 160/450*100 = 35.56.
	if rep.Totals.MarginPct != 35.56 {
		t.Errorf("totals marginPct = %v, want 35.56", rep.Totals.MarginPct)
	}

	// Detail list is truncated but flagged, and sorted by revenue desc.
	if rep.RowCount != 3 || rep.Returned != 1 || !rep.Truncated {
		t.Errorf("rowCount=%d returned=%d truncated=%v, want 3/1/true", rep.RowCount, rep.Returned, rep.Truncated)
	}
	if len(rep.Rows) != 1 || rep.Rows[0].Name != "B" {
		t.Fatalf("top row = %+v, want B (highest revenue)", rep.Rows)
	}
}

func TestProfitProductReport_NoTruncationWhenLimitCoversAll(t *testing.T) {
	rows := []moysklad.ProfitByProductRow{ppRow("A", 1, 100_00, 60_00, 40_00)}
	rep := ProfitProductReport(rows, 100)
	if rep.Truncated || rep.RowCount != 1 || rep.Returned != 1 {
		t.Errorf("small report should not truncate: %+v", rep)
	}
	// Rows must never be a nil slice (clients expect an array).
	if rep.Rows == nil {
		t.Error("Rows is nil, want empty/non-nil slice")
	}
}

func TestProfitEntityReport_Totals(t *testing.T) {
	rows := []moysklad.ProfitByEntityRow{
		{Counterparty: &moysklad.NamedRef{Name: "ACME"}, SellSum: 500_00, SellCostSum: 400_00, Profit: 100_00, SalesCount: 3},
		{Counterparty: &moysklad.NamedRef{Name: "Beta"}, SellSum: 200_00, SellCostSum: 150_00, Profit: 50_00, SalesCount: 2},
	}
	rep := ProfitEntityReport(rows, 100)
	if rep.Totals.Revenue != 700 || rep.Totals.Profit != 150 || rep.Totals.SalesCount != 5 {
		t.Errorf("entity totals wrong: %+v", rep.Totals)
	}
	if rep.Rows[0].Name != "ACME" {
		t.Errorf("first row = %q, want ACME (highest revenue)", rep.Rows[0].Name)
	}
}

func TestDocumentReport_TotalsAndOrder(t *testing.T) {
	docs := []moysklad.Document{
		{Name: "D-3", Moment: "2026-03-01 10:00:00", Sum: 300_00, PayedSum: 300_00},
		{Name: "D-1", Moment: "2026-01-01 10:00:00", Sum: 100_00, PayedSum: 50_00},
	}
	rep := DocumentReport(docs, 1)

	// Totals over both docs regardless of the 1-row detail cap.
	if rep.Totals.Sum != 400 || rep.Totals.Paid != 350 {
		t.Errorf("document totals wrong: %+v", rep.Totals)
	}
	if rep.RowCount != 2 || !rep.Truncated {
		t.Errorf("rowCount=%d truncated=%v, want 2/true", rep.RowCount, rep.Truncated)
	}
	// Order preserved (API sends moment desc); first row stays D-3.
	if rep.Rows[0].Name != "D-3" {
		t.Errorf("first row = %q, want D-3 (order preserved)", rep.Rows[0].Name)
	}
}

func TestStockReport_TotalsTiedUpValue(t *testing.T) {
	rows := []moysklad.StockRow{
		{Name: "Cheap", Price: 10_00, Stock: 2},   // value 20
		{Name: "Pricey", Price: 100_00, Stock: 3}, // value 300 -> sorts first
	}
	rep := StockReport(rows, 100)
	if rep.Totals.StockValue != 320 || rep.Totals.Units != 5 {
		t.Errorf("stock totals wrong: %+v", rep.Totals)
	}
	if rep.Rows[0].Name != "Pricey" {
		t.Errorf("first row = %q, want Pricey (highest value)", rep.Rows[0].Name)
	}
}

func TestCounterpartyReport_Totals(t *testing.T) {
	rows := []moysklad.CounterpartyRow{
		cpRow("BigCo", "", 10_000_00, 0, 3_000_00, 500_00, 20),
		cpRow("SmallCo", "", 1_000_00, 0, 200_00, 0, 5),
	}
	rep := CounterpartyReport(rows, 100)
	if rep.Totals.Revenue != 11_000 || rep.Totals.Profit != 3_200 || rep.Totals.Balance != 500 {
		t.Errorf("counterparty totals wrong: %+v", rep.Totals)
	}
	if rep.Rows[0].Name != "BigCo" {
		t.Errorf("first row = %q, want BigCo (highest revenue)", rep.Rows[0].Name)
	}
}
