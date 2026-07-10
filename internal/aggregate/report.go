package aggregate

import (
	"sort"

	"mcp.chic.md/internal/moysklad"
)

// This file maps raw MoySklad report rows into compact, LLM-friendly DTOs.
// Every monetary field is converted from minor to major units here. Margins are
// recomputed as profit/revenue*100 so the value is unambiguous regardless of
// how MoySklad encodes its own `margin` field.

// ---- Report envelope ------------------------------------------------------

// Report is the uniform envelope every list-style report tool returns. Totals
// are ALWAYS computed over the complete result set (every row MoySklad returned
// for the period); Rows carries only the top Returned rows, sorted by the
// report's primary money metric. Truncated tells the model that Rows is a subset
// — the totals still cover every row, so it must never re-sum Rows to answer a
// "what's the total" question. This is the fix for silent 100-row truncation:
// the model gets the correct grand total regardless of how many detail rows fit.
type Report[Row any, Totals any] struct {
	Totals    Totals `json:"totals"`
	RowCount  int    `json:"rowCount"`  // rows in the full result set
	Returned  int    `json:"returned"`  // rows included in Rows
	Truncated bool   `json:"truncated"` // RowCount > Returned
	Rows      []Row  `json:"rows"`
}

// newReport truncates rows to a display limit (<=0 means "all") and records
// whether truncation occurred. Totals are passed in already computed over the
// full slice, so truncation never affects them.
func newReport[Row any, Totals any](rows []Row, totals Totals, limit int) Report[Row, Totals] {
	full := len(rows)
	truncated := false
	if limit > 0 && limit < full {
		rows = rows[:limit]
		truncated = true
	}
	if rows == nil {
		rows = []Row{}
	}
	return Report[Row, Totals]{
		Totals:    totals,
		RowCount:  full,
		Returned:  len(rows),
		Truncated: truncated,
		Rows:      rows,
	}
}

// ---- Dashboard ------------------------------------------------------------

type DashboardSummary struct {
	Period       string  `json:"period"`
	SalesCount   int     `json:"salesCount"`
	SalesAmount  float64 `json:"salesAmount"`
	SalesDelta   float64 `json:"salesDeltaVsPrev"`
	OrdersCount  int     `json:"ordersCount"`
	OrdersAmount float64 `json:"ordersAmount"`
	MoneyIncome  float64 `json:"moneyIncome"`
	MoneyOutcome float64 `json:"moneyOutcome"`
	MoneyBalance float64 `json:"moneyBalance"`
}

// Dashboard money amounts are assumed to be in minor units (as elsewhere in
// MoySklad); verify against a real account and adjust here if a field turns out
// to be in major units.
func Dashboard(period string, d *moysklad.Dashboard) DashboardSummary {
	return DashboardSummary{
		Period:       period,
		SalesCount:   d.Sales.Count,
		SalesAmount:  MinorToMajor(d.Sales.Amount),
		SalesDelta:   MinorToMajor(d.Sales.MovementAmount),
		OrdersCount:  d.Orders.Count,
		OrdersAmount: MinorToMajor(d.Orders.Amount),
		MoneyIncome:  MinorToMajor(d.Money.Income),
		MoneyOutcome: MinorToMajor(d.Money.Outcome),
		MoneyBalance: MinorToMajor(d.Money.Balance),
	}
}

// ---- Profit ---------------------------------------------------------------

type ProfitProductLine struct {
	Name         string  `json:"name"`
	Code         string  `json:"code,omitempty"`
	SellQuantity float64 `json:"sellQuantity"`
	Revenue      float64 `json:"revenue"`
	Cost         float64 `json:"cost"`
	ReturnSum    float64 `json:"returnSum"`
	Profit       float64 `json:"profit"`
	MarginPct    float64 `json:"marginPct"`
}

func ProfitByProduct(rows []moysklad.ProfitByProductRow) []ProfitProductLine {
	out := make([]ProfitProductLine, 0, len(rows))
	for _, r := range rows {
		revenue := MinorToMajor(r.SellSum)
		profit := MinorToMajor(r.Profit)
		out = append(out, ProfitProductLine{
			Name:         r.Assortment.Name,
			Code:         r.Assortment.Code,
			SellQuantity: r.SellQuantity,
			Revenue:      revenue,
			Cost:         MinorToMajor(r.SellCostSum),
			ReturnSum:    MinorToMajor(r.ReturnSum),
			Profit:       profit,
			MarginPct:    marginPct(profit, revenue),
		})
	}
	return out
}

