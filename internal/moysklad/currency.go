package moysklad

import (
	"context"
	"fmt"
)

// Currency is a trimmed entity/currency row. Every MoySklad account has one
// currency flagged Default — the "валюта учёта" all reports and the dashboard
// are denominated in. ISOCode ("RUB", "MDL", "EUR") is the stable label; Name
// is the localized short form ("руб.", "лей").
type Currency struct {
	Meta     Meta   `json:"meta"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"fullName,omitempty"`
	ISOCode  string `json:"isoCode,omitempty"`
	Code     string `json:"code,omitempty"` // numeric ISO 4217 code
	Default  bool   `json:"default,omitempty"`
}

// AccountCurrency returns the account's accounting currency — the one MoySklad
// denominates every report, dashboard and stored amount in. It is the entry in
// entity/currency with default=true. The set of currencies is tiny and changes
// almost never, so a single unpaginated fetch is enough.
func (c *Client) AccountCurrency(ctx context.Context) (*Currency, error) {
	rows, err := getAll[Currency](ctx, c, "/entity/currency", ListOptions{Filter: []string{"default=true"}})
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Default {
			return &rows[i], nil
		}
	}
	// Some accounts don't honor the filter; fall back to the first row so the
	// caller still gets a usable label rather than an error.
	if len(rows) > 0 {
		return &rows[0], nil
	}
	return nil, fmt.Errorf("no account currency found")
}
