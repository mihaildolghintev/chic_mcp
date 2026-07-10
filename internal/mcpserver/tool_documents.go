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
	"purchasereturn, paymentin, paymentout."

// ---- search_documents -----------------------------------------------------

func registerSearchDocuments(s *server.MCPServer, api MoyskladAPI) {
	tool := mcp.NewTool("search_documents",
		mcp.WithDescription(
			"Search MoySklad documents of a given type in a date range, optionally by "+
				"counterparty or free text. Returns compact rows (id, name, date, sum, "+
				"paid, counterparty, state). Use to find sales, orders, supplies, invoices "+
				"and returns, then get_document for line items. Amounts are in the account's base currency.",
		),
		mcp.WithString("type", mcp.Required(), mcp.Description(docTypesHelp)),
		mcp.WithString("date_from", mcp.Description("Filter moment >= this date, YYYY-MM-DD. Optional.")),
		mcp.WithString("date_to", mcp.Description("Filter moment <= this date, YYYY-MM-DD. Optional.")),
		mcp.WithString("counterparty_id", mcp.Description("Filter by counterparty UUID. Optional.")),
		mcp.WithString("search", mcp.Description("Free-text search over document name/description. Optional.")),
		mcp.WithNumber("limit", mcp.Description("Max rows. Default 100, max 1000.")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docType := req.GetString("type", "")
		if !moysklad.ValidDocumentType(docType) {
			return mcp.NewToolResultError("invalid document type. " + docTypesHelp), nil
		}
		from, to := dateArgs(req)
		q := moysklad.DocumentQuery{
			From:           from,
			To:             to,
			CounterpartyID: req.GetString("counterparty_id", ""),
			Search:         req.GetString("search", ""),
			Limit:          clampLimit(req.GetInt("limit", 100)),
			Order:          "moment,desc",
		}
		docs, err := api.SearchDocuments(ctx, moysklad.DocumentType(docType), q)
		if err != nil {
			return resultOrError[any](nil, err)
		}
		return resultOrError(aggregate.DocumentSummaries(docs), nil)
	})
}

// ---- get_document ---------------------------------------------------------

func registerGetDocument(s *server.MCPServer, api MoyskladAPI) {
	tool := mcp.NewTool("get_document",
		mcp.WithDescription(
			"Fetch one document by type and id with its line items (positions): "+
				"product, quantity, price, discount, total. Amounts are in the account's base currency.",
		),
		mcp.WithString("type", mcp.Required(), mcp.Description(docTypesHelp)),
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
		doc, err := api.GetDocument(ctx, moysklad.DocumentType(docType), id,
			[]string{"positions.assortment", "agent", "state", "store"})
		if err != nil {
			return resultOrError[any](nil, err)
		}
		return resultOrError(aggregate.DocumentDetailOf(*doc), nil)
	})
}

// ---- search_counterparty --------------------------------------------------

func registerSearchCounterparty(s *server.MCPServer, api MoyskladAPI) {
	tool := mcp.NewTool("search_counterparty",
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
