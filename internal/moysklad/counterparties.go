package moysklad

import "context"

// Counterparty is a trimmed entity/counterparty row.
type Counterparty struct {
	Meta        Meta   `json:"meta"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	CompanyType string `json:"companyType,omitempty"` // legal|entrepreneur|individual
	INN         string `json:"inn,omitempty"`
	Email       string `json:"email,omitempty"`
	Phone       string `json:"phone,omitempty"`
	Archived    bool   `json:"archived,omitempty"`
	Description string `json:"description,omitempty"`
}

// SearchCounterparties lists counterparties (entity/counterparty).
func (c *Client) SearchCounterparties(ctx context.Context, opts ListOptions) ([]Counterparty, error) {
	return getAll[Counterparty](ctx, c, "/entity/counterparty", opts)
}
