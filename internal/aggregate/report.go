package aggregate

import "mcp.chic.md/internal/moysklad"

// This file maps raw MoySklad report rows into compact, LLM-friendly DTOs.
// Every monetary field is converted from kopecks to rubles here. Margins are
// recomputed as profit/revenue*100 so the value is unambiguous regardless of
// how MoySklad encodes its own `margin` field.

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

// Dashboard money amounts are assumed to be kopecks (as elsewhere in MoySklad);
// verify against a real account and adjust here if a field turns out to be in
// major units.
func Dashboard(period string, d *moysklad.Dashboard) DashboardSummary {
	return DashboardSummary{
		Period:       period,
		SalesCount:   d.Sales.Count,
		SalesAmount:  KopecksToRubles(d.Sales.Amount),
		SalesDelta:   KopecksToRubles(d.Sales.MovementAmount),
		OrdersCount:  d.Orders.Count,
		OrdersAmount: KopecksToRubles(d.Orders.Amount),
		MoneyIncome:  KopecksToRubles(d.Money.Income),
		MoneyOutcome: KopecksToRubles(d.Money.Outcome),
		MoneyBalance: KopecksToRubles(d.Money.Balance),
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
		revenue := KopecksToRubles(r.SellSum)
		profit := KopecksToRubles(r.Profit)
		out = append(out, ProfitProductLine{
			Name:         r.Assortment.Name,
			Code:         r.Assortment.Code,
			SellQuantity: r.SellQuantity,
			Revenue:      revenue,
			Cost:         KopecksToRubles(r.SellCostSum),
			ReturnSum:    KopecksToRubles(r.ReturnSum),
			Profit:       profit,
			MarginPct:    marginPct(profit, revenue),
		})
	}
	return out
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
		revenue := KopecksToRubles(r.SellSum)
		profit := KopecksToRubles(r.Profit)
		out = append(out, ProfitEntityLine{
			Name:       r.Name(),
			Revenue:    revenue,
			Cost:       KopecksToRubles(r.SellCostSum),
			Profit:     profit,
			SalesCount: r.SalesCount,
			AvgCheck:   KopecksToRubles(r.SalesAvgCheck),
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
			EndValue:     KopecksToRubles(r.OnPeriodEnd.Sum),
			TurnoverDays: turnoverDays(r, periodDays),
		})
	}
	return out
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
		cost := KopecksToRubles(r.Price)
		out = append(out, StockLine{
			Name:       r.Name,
			Code:       r.Code,
			Article:    r.Article,
			Stock:      r.Stock,
			Reserve:    r.Reserve,
			Available:  r.Stock - r.Reserve,
			InTransit:  r.InTransit,
			CostPrice:  cost,
			SalePrice:  KopecksToRubles(r.SalePrice),
			StockValue: round2(r.Stock * cost),
			StockDays:  r.StockDays,
		})
	}
	return out
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
			Revenue:      KopecksToRubles(r.DemandsSum),
			AvgReceipt:   KopecksToRubles(r.AverageReceipt),
			ReturnsSum:   KopecksToRubles(r.ReturnsSum),
			Balance:      KopecksToRubles(r.Balance),
			Profit:       KopecksToRubles(r.Profit),
		})
	}
	return out
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
	income := KopecksToRubles(m.Credit)
	outcome := KopecksToRubles(m.Debit)
	pts := make([]MoneyFlowPoint, 0, len(m.Series))
	for _, p := range m.Series {
		pts = append(pts, MoneyFlowPoint{
			Date:    p.Date,
			Income:  KopecksToRubles(p.Credit),
			Outcome: KopecksToRubles(p.Debit),
			Balance: KopecksToRubles(p.Balance),
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
