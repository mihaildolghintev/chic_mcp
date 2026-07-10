package aggregate

import (
	"testing"
	"time"

	"mcp.chic.md/internal/moysklad"
)

func TestABC_Classification(t *testing.T) {
	// Values chosen so cumulative shares fall cleanly into A/B/C.
	// Total = 1000. A cutoff 80% -> 800, B cutoff 95% -> 950.
	items := []float64{700, 150, 100, 30, 20}
	res := ABC(items, func(v float64) float64 { return v }, 0.8, 0.95)

	if len(res) != 5 {
		t.Fatalf("got %d items, want 5", len(res))
	}
	// Sorted descending.
	if res[0].Value != 700 {
		t.Errorf("first value = %v, want 700", res[0].Value)
	}
	// Cumulative shares drive the class: 700 cum=70% <=80% -> A;
	// +150 cum=85% (>80, <=95) -> B; +100 cum=95% -> B; 30, 20 -> C.
	wantClass := []ABCClass{ClassA, ClassB, ClassB, ClassC, ClassC}
	for i, w := range wantClass {
		if res[i].Class != w {
			t.Errorf("item %d (value %v): class = %s, want %s (cum %.1f%%)",
				i, res[i].Value, res[i].Class, w, res[i].Cumshare)
		}
	}
	if res[0].Share != 70 {
		t.Errorf("first share = %v, want 70", res[0].Share)
	}
}

func TestABC_EmptyAndZero(t *testing.T) {
	if got := ABC(nil, func(v float64) float64 { return v }, 0, 0); len(got) != 0 {
		t.Errorf("ABC(nil) len = %d, want 0", len(got))
	}
	res := ABC([]float64{0, 0}, func(v float64) float64 { return v }, 0, 0)
	for _, r := range res {
		if r.Class != ClassC {
			t.Errorf("zero-value item class = %s, want C", r.Class)
		}
	}
}

func cpRow(name, lastDemand string, revenue, avgReceipt, profit, balance float64, count int) moysklad.CounterpartyRow {
	return moysklad.CounterpartyRow{
		Counterparty:   moysklad.NamedRef{Name: name},
		LastDemandDate: lastDemand,
		DemandsCount:   count,
		DemandsSum:     revenue,
		AverageReceipt: avgReceipt,
		Profit:         profit,
		Balance:        balance,
	}
}

func TestSegmentCounterparties(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	rows := []moysklad.CounterpartyRow{
		// VIP (top revenue), recent, profitable.
		cpRow("BigCo", "2026-06-25 10:00:00", 10_000_00, 5_000_00, 3_000_00, 0, 20),
		// Sleeping (last purchase 120 days ago), and a debtor.
		cpRow("SleepyLtd", "2026-03-01 10:00:00", 500_00, 250_00, 100_00, 1_000_00, 5),
		// At risk (60 days ago), low check, negative margin.
		cpRow("EdgeCase", "2026-05-02 10:00:00", 300_00, 50_00, -20_00, 0, 10),
		// Never purchased.
		cpRow("Prospect", "", 0, 0, 0, 0, 0),
	}
	segs := SegmentCounterparties(rows, SegmentParams{
		Now:               now,
		SleepingDays:      90,
		AtRiskDays:        45,
		VIPTopPercent:     0.3,
		LowCheckThreshold: 1_000, // 1000 rubles
	})

	byName := map[string][]string{}
	for _, s := range segs {
		byName[s.Name] = s.Segments
	}

	if !contains(byName["BigCo"], "vip") {
		t.Errorf("BigCo segments = %v, want vip", byName["BigCo"])
	}
	if !contains(byName["SleepyLtd"], "sleeping") || !contains(byName["SleepyLtd"], "debtor") {
		t.Errorf("SleepyLtd segments = %v, want sleeping+debtor", byName["SleepyLtd"])
	}
	if !contains(byName["EdgeCase"], "at_risk") {
		t.Errorf("EdgeCase segments = %v, want at_risk", byName["EdgeCase"])
	}
	if !contains(byName["EdgeCase"], "low_check") {
		t.Errorf("EdgeCase segments = %v, want low_check", byName["EdgeCase"])
	}
	if !contains(byName["EdgeCase"], "negative_margin") {
		t.Errorf("EdgeCase segments = %v, want negative_margin", byName["EdgeCase"])
	}
	// Prospect never purchased -> no recency label.
	if contains(byName["Prospect"], "sleeping") || contains(byName["Prospect"], "at_risk") {
		t.Errorf("Prospect should have no recency segment, got %v", byName["Prospect"])
	}
}

