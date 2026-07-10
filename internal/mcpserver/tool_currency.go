package mcpserver

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func init() {
	register(registerGetAccountCurrency)
}

// currencyOut is the compact account-currency payload. isoCode is the stable
// label to render amounts with; name is the localized short form.
type currencyOut struct {
	ISOCode  string `json:"isoCode"`
	Name     string `json:"name"`
	FullName string `json:"fullName,omitempty"`
	Code     string `json:"code,omitempty"`
}

// ---- get_account_currency -------------------------------------------------

func registerGetAccountCurrency(s *server.MCPServer, api MoyskladAPI) {
	tool := mcp.NewTool("get_account_currency",
		mcp.WithDescription(
			"The account's base (accounting) currency — the one every amount from the "+
				"other tools (dashboard, profit, stock, money, documents, prices) is "+
				"denominated in. Call this to know how to label monetary values; do not "+
				"assume rubles. Returns isoCode (e.g. RUB, MDL, EUR) and the short name.",
		),
	)
	s.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cur, err := api.AccountCurrency(ctx)
		if err != nil {
			return resultOrError[any](nil, err)
		}
		return resultOrError(currencyOut{
			ISOCode:  cur.ISOCode,
			Name:     cur.Name,
			FullName: cur.FullName,
			Code:     cur.Code,
		}, nil)
	})
}
