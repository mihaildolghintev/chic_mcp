package mcpserver

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"mcp.chic.md/internal/aggregate"
	"mcp.chic.md/internal/moysklad"
)

func init() {
	register(registerSearchDocuments)
	register(registerGetDocument)
	register(registerSearchCounterparty)
}

const docTypesHelp = "One of: demand (sale), customerorder, supply (purchase), " +
	"purchaseorder, invoiceout (customer invoice), invoicein, salesreturn, " +
	"purchasereturn, paymentin, paymentout, move (warehouse transfer), " +
	"inventory (stock count), loss (write-off), enter (stock-in), processing."

// docTypeEnum is the JSON-schema enum, derived from the client's single source
// of truth so it can never drift from ValidDocumentType.
var docTypeEnum = moysklad.DocumentTypeStrings()

// ---- search_documents -----------------------------------------------------

func registerSearchDocuments(s *server.MCPServer, api MoyskladAPI) {
	tool := newTool("search_documents",
		mcp.WithDescription(
			"Search MoySklad documents of a given type in a date range, optionally by "+
				"counterparty or free text. Returns a `totals` object (sum and paid over "+
				"EVERY matching document) plus the most recent documents in `rows` (id, "+
				"name, date, sum, paid, counterparty, state). Use `totals.sum` for the "+
				"period total — never re-sum `rows`, which may be truncated (see "+
				"`truncated`/`rowCount`). Use to find sales, orders, supplies, invoices and "+
				"returns, then get_document for line items. Note: a demand (sale) document "+
				"sum includes services and does not subtract returns, so it can differ from "+
				"get_profit revenue. Amounts are in the account's base currency.",
		),
		mcp.WithString("type", mcp.Required(), mcp.Description(docTypesHelp), mcp.Enum(docTypeEnum...)),
		mcp.WithString("date_from", mcp.Description("Filter moment >= this date, YYYY-MM-DD. Optional.")),
		mcp.WithString("date_to", mcp.Description("Filter moment <= this date, YYYY-MM-DD. Optional.")),
		mcp.WithString("counterparty_id", mcp.Description("Filter by counterparty UUID. Optional.")),
		mcp.WithString("search", mcp.Description("Free-text search over document name/description. Optional.")),
		mcp.WithNumber("limit", mcp.Description("Max detail rows to return. Does NOT affect totals. Default 100, max 1000.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docType := req.GetString("type", "")
		if !moysklad.ValidDocumentType(docType) {
			return mcp.NewToolResultError("invalid document type. " + docTypesHelp), nil
		}
		from, to := dateArgs(req)
		display := clampLimit(req.GetInt("limit", 100))
		// Limit 0 fetches every matching document so totals cover the whole period.
		q := moysklad.DocumentQuery{
			From:           from,
			To:             to,
			CounterpartyID: req.GetString("counterparty_id", ""),
			Search:         req.GetString("search", ""),
			Order:          "moment,desc",
		}
		// Expand the currency so non-base-currency documents are labelled with
		// their ISO code instead of being silently read as base — but only for
		// document types that actually have a currency (warehouse ops reject it).
		if moysklad.HasCurrency(moysklad.DocumentType(docType)) {
			q.Expand = []string{"rate.currency"}
		}
		docs, err := api.SearchDocuments(ctx, moysklad.DocumentType(docType), q)
		if err != nil {
			return resultOrError[any](nil, err)
		}
		return resultOrError(aggregate.DocumentReport(docs, display), nil)
	})
}

// ---- get_document ---------------------------------------------------------

func registerGetDocument(s *server.MCPServer, api MoyskladAPI) {
	tool := newTool("get_document",
		mcp.WithDescription(
			"Fetch one document by type and id with its line items (positions): "+
				"product, quantity, price, discount, total. Amounts are in the account's base currency.",
		),
		mcp.WithString("type", mcp.Required(), mcp.Description(docTypesHelp), mcp.Enum(docTypeEnum...)),
		mcp.WithString("id", mcp.Required(), mcp.Description("Document UUID.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docType := req.GetString("type", "")
		if !moysklad.ValidDocumentType(docType) {
			return mcp.NewToolResultError("invalid document type. " + docTypesHelp), nil
		}
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError("id is required"), nil
		}
		expand := []string{"positions.assortment", "agent", "state", "store"}
		if moysklad.HasCurrency(moysklad.DocumentType(docType)) {
			expand = append(expand, "rate.currency")
		}
		doc, err := api.GetDocument(ctx, moysklad.DocumentType(docType), id, expand)
		if err != nil {
			return resultOrError[any](nil, err)
		}
		return resultOrError(aggregate.DocumentDetailOf(*doc), nil)
	})
}

// ---- search_counterparty --------------------------------------------------

func registerSearchCounterparty(s *server.MCPServer, api MoyskladAPI) {
	tool := newTool("search_counterparty",
		mcp.WithDescription(
			"Find counterparties (customers/suppliers) by name, INN, phone or email. "+
				"Returns id, name, type, INN, contacts — use the id to filter documents "+
				"or metrics for a specific customer.",
		),
		mcp.WithString("query", mcp.Description("Full-text search term. Optional (empty lists all).")),
		mcp.WithNumber("limit", mcp.Description("Max rows. Default 50, max 1000.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rows, err := api.SearchCounterparties(ctx, moysklad.ListOptions{
			Search: req.GetString("query", ""),
			Limit:  clampLimit(req.GetInt("limit", 50)),
			Order:  "name,asc",
		})
		return resultOrError(rows, err)
	})
}