func TestDeadStock(t *testing.T) {
	rows := []moysklad.StockRow{
		{Meta: moysklad.Meta{Href: "p1"}, Name: "Old & unsold", Price: 100_00, Stock: 10, StockDays: 200},
		{Meta: moysklad.Meta{Href: "p2"}, Name: "Old but selling", Price: 50_00, Stock: 5, StockDays: 200},
		{Meta: moysklad.Meta{Href: "p3"}, Name: "Fresh", Price: 100_00, Stock: 3, StockDays: 10},
		{Meta: moysklad.Meta{Href: "p4"}, Name: "Zero stock", Price: 100_00, Stock: 0, StockDays: 300},
	}
	outcome := map[string]float64{"p1": 0, "p2": 7} // p2 moved, p1 did not

	dead := DeadStock(rows, outcome, 90)
	if len(dead) != 1 {
		t.Fatalf("got %d dead items, want 1 (%+v)", len(dead), dead)
	}
	if dead[0].Name != "Old & unsold" {
		t.Errorf("dead item = %q, want 'Old & unsold'", dead[0].Name)
	}
	if dead[0].StockValue != 10_00 { // 10 units * 100.00 rub
		t.Errorf("stock value = %v, want 1000", dead[0].StockValue)
	}

	// Without turnover data, age alone qualifies (p1, p2 both dead; p4 zero stock excluded).
	deadNoTurn := DeadStock(rows, nil, 90)
	if len(deadNoTurn) != 2 {
		t.Errorf("without turnover got %d, want 2", len(deadNoTurn))
	}
}

func TestComparePeriods(t *testing.T) {
	type line struct {
		name string
		val  float64
	}
	a := []line{{"A", 100}, {"B", 200}, {"C", 50}}
	b := []line{{"A", 150}, {"B", 120}, {"D", 80}} // A up 50, B down 80, C gone (-50), D new (+80)

	cmp := ComparePeriods(a, b,
		func(l line) string { return l.name },
		func(l line) float64 { return l.val },
		2)

	if cmp.TotalA != 350 || cmp.TotalB != 350 {
		t.Errorf("totals A=%v B=%v, want 350/350", cmp.TotalA, cmp.TotalB)
	}
	if cmp.Delta != 0 {
		t.Errorf("delta = %v, want 0", cmp.Delta)
	}
	// Top gainers by delta: D(+80), A(+50).
	if len(cmp.Gainers) != 2 || cmp.Gainers[0].Key != "D" || cmp.Gainers[1].Key != "A" {
		t.Errorf("gainers = %+v, want D then A", cmp.Gainers)
	}
	// Top decliners: B(-80), C(-50).
	if len(cmp.Decliners) != 2 || cmp.Decliners[0].Key != "B" || cmp.Decliners[1].Key != "C" {
		t.Errorf("decliners = %+v, want B then C", cmp.Decliners)
	}
}

