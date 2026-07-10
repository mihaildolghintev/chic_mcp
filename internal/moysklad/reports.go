package moysklad

import (
	"context"
	"net/url"
	"strconv"
)

// ---- Dashboard ------------------------------------------------------------

// Dashboard is the /report/dashboard/{period} response. Money amounts are in
// the account currency; sales/orders amounts are in kopecks.
type Dashboard struct {
	Sales  DashboardCount `json:"sales"`
	Orders DashboardCount `json:"orders"`
	Money  DashboardMoney `json:"money"`
}

type DashboardCount struct {
	Count          int    `json:"count"`
	Amount         Amount `json:"amount"`
	MovementAmount Amount `json:"movementAmount"`
}

type DashboardMoney struct {
	Income        Amount `json:"income"`
	Outcome       Amount `json:"outcome"`
	Balance       Amount `json:"balance"`
	TodayMovement Amount `json:"todayMovement"`
	Movement      Amount `json:"movement"`
}

// GetDashboard fetches the dashboard for "day", "week" or "month".
func (c *Client) GetDashboard(ctx context.Context, period string) (*Dashboard, error) {
	var d Dashboard
	if err := c.get(ctx, "/report/dashboard/"+period, nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// ---- Profit ---------------------------------------------------------------

// ProfitOptions filters a profit report.
type ProfitOptions struct {
	From   string
	To     string
	Filter []string // raw MoySklad filter expressions
	Limit  int
}

func (o ProfitOptions) values() url.Values {
	v := url.Values{}
	setPeriod(v, o.From, o.To)
	for _, f := range o.Filter {
		v.Add("filter", f)
	}
	return v
}

// ProfitByProductRow is a row of /report/profit/byproduct|byvariant. All *Sum
// and *CostSum fields are in kopecks; margin/salesMargin are percentages.
type ProfitByProductRow struct {
	Assortment     NamedRef `json:"assortment"`
	SellQuantity   float64  `json:"sellQuantity"`
	SellSum        Amount   `json:"sellSum"`
	SellCostSum    Amount   `json:"sellCostSum"`
	ReturnQuantity float64  `json:"returnQuantity"`
	ReturnSum      Amount   `json:"returnSum"`
	ReturnCostSum  Amount   `json:"returnCostSum"`
	Profit         Amount   `json:"profit"`
	Margin         float64  `json:"margin"`
	SalesMargin    float64  `json:"salesMargin"`
}

// ProfitByEntityRow is a row of /report/profit/bycounterparty|byemployee|
// bysaleschannel. Exactly one holder pointer is set depending on the endpoint.
type ProfitByEntityRow struct {
	Counterparty  *NamedRef `json:"counterparty,omitempty"`
	Employee      *NamedRef `json:"employee,omitempty"`
	SalesChannel  *NamedRef `json:"salesChannel,omitempty"`
	SellSum       Amount    `json:"sellSum"`
	SellCostSum   Amount    `json:"sellCostSum"`
	ReturnSum     Amount    `json:"returnSum"`
	ReturnCostSum Amount    `json:"returnCostSum"`
	SalesCount    int       `json:"salesCount"`
	SalesAvgCheck Amount    `json:"salesAvgCheck"`
	ReturnCount   int       `json:"returnCount"`
	Profit        Amount    `json:"profit"`
	Margin        float64   `json:"margin"`
	SalesMargin   float64   `json:"salesMargin"`
}

// Name returns the holder's display name regardless of endpoint.
func (r ProfitByEntityRow) Name() string {
	switch {
	case r.Counterparty != nil:
		return r.Counterparty.Name
	case r.Employee != nil:
		return r.Employee.Name
	case r.SalesChannel != nil:
		return r.SalesChannel.Name
	}
	return ""
}

// ProfitByProduct fetches profit grouped by product ("byproduct") or variant.
func (c *Client) ProfitByProduct(ctx context.Context, variant bool, opts ProfitOptions) ([]ProfitByProductRow, error) {
	path := "/report/profit/byproduct"
	if variant {
		path = "/report/profit/byvariant"
	}
	return getReportRows[ProfitByProductRow](ctx, c, path, opts.values(), opts.Limit)
}

// ProfitByEntity fetches profit grouped by "counterparty", "employee" or
// "saleschannel".
func (c *Client) ProfitByEntity(ctx context.Context, dimension string, opts ProfitOptions) ([]ProfitByEntityRow, error) {
	path := "/report/profit/by" + dimension
	return getReportRows[ProfitByEntityRow](ctx, c, path, opts.values(), opts.Limit)
}

// ---- Turnover -------------------------------------------------------------

// TurnoverMeasure is a quantity+sum pair (sum in kopecks).
type TurnoverMeasure struct {
	Quantity float64 `json:"quantity"`
	Sum      Amount  `json:"sum"`
}

// TurnoverRow is a row of /report/turnover/all.
type TurnoverRow struct {
	Assortment    NamedRef        `json:"assortment"`
	OnPeriodStart TurnoverMeasure `json:"onPeriodStart"`
	Income        TurnoverMeasure `json:"income"`
	Outcome       TurnoverMeasure `json:"outcome"`
	OnPeriodEnd   TurnoverMeasure `json:"onPeriodEnd"`
}

// GetTurnover fetches turnover for all products over a period.
func (c *Client) GetTurnover(ctx context.Context, opts ProfitOptions) ([]TurnoverRow, error) {
	return getReportRows[TurnoverRow](ctx, c, "/report/turnover/all", opts.values(), opts.Limit)
}

// ---- Stock ----------------------------------------------------------------

// StockOptions filters the extended stock report.
type StockOptions struct {
	// StockMode: all|positiveOnly|negativeOnly|empty|nonEmpty|underMinimum.
	StockMode string
	// GroupBy: product|variant|consignment.
	GroupBy string
	// Moment, when set (YYYY-MM-DD), returns the stock slice AS OF that date
	// instead of the current snapshot. Empty means "now".
	Moment string
	// StoreID scopes the report to a single warehouse (store UUID). Empty means
	// all warehouses combined.
	StoreID string
	Filter  []string
	Limit   int
}

func (o StockOptions) values() url.Values {
	v := url.Values{}
	if o.StockMode != "" {
		v.Set("stockMode", o.StockMode)
	}
	if o.GroupBy != "" {
		v.Set("groupBy", o.GroupBy)
	}
	if m := normalizeMoment(o.Moment); m != "" {
		v.Set("moment", m)
	}
	for _, f := range o.Filter {
		v.Add("filter", f)
	}
	return v
}

// StockRow is a row of /report/stock/all. Price (cost) and SalePrice are in
// kopecks. StockDays is the age of the goods on the warehouse.
type StockRow struct {
	Meta      Meta    `json:"meta"`
	Name      string  `json:"name"`
	Code      string  `json:"code"`
	Article   string  `json:"article"`
	Price     Amount  `json:"price"`     // cost price, kopecks
	SalePrice Amount  `json:"salePrice"` // kopecks
	Stock     float64 `json:"stock"`
	Reserve   float64 `json:"reserve"`
	InTransit float64 `json:"inTransit"`
	Quantity  float64 `json:"quantity"`
	StockDays int     `json:"stockDays"`
}

// GetStock fetches the extended stock report. When opts.StoreID is set the
// report is scoped to that single warehouse via a store filter.
func (c *Client) GetStock(ctx context.Context, opts StockOptions) ([]StockRow, error) {
	if opts.StoreID != "" {
		opts.Filter = append(opts.Filter, "store="+c.baseURL+"/entity/store/"+opts.StoreID)
	}
	return getReportRows[StockRow](ctx, c, "/report/stock/all", opts.values(), opts.Limit)
}

// ---- Counterparty report --------------------------------------------------

// CounterpartyRow is a row of /report/counterparty. Sums/balance/profit are in
// kopecks. Dates are RFC "YYYY-MM-DD HH:MM:SS" strings (may be empty).
type CounterpartyRow struct {
	Counterparty    NamedRef `json:"counterparty"`
	FirstDemandDate string   `json:"firstDemandDate"`
	LastDemandDate  string   `json:"lastDemandDate"`
	DemandsCount    int      `json:"demandsCount"`
	DemandsSum      Amount   `json:"demandsSum"`
	AverageReceipt  Amount   `json:"averageReceipt"`
	ReturnsCount    int      `json:"returnsCount"`
	ReturnsSum      Amount   `json:"returnsSum"`
	DiscountsSum    Amount   `json:"discountsSum"`
	Balance         Amount   `json:"balance"`
	Profit          Amount   `json:"profit"`
	LastEventDate   string   `json:"lastEventDate"`
}

// GetCounterpartyReport fetches per-counterparty aggregate metrics.
func (c *Client) GetCounterpartyReport(ctx context.Context, filter []string, limit int) ([]CounterpartyRow, error) {
	v := url.Values{}
	for _, f := range filter {
		v.Add("filter", f)
	}
	return getReportRows[CounterpartyRow](ctx, c, "/report/counterparty", v, limit)
}

// ---- Money ----------------------------------------------------------------

// MoneySeries is the /report/money/plotseries response. credit is income,
// debit is outcome (both in kopecks).
type MoneySeries struct {
	Credit Amount             `json:"credit"`
	Debit  Amount             `json:"debit"`
	Series []MoneySeriesPoint `json:"series"`
}

type MoneySeriesPoint struct {
	Date    string `json:"date"`
	Credit  Amount `json:"credit"`
	Debit   Amount `json:"debit"`
	Balance Amount `json:"balance"`
}

// GetMoneySeries fetches the cash-flow series over a period.
// interval is "hour", "day" or "month".
func (c *Client) GetMoneySeries(ctx context.Context, from, to, interval string) (*MoneySeries, error) {
	v := url.Values{}
	setPeriod(v, from, to)
	if interval != "" {
		v.Set("interval", interval)
	}
	var m MoneySeries
	if err := c.get(ctx, "/report/money/plotseries", v, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ---- shared report pagination --------------------------------------------

// getReportRows fetches a report collection with offset/limit pagination.
// Report endpoints share the ListResponse envelope with entity endpoints.
func getReportRows[T any](ctx context.Context, c *Client, path string, base url.Values, limit int) ([]T, error) {
	pageSize := c.pageLimit
	if pageSize <= 0 {
		pageSize = defaultPageLimit
	}
	if limit > 0 && limit < pageSize {
		pageSize = limit
	}

	var all []T
	offset := 0
	for {
		q := cloneValues(base)
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("offset", strconv.Itoa(offset))

		var page ListResponse[T]
		if err := c.get(ctx, path, q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Rows...)

		if len(page.Rows) < pageSize {
			break
		}
		if limit > 0 && len(all) >= limit {
			all = all[:limit]
			break
		}
		offset += pageSize
		if page.Meta.Size > 0 && offset >= page.Meta.Size {
			break
		}
	}
	return all, nil
}
