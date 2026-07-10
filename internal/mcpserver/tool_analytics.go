package mcpserver

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"mcp.chic.md/internal/aggregate"
	"mcp.chic.md/internal/moysklad"
)

func init() {
	register(registerComparePeriods)
	register(registerABCAnalysis)
	register(registerSegmentCounterparties)
	register(registerDeadStock)
	register(registerReceivablesAging)
}

// ---- compare_periods ------------------------------------------------------

func registerComparePeriods(s *server.MCPServer, api MoyskladAPI) {
	tool := newTool("compare_periods",
		mcp.WithDescription(
			"Compare two periods on revenue or profit, broken down by product or "+
				"customer, and surface the biggest contributors to the change. Answers "+
				"'why did revenue grow/fall between period A and period B' by ranking top "+
				"gainers and decliners. Amounts are in the account's base currency.",
		),
		mcp.WithString("dimension", mcp.Description("product or counterparty. Default product."), mcp.Enum("product", "counterparty"), mcp.DefaultString("product")),
		mcp.WithString("metric", mcp.Description("revenue or profit. Default revenue."), mcp.Enum("revenue", "profit"), mcp.DefaultString("revenue")),
		mcp.WithString("period_a_from", mcp.Required(), mcp.Description("Baseline period start, YYYY-MM-DD.")),
		mcp.WithString("period_a_to", mcp.Required(), mcp.Description("Baseline period end, YYYY-MM-DD.")),
		mcp.WithString("period_b_from", mcp.Required(), mcp.Description("Comparison period start, YYYY-MM-DD.")),
		mcp.WithString("period_b_to", mcp.Required(), mcp.Description("Comparison period end, YYYY-MM-DD.")),
		mcp.WithNumber("top_n", mcp.Description("How many top gainers/decliners to return. Default 10.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		dimension := req.GetString("dimension", "product")
		metric := req.GetString("metric", "revenue")
		topN := req.GetInt("top_n", 10)
		if topN <= 0 {
			topN = 10
		}
		// Limit 0 = fetch every row so the compared totals cover the whole period,
		// not just the first 1000 products/customers.
		optsA := moysklad.ProfitOptions{From: req.GetString("period_a_from", ""), To: req.GetString("period_a_to", "")}
		optsB := moysklad.ProfitOptions{From: req.GetString("period_b_from", ""), To: req.GetString("period_b_to", "")}

		switch dimension {
		case "product", "":
			a, err := api.ProfitByProduct(ctx, false, optsA)
			if err != nil {
				return resultOrError[any](nil, err)
			}
			b, err := api.ProfitByProduct(ctx, false, optsB)
			if err != nil {
				return resultOrError[any](nil, err)
			}
			la, lb := aggregate.ProfitByProduct(a), aggregate.ProfitByProduct(b)
			val := productMetric(metric)
			cmp := aggregate.ComparePeriods(la, lb,
				func(l aggregate.ProfitProductLine) string { return l.Name }, val, topN)
			return resultOrError(cmp, nil)
		case "counterparty":
			a, err := api.ProfitByEntity(ctx, "counterparty", optsA)
			if err != nil {
				return resultOrError[any](nil, err)
			}
			b, err := api.ProfitByEntity(ctx, "counterparty", optsB)
			if err != nil {
				return resultOrError[any](nil, err)
			}
			la, lb := aggregate.ProfitByEntity(a), aggregate.ProfitByEntity(b)
			val := entityMetric(metric)
			cmp := aggregate.ComparePeriods(la, lb,
				func(l aggregate.ProfitEntityLine) string { return l.Name }, val, topN)
			return resultOrError(cmp, nil)
		default:
			return mcp.NewToolResultError("dimension must be product or counterparty"), nil
		}
	})
}

func productMetric(metric string) func(aggregate.ProfitProductLine) float64 {
	if metric == "profit" {
		return func(l aggregate.ProfitProductLine) float64 { return l.Profit }
	}
	return func(l aggregate.ProfitProductLine) float64 { return l.Revenue }
}

func entityMetric(metric string) func(aggregate.ProfitEntityLine) float64 {
	if metric == "profit" {
		return func(l aggregate.ProfitEntityLine) float64 { return l.Profit }
	}
	return func(l aggregate.ProfitEntityLine) float64 { return l.Revenue }
}

// ---- abc_analysis ---------------------------------------------------------

func registerABCAnalysis(s *server.MCPServer, api MoyskladAPI) {
	tool := newTool("abc_analysis",
		mcp.WithDescription(
			"ABC (Pareto) analysis of products or customers by revenue or profit over "+
				"a period. Classes: A = top ~80% of value, B = next ~15%, C = long tail. "+
				"Use to find the vital few products/customers that drive the business. "+
				"Amounts are in the account's base currency.",
		),
		mcp.WithString("dimension", mcp.Description("product or counterparty. Default product."), mcp.Enum("product", "counterparty"), mcp.DefaultString("product")),
		mcp.WithString("metric", mcp.Description("revenue or profit. Default revenue."), mcp.Enum("revenue", "profit"), mcp.DefaultString("revenue")),
		mcp.WithString("date_from", mcp.Description("Period start, YYYY-MM-DD. Optional.")),
		mcp.WithString("date_to", mcp.Description("Period end, YYYY-MM-DD. Optional.")),
		mcp.WithNumber("limit", mcp.Description("Max detail rows to return (top by value). Does NOT affect class totals. Default 100, max 1000.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		from, to := dateArgs(req)
		// Limit 0 = every row, so the Pareto classes are computed over the full
		// catalogue rather than a truncated first 1000; `display` only caps the
		// returned detail (the C-class long tail is not worth enumerating).
		opts := moysklad.ProfitOptions{From: from, To: to}
		display := clampLimit(req.GetInt("limit", 100))
		metric := req.GetString("metric", "revenue")

		switch req.GetString("dimension", "product") {
		case "product", "":
			rows, err := api.ProfitByProduct(ctx, false, opts)
			if err != nil {
				return resultOrError[any](nil, err)
			}
			lines := aggregate.ProfitByProduct(rows)
			return resultOrError(aggregate.ABCReport(aggregate.ABC(lines, productMetric(metric), 0.8, 0.95), display), nil)
		case "counterparty":
			rows, err := api.ProfitByEntity(ctx, "counterparty", opts)
			if err != nil {
				return resultOrError[any](nil, err)
			}
			lines := aggregate.ProfitByEntity(rows)
			return resultOrError(aggregate.ABCReport(aggregate.ABC(lines, entityMetric(metric), 0.8, 0.95), display), nil)
		default:
			return mcp.NewToolResultError("dimension must be product or counterparty"), nil
		}
	})
}

// ---- segment_counterparties -----------------------------------------------

func registerSegmentCounterparties(s *server.MCPServer, api MoyskladAPI) {
	tool := newTool("segment_counterparties",
		mcp.WithDescription(
			"Rule-based customer segmentation from purchase history. Labels each "+
				"customer with any of: vip (top revenue), sleeping (no purchase for a long "+
				"time), at_risk (purchase gap growing), low_check (small average receipt), "+
				"debtor (owes money), negative_margin (unprofitable). Heuristic, not a "+
				"predictive model. Amounts are in the account's base currency.",
		),
		mcp.WithNumber("sleeping_days", mcp.Description("No purchase for more than this many days -> sleeping. Default 90.")),
		mcp.WithNumber("at_risk_days", mcp.Description("Purchase gap over this (but below sleeping) -> at_risk. Default 45.")),
		mcp.WithNumber("vip_top_percent", mcp.Description("Top share by revenue tagged vip, 0-1. Default 0.2.")),
		mcp.WithNumber("low_check_threshold", mcp.Description("Average receipt below this (in the account's base currency) -> low_check. Default off.")),
		mcp.WithNumber("limit", mcp.Description("Max detail rows to return (top by revenue). Does NOT affect label totals. Default 200, max 1000.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Limit 0 = scan every counterparty so segmentation is complete; `display`
		// only caps the returned detail.
		rows, err := api.GetCounterpartyReport(ctx, nil, 0)
		if err != nil {
			return resultOrError[any](nil, err)
		}
		params := aggregate.SegmentParams{
			Now:               time.Now(),
			SleepingDays:      req.GetInt("sleeping_days", 0),
			AtRiskDays:        req.GetInt("at_risk_days", 0),
			VIPTopPercent:     req.GetFloat("vip_top_percent", 0),
			LowCheckThreshold: req.GetFloat("low_check_threshold", 0),
		}
		display := clampLimit(req.GetInt("limit", 200))
		return resultOrError(aggregate.SegmentReport(aggregate.SegmentCounterparties(rows, params), display), nil)
	})
}

// ---- dead_stock -----------------------------------------------------------

func registerDeadStock(s *server.MCPServer, api MoyskladAPI) {
	tool := newTool("dead_stock",
		mcp.WithDescription(
			"Find dead/slow stock: items on hand for at least threshold_days with no "+
				"outbound movement in the period. Sorted by tied-up value. Use for "+
				"'what isn't selling', 'what's gathering dust', 'how much money is frozen'.",
		),
		mcp.WithNumber("threshold_days", mcp.Description("Minimum age on warehouse in days. Default 90.")),
		mcp.WithString("date_from", mcp.Description("Movement window start, YYYY-MM-DD. Optional (enables no-movement check).")),
		mcp.WithString("date_to", mcp.Description("Movement window end, YYYY-MM-DD. Optional.")),
		mcp.WithNumber("limit", mcp.Description("Max detail rows to return (top by tied-up value). Does NOT affect totals. Default 100, max 1000.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		threshold := req.GetInt("threshold_days", 90)
		if threshold < 0 {
			threshold = 90
		}
		display := clampLimit(req.GetInt("limit", 100))
		stock, err := api.GetStock(ctx, moysklad.StockOptions{StockMode: "positiveOnly", GroupBy: "product"})
		if err != nil {
			return resultOrError[any](nil, err)
		}

		var outcomeByRef map[string]float64
		from, to := dateArgs(req)
		if from != "" || to != "" {
			turns, err := api.GetTurnover(ctx, moysklad.ProfitOptions{From: from, To: to})
			if err != nil {
				return resultOrError[any](nil, err)
			}
			outcomeByRef = make(map[string]float64, len(turns))
			for _, t := range turns {
				outcomeByRef[t.Assortment.Meta.Href] = t.Outcome.Quantity
			}
		}
		return resultOrError(aggregate.DeadStockReport(aggregate.DeadStock(stock, outcomeByRef, threshold), display), nil)
	})
}

// ---- receivables_aging ----------------------------------------------------

func registerReceivablesAging(s *server.MCPServer, api MoyskladAPI) {
	tool := newTool("receivables_aging",
		mcp.WithDescription(
			"Accounts-receivable aging from customer invoices: total outstanding, "+
				"total overdue, and buckets (current, 1-30, 31-60, 61-90, 90+ days overdue) "+
				"plus per-invoice detail sorted by days overdue. Use for 'who owes us and "+
				"how late'. Amounts are in the account's base currency.",
		),
		mcp.WithNumber("limit", mcp.Description("Max per-invoice detail rows (top by days overdue). Does NOT affect totals/buckets. Default 200, max 1000.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Limit 0 on the fetch = every open invoice, so the aging buckets and
		// totals are complete; the tool `limit` only caps the emitted detail.
		docs, err := api.SearchDocuments(ctx, moysklad.DocInvoiceOut, moysklad.DocumentQuery{
			Expand: []string{"agent"},
			Order:  "moment,desc",
		})
		if err != nil {
			return resultOrError[any](nil, err)
		}
		display := clampLimit(req.GetInt("limit", 200))
		return resultOrError(aggregate.ReceivablesAging(docs, time.Now(), display), nil)
	})
}