func TestReceivablesAging(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	docs := []moysklad.Document{
		// Fully paid -> excluded.
		{Name: "INV-1", Sum: 100_00, PayedSum: 100_00, PaymentPlannedMoment: "2026-06-01 00:00:00"},
		// Overdue 30 days, 500 rub outstanding.
		{Name: "INV-2", Sum: 1000_00, PayedSum: 500_00, PaymentPlannedMoment: "2026-06-01 00:00:00", Agent: &moysklad.NamedRef{Name: "LateCo"}},
		// Not yet due -> current bucket.
		{Name: "INV-3", Sum: 300_00, PayedSum: 0, PaymentPlannedMoment: "2026-08-01 00:00:00"},
		// Overdue 100 days -> 90+ bucket.
		{Name: "INV-4", Sum: 200_00, PayedSum: 0, PaymentPlannedMoment: "2026-03-23 00:00:00"},
	}
	ag := ReceivablesAging(docs, now, 0)

	if ag.TotalOutstanding != 500+300+200 {
		t.Errorf("total outstanding = %v, want 1000", ag.TotalOutstanding)
	}
	if ag.TotalOverdue != 500+200 {
		t.Errorf("total overdue = %v, want 700", ag.TotalOverdue)
	}
	// Buckets: current[0], 1-30[1], 31-60[2], 61-90[3], 90+[4].
	if ag.Buckets[0].Amount != 300 { // INV-3 current
		t.Errorf("current bucket = %v, want 300", ag.Buckets[0].Amount)
	}
	if ag.Buckets[1].Amount != 500 { // INV-2 at 30 days
		t.Errorf("1-30 bucket = %v, want 500", ag.Buckets[1].Amount)
	}
	if ag.Buckets[4].Amount != 200 { // INV-4 at 100 days
		t.Errorf("90+ bucket = %v, want 200", ag.Buckets[4].Amount)
	}
	// Sorted most-overdue first.
	if ag.Items[0].Document != "INV-4" {
		t.Errorf("first item = %q, want INV-4 (most overdue)", ag.Items[0].Document)
	}
}

func TestReceivablesAging_TruncatesItemsButNotTotals(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	var docs []moysklad.Document
	for i := 0; i < 5; i++ {
		docs = append(docs, moysklad.Document{Name: "INV", Sum: 100_00, PayedSum: 0, PaymentPlannedMoment: "2026-06-01 00:00:00"})
	}
	ag := ReceivablesAging(docs, now, 2)

	// Totals and item count cover all 5 despite the 2-row detail cap.
	if ag.TotalOutstanding != 500 {
		t.Errorf("outstanding = %v, want 500 (all 5)", ag.TotalOutstanding)
	}
	if ag.ItemCount != 5 || !ag.ItemsTruncated || len(ag.Items) != 2 {
		t.Errorf("itemCount=%d truncated=%v len=%d, want 5/true/2", ag.ItemCount, ag.ItemsTruncated, len(ag.Items))
	}
}

func TestABCReport_TotalsCoverAllDespiteTruncation(t *testing.T) {
	items := ABC([]string{"a", "b", "c"}, func(s string) float64 {
		return map[string]float64{"a": 100, "b": 60, "c": 5}[s]
	}, 0.8, 0.95)
	rep := ABCReport(items, 1)

	if rep.Totals.Count != 3 || rep.Totals.Value != 165 {
		t.Errorf("totals = %+v, want count 3 value 165", rep.Totals)
	}
	if rep.Returned != 1 || !rep.Truncated {
		t.Errorf("returned=%d truncated=%v, want 1/true", rep.Returned, rep.Truncated)
	}
	if rep.Totals.ACount+rep.Totals.BCount+rep.Totals.CCount != 3 {
		t.Errorf("class counts must sum to 3: %+v", rep.Totals)
	}
}

func TestTurnoverDays(t *testing.T) {
	rows := []moysklad.TurnoverRow{
		{
			Assortment:    moysklad.NamedRef{Name: "Widget"},
			OnPeriodStart: moysklad.TurnoverMeasure{Quantity: 100},
			OnPeriodEnd:   moysklad.TurnoverMeasure{Quantity: 100},
			Outcome:       moysklad.TurnoverMeasure{Quantity: 30},
		},
		{
			Assortment: moysklad.NamedRef{Name: "Deadweight"},
			Outcome:    moysklad.TurnoverMeasure{Quantity: 0}, // no sales
		},
	}
	lines := Turnover(rows, 30) // 30-day period
	// avg stock 100, daily out 1/day -> 100 days.
	if lines[0].TurnoverDays != 100 {
		t.Errorf("turnover days = %v, want 100", lines[0].TurnoverDays)
	}
	// No outbound -> 0 (dead-stock signal).
	if lines[1].TurnoverDays != 0 {
		t.Errorf("dead item turnover days = %v, want 0", lines[1].TurnoverDays)
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
