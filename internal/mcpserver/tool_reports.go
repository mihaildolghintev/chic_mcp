package mcpserver

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"mcp.chic.md/internal/aggregate"
	"mcp.chic.md/internal/moysklad"
)

func init() {
	register(registerGetDashboard)
	register(registerGetProfit)
	register(registerGetTurnover)
	register(registerGetStock)
	register(registerGetCounterpartyMetrics)
	register(registerGetMoney)
}

// ---- get_dashboard --------------------------------------------------------

func registerGetDashboard(s *server.MCPServer, api MoyskladAPI) {
	tool := mcp.NewTool("get_dashboard",
		mcp.WithDescription(
			"Quick business summary for a fixed window: sales count and revenue, "+
				"orders, and money in/out/balance, with the change versus the previous "+
				"comparable period. Use for 'how are we doing today/this week/this month'. "+
				"Amounts are in the account's base currency.",
		),
		mcp.WithString("period",
			mcp.Description("One of: day, week, month. Defaults to month."),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		period := req.GetString("period", "month")
		switch period {
		case "day", "week", "month":
		default:
			period = "month"
		}
		d, err := api.GetDashboard(ctx, period)
		if err != nil {
			return resultOrError[any](nil, err)
		}
		return resultOrError(aggregate.Dashboard(period, d), nil)
	})
}

// ---- get_profit -----------------------------------------------------------