// ProfitTotals are the period grand totals for a profit report, over every row.
type ProfitTotals struct {
	Revenue      float64 `json:"revenue"`
	Cost         float64 `json:"cost"`
	Profit       float64 `json:"profit"`
	ReturnSum    float64 `json:"returnSum,omitempty"`
	SellQuantity float64 `json:"sellQuantity,omitempty"`
	SalesCount   int     `json:"salesCount,omitempty"`
	MarginPct    float64 `json:"marginPct"`
}

// ProfitProductReport maps rows, sorts by revenue desc, computes complete
// totals, and truncates the detail list to limit. This is what get_profit
// returns so the model never has to sum rows itself.
func ProfitProductReport(rows []moysklad.ProfitByProductRow, limit int) Report[ProfitProductLine, ProfitTotals] {
	lines := ProfitByProduct(rows)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Revenue > lines[j].Revenue })
	var t ProfitTotals
	for _, l := range lines {
		t.Revenue += l.Revenue
		t.Cost += l.Cost
		t.Profit += l.Profit
		t.ReturnSum += l.ReturnSum
		t.SellQuantity += l.SellQuantity
	}
	t.finalize()
	return newReport(lines, t, limit)
}

// ProfitEntityReport is ProfitProductReport for the counterparty/channel/
// employee dimension.
func ProfitEntityReport(rows []moysklad.ProfitByEntityRow, limit int) Report[ProfitEntityLine, ProfitTotals] {
	lines := ProfitByEntity(rows)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Revenue > lines[j].Revenue })
	var t ProfitTotals
	for _, l := range lines {
		t.Revenue += l.Revenue
		t.Cost += l.Cost
		t.Profit += l.Profit
		t.SalesCount += l.SalesCount
	}
	t.finalize()
	return newReport(lines, t, limit)
}

// finalize rounds accumulated sums and derives the blended margin.
func (t *ProfitTotals) finalize() {
	t.Revenue = round2(t.Revenue)
	t.Cost = round2(t.Cost)
	t.Profit = round2(t.Profit)
	t.ReturnSum = round2(t.ReturnSum)
	t.SellQuantity = round2(t.SellQuantity)
	t.MarginPct = marginPct(t.Profit, t.Revenue)
}

type ProfitEntityLine struct {
	Name       string  `json:"name"`
	Revenue    float64 `json:"revenue"`
	Cost       float64 `json:"cost"`
	Profit     float64 `json:"profit"`
	SalesCount int     `json:"salesCount"`
	AvgCheck   float64 `json:"avgCheck"`
	MarginPct  float64 `json:"marginPct"`
}

func ProfitByEntity(rows []moysklad.ProfitByEntityRow) []ProfitEntityLine {
	out := make([]ProfitEntityLine, 0, len(rows))
	for _, r := range rows {
		revenue := MinorToMajor(r.SellSum)
		profit := MinorToMajor(r.Profit)
		out = append(out, ProfitEntityLine{
			Name:       r.Name(),
			Revenue:    revenue,
			Cost:       MinorToMajor(r.SellCostSum),
			Profit:     profit,
			SalesCount: r.SalesCount,
			AvgCheck:   MinorToMajor(r.SalesAvgCheck),
			MarginPct:  marginPct(profit, revenue),
		})
	}
	return out
}

// ---- Turnover -------------------------------------------------------------

type TurnoverLine struct {
	Name       string  `json:"name"`
	StartQty   float64 `json:"startQty"`
	IncomeQty  float64 `json:"incomeQty"`
	OutcomeQty float64 `json:"outcomeQty"`
	EndQty     float64 `json:"endQty"`
	EndValue   float64 `json:"endValue"`
	// TurnoverDays is avg stock divided by average daily outbound quantity over
	// the period; 0 when there was no outbound movement (candidate dead stock).
	TurnoverDays float64 `json:"turnoverDays"`
}

