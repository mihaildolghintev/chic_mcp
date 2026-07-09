package mcpserver

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"mcp.chic.md/internal/aggregate"
	"mcp.chic.md/internal/moysklad"
)

func init() { register(registerListProducts) }

func registerListProducts(s *server.MCPServer, api MoyskladAPI) {
	tool := mcp.NewTool("list_products",
		mcp.WithDescription(
			"List products from the MoySklad catalog. Use to look up items by "+
				"name/article, check catalog contents, or resolve a product before "+
				"querying stock. Prices are returned in rubles. Returns id, name, "+
				"code, article, archived flag, sale price and buy price.",
		),
		mcp.WithString("query",
			mcp.Description("Full-text search over product name, code and article. Optional."),
		),
		mcp.WithBoolean("include_archived",
			mcp.Description("Include archived products. Defaults to false."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of products to return. Defaults to 100; max 1000."),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		opts := moysklad.ListOptions{
			Search: req.GetString("query", ""),
			Limit:  clampLimit(req.GetInt("limit", 100)),
			Order:  "name,asc",
		}
		if !req.GetBool("include_archived", false) {
			opts.Filter = append(opts.Filter, "archived=false")
		}

		products, err := api.ListProducts(ctx, opts)
		return resultOrError(aggregate.Products(products), err)
	})
}