func registerGetProfit(s *server.MCPServer, api MoyskladAPI) {
	tool := mcp.NewTool("get_profit",
		mcp.WithDescription(
			"Profitability report over a period, grouped by a chosen dimension. "+
				"Answers revenue, cost, profit and margin questions and breaks them down "+
				"by product, variant, customer, sales channel or employee. Margin is "+
				"computed as profit/revenue. Amounts are in the account's base currency. "+
				"Without dates, MoySklad "+
				"defaults to the previous month.",
		),
		mcp.WithString("group_by",
			mcp.Description("One of: product, variant, counterparty, saleschannel, employee. Defaults to product."),
		),
		mcp.WithString("date_from", mcp.Description("Period start, YYYY-MM-DD. Optional.")),
		mcp.WithString("date_to", mcp.Description("Period end, YYYY-MM-DD. Optional.")),
		mcp.WithNumber("limit", mcp.Description("Max rows. Default 100, max 1000.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		from, to := dateArgs(req)
		opts := moysklad.ProfitOptions{From: from, To: to, Limit: clampLimit(req.GetInt("limit", 100))}
		groupBy := req.GetString("group_by", "product")

		switch groupBy {
		case "product", "variant":
			rows, err := api.ProfitByProduct(ctx, groupBy == "variant", opts)
			if err != nil {
				return resultOrError[any](nil, err)
			}
			return resultOrError(aggregate.ProfitByProduct(rows), nil)
		case "counterparty", "saleschannel", "employee":
			rows, err := api.ProfitByEntity(ctx, groupBy, opts)
			if err != nil {
				return resultOrError[any](nil, err)
			}
			return resultOrError(aggregate.ProfitByEntity(rows), nil)
		default:
			return mcp.NewToolResultError("group_by must be one of: product, variant, counterparty, saleschannel, employee"), nil
		}
	})
}

// ---- get_turnover ---------------------------------------------------------

func registerGetTurnover(s *server.MCPServer, api MoyskladAPI) {
	tool := mcp.NewTool("get_turnover",
		mcp.WithDescription(
			"Inventory turnover over a period per product: opening stock, goods in, "+
				"goods out, closing stock, plus computed turnover days (avg stock / avg "+
				"daily sales). Low turnover or zero outbound flags slow/dead stock. "+
				"Amounts are in the account's base currency.",
		),
		mcp.WithString("date_from", mcp.Description("Period start, YYYY-MM-DD. Optional.")),
		mcp.WithString("date_to", mcp.Description("Period end, YYYY-MM-DD. Optional.")),
		mcp.WithNumber("limit", mcp.Description("Max rows. Default 200, max 1000.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		from, to := dateArgs(req)
		rows, err := api.GetTurnover(ctx, moysklad.ProfitOptions{From: from, To: to, Limit: clampLimit(req.GetInt("limit", 200))})
		if err != nil {
			return resultOrError[any](nil, err)
		}
		return resultOrError(aggregate.Turnover(rows, periodDays(from, to, 30)), nil)
	})
}

// ---- get_stock ------------------------------------------------------------

func registerGetStock(s *server.MCPServer, api MoyskladAPI) {
	tool := mcp.NewTool("get_stock",
		mcp.WithDescription(
			"Current warehouse stock: on-hand, reserved, available, in-transit, cost "+
				"and sale price, stock value, and age in days (stockDays). Use for 'what's "+
				"in stock', 'what's below minimum', 'how much money is tied up'. Amounts "+
				"are in the account's base currency.",
		),
		mcp.WithString("stock_mode",
			mcp.Description("One of: nonEmpty, all, positiveOnly, negativeOnly, underMinimum, empty. Defaults to nonEmpty."),
		),
		mcp.WithNumber("limit", mcp.Description("Max rows. Default 200, max 1000.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mode := req.GetString("stock_mode", "nonEmpty")
		switch mode {
		case "nonEmpty", "all", "positiveOnly", "negativeOnly", "underMinimum", "empty":
		default:
			mode = "nonEmpty"
		}
		rows, err := api.GetStock(ctx, moysklad.StockOptions{
			StockMode: mode,
			GroupBy:   "product",
			Limit:     clampLimit(req.GetInt("limit", 200)),
		})
		if err != nil {
			return resultOrError[any](nil, err)
		}
		return resultOrError(aggregate.Stock(rows), nil)
	})
}

// ---- get_counterparty_metrics --------------------------------------------

func registerGetCounterpartyMetrics(s *server.MCPServer, api MoyskladAPI) {
	tool := mcp.NewTool("get_counterparty_metrics",
		mcp.WithDescription(
			"Per-customer aggregate metrics: first/last purchase date, number of "+
				"sales, revenue, average receipt, returns, current balance (debt) and "+
				"profit. The basis for customer analysis and segmentation. Amounts are in "+
				"the account's base currency.",
		),
		mcp.WithBoolean("only_debtors", mcp.Description("Return only counterparties with a positive balance (owe money). Default false.")),
		mcp.WithNumber("limit", mcp.Description("Max rows. Default 200, max 1000.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var filter []string
		if req.GetBool("only_debtors", false) {
			filter = append(filter, "balance>0")
		}
		rows, err := api.GetCounterpartyReport(ctx, filter, clampLimit(req.GetInt("limit", 200)))
		if err != nil {
			return resultOrError[any](nil, err)
		}
		return resultOrError(aggregate.CounterpartyMetrics(rows), nil)
	})
}

// ---- get_money ------------------------------------------------------------

func registerGetMoney(s *server.MCPServer, api MoyskladAPI) {
	tool := mcp.NewTool("get_money",
		mcp.WithDescription(
			"Cash flow over a period: total money in, money out, net, and a time "+
				"series at the chosen interval. Use for 'how much came in and went out'. "+
				"Amounts are in the account's base currency.",
		),
		mcp.WithString("date_from", mcp.Description("Period start, YYYY-MM-DD. Optional.")),
		mcp.WithString("date_to", mcp.Description("Period end, YYYY-MM-DD. Optional.")),
		mcp.WithString("interval", mcp.Description("One of: hour, day, month. Defaults to day.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		from, to := dateArgs(req)
		interval := req.GetString("interval", "day")
		switch interval {
		case "hour", "day", "month":
		default:
			interval = "day"
		}
		m, err := api.GetMoneySeries(ctx, from, to, interval)
		if err != nil {
			return resultOrError[any](nil, err)
		}
		return resultOrError(aggregate.Money(m), nil)
	})
}