// Turnover converts rows and computes turnover days using the period length in
// days (pass the number of days between momentFrom and momentTo).
func Turnover(rows []moysklad.TurnoverRow, periodDays float64) []TurnoverLine {
	out := make([]TurnoverLine, 0, len(rows))
	for _, r := range rows {
		out = append(out, TurnoverLine{
			Name:         r.Assortment.Name,
			StartQty:     r.OnPeriodStart.Quantity,
			IncomeQty:    r.Income.Quantity,
			OutcomeQty:   r.Outcome.Quantity,
			EndQty:       r.OnPeriodEnd.Quantity,
			EndValue:     MinorToMajor(r.OnPeriodEnd.Sum),
			TurnoverDays: turnoverDays(r, periodDays),
		})
	}
	return out
}

// TurnoverTotals are the period grand totals for a turnover report.
type TurnoverTotals struct {
	IncomeQty  float64 `json:"incomeQty"`
	OutcomeQty float64 `json:"outcomeQty"`
	EndValue   float64 `json:"endValue"`
}

// TurnoverReport maps rows, sorts by closing inventory value desc, totals over
// all rows, and truncates the detail list to limit.
func TurnoverReport(rows []moysklad.TurnoverRow, periodDays float64, limit int) Report[TurnoverLine, TurnoverTotals] {
	lines := Turnover(rows, periodDays)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].EndValue > lines[j].EndValue })
	var t TurnoverTotals
	for _, l := range lines {
		t.IncomeQty += l.IncomeQty
		t.OutcomeQty += l.OutcomeQty
		t.EndValue += l.EndValue
	}
	t.IncomeQty = round2(t.IncomeQty)
	t.OutcomeQty = round2(t.OutcomeQty)
	t.EndValue = round2(t.EndValue)
	return newReport(lines, t, limit)
}

func turnoverDays(r moysklad.TurnoverRow, periodDays float64) float64 {
	out := r.Outcome.Quantity
	if out <= 0 || periodDays <= 0 {
		return 0
	}
	avgStock := (r.OnPeriodStart.Quantity + r.OnPeriodEnd.Quantity) / 2
	dailyOut := out / periodDays
	if dailyOut <= 0 {
		return 0
	}
	return round2(avgStock / dailyOut)
}

// ---- Stock ----------------------------------------------------------------

type StockLine struct {
	Name       string  `json:"name"`
	Code       string  `json:"code,omitempty"`
	Article    string  `json:"article,omitempty"`
	Stock      float64 `json:"stock"`
	Reserve    float64 `json:"reserve"`
	Available  float64 `json:"available"`
	InTransit  float64 `json:"inTransit"`
	CostPrice  float64 `json:"costPrice"`
	SalePrice  float64 `json:"salePrice"`
	StockValue float64 `json:"stockValue"`
	StockDays  int     `json:"stockDays"`
}

func Stock(rows []moysklad.StockRow) []StockLine {
	out := make([]StockLine, 0, len(rows))
	for _, r := range rows {
		cost := MinorToMajor(r.Price)
		out = append(out, StockLine{
			Name:       r.Name,
			Code:       r.Code,
			Article:    r.Article,
			Stock:      r.Stock,
			Reserve:    r.Reserve,
			Available:  r.Stock - r.Reserve,
			InTransit:  r.InTransit,
			CostPrice:  cost,
			SalePrice:  MinorToMajor(r.SalePrice),
			StockValue: round2(r.Stock * cost),
			StockDays:  r.StockDays,
		})
	}
	return out
}

// StockTotals are the warehouse grand totals for a stock report — the direct
// answer to "how much money is tied up in stock".
type StockTotals struct {
	Units      float64 `json:"units"`
	Available  float64 `json:"available"`
	StockValue float64 `json:"stockValue"`
}

// StockReport maps rows, sorts by tied-up value desc, totals over all rows, and
// truncates the detail list to limit.
func StockReport(rows []moysklad.StockRow, limit int) Report[StockLine, StockTotals] {
	lines := Stock(rows)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].StockValue > lines[j].StockValue })
	var t StockTotals
	for _, l := range lines {
		t.Units += l.Stock
		t.Available += l.Available
		t.StockValue += l.StockValue
	}
	t.Units = round2(t.Units)
	t.Available = round2(t.Available)
	t.StockValue = round2(t.StockValue)
	return newReport(lines, t, limit)
}

// ---- Counterparty report --------------------------------------------------

type CounterpartyMetric struct {
	Name         string  `json:"name"`
	FirstDemand  string  `json:"firstDemand,omitempty"`
	LastDemand   string  `json:"lastDemand,omitempty"`
	DemandsCount int     `json:"demandsCount"`
	Revenue      float64 `json:"revenue"`
	AvgReceipt   float64 `json:"avgReceipt"`
	ReturnsSum   float64 `json:"returnsSum"`
	Balance      float64 `json:"balance"`
	Profit       float64 `json:"profit"`
}

func CounterpartyMetrics(rows []moysklad.CounterpartyRow) []CounterpartyMetric {
	out := make([]CounterpartyMetric, 0, len(rows))
	for _, r := range rows {
		out = append(out, CounterpartyMetric{
			Name:         r.Counterparty.Name,
			FirstDemand:  r.FirstDemandDate,
			LastDemand:   r.LastDemandDate,
			DemandsCount: r.DemandsCount,
			Revenue:      MinorToMajor(r.DemandsSum),
			AvgReceipt:   MinorToMajor(r.AverageReceipt),
			ReturnsSum:   MinorToMajor(r.ReturnsSum),
			Balance:      MinorToMajor(r.Balance),
			Profit:       MinorToMajor(r.Profit),
		})
	}
	return out
}

// CounterpartyTotals are the grand totals across all counterparties in a report.
type CounterpartyTotals struct {
	Revenue    float64 `json:"revenue"`
	Profit     float64 `json:"profit"`
	ReturnsSum float64 `json:"returnsSum,omitempty"`
	Balance    float64 `json:"balance"`
	MarginPct  float64 `json:"marginPct"`
}

// CounterpartyReport maps rows, sorts by revenue desc, totals over all rows, and
// truncates the detail list to limit.
func CounterpartyReport(rows []moysklad.CounterpartyRow, limit int) Report[CounterpartyMetric, CounterpartyTotals] {
	lines := CounterpartyMetrics(rows)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Revenue > lines[j].Revenue })
	var t CounterpartyTotals
	for _, l := range lines {
		t.Revenue += l.Revenue
		t.Profit += l.Profit
		t.ReturnsSum += l.ReturnsSum
		t.Balance += l.Balance
	}
	t.Revenue = round2(t.Revenue)
	t.Profit = round2(t.Profit)
	t.ReturnsSum = round2(t.ReturnsSum)
	t.Balance = round2(t.Balance)
	t.MarginPct = marginPct(t.Profit, t.Revenue)
	return newReport(lines, t, limit)
}

// ---- Money ----------------------------------------------------------------

type MoneyFlow struct {
	Income  float64          `json:"income"`
	Outcome float64          `json:"outcome"`
	Net     float64          `json:"net"`
	Series  []MoneyFlowPoint `json:"series"`
}

type MoneyFlowPoint struct {
	Date    string  `json:"date"`
	Income  float64 `json:"income"`
	Outcome float64 `json:"outcome"`
	Balance float64 `json:"balance"`
}

func Money(m *moysklad.MoneySeries) MoneyFlow {
	income := MinorToMajor(m.Credit)
	outcome := MinorToMajor(m.Debit)
	pts := make([]MoneyFlowPoint, 0, len(m.Series))
	for _, p := range m.Series {
		pts = append(pts, MoneyFlowPoint{
			Date:    p.Date,
			Income:  MinorToMajor(p.Credit),
			Outcome: MinorToMajor(p.Debit),
			Balance: MinorToMajor(p.Balance),
		})
	}
	return MoneyFlow{Income: income, Outcome: outcome, Net: round2(income - outcome), Series: pts}
}

// ---- helpers --------------------------------------------------------------

func marginPct(profit, revenue float64) float64 {
	if revenue == 0 {
		return 0
	}
	return round2(profit / revenue * 100)
}
