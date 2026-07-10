package aggregate

import "mcp.chic.md/internal/moysklad"

// ProductSummary is the compact product shape handed to the LLM. Prices are in
// major units (converted from MoySklad minor units) — never expose raw minor units upward.
type ProductSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Code     string `json:"code,omitempty"`
	Article  string `json:"article,omitempty"`
	Archived bool   `json:"archived"`
	// SalePrice is the first configured sale price, in major units. 0 if unset.
	SalePrice float64 `json:"salePrice"`
	// BuyPrice is the purchase price, in major units. 0 if unset.
	BuyPrice float64 `json:"buyPrice"`
}

// Product converts one raw MoySklad product into a ProductSummary.
func Product(p moysklad.Product) ProductSummary {
	s := ProductSummary{
		ID:       p.ID,
		Name:     p.Name,
		Code:     p.Code,
		Article:  p.Article,
		Archived: p.Archived,
	}
	if len(p.SalePrices) > 0 {
		s.SalePrice = MinorToMajor(p.SalePrices[0].Value)
	}
	if p.BuyPrice != nil {
		s.BuyPrice = MinorToMajor(p.BuyPrice.Value)
	}
	return s
}

// Products converts a slice of raw products.
func Products(ps []moysklad.Product) []ProductSummary {
	out := make([]ProductSummary, 0, len(ps))
	for _, p := range ps {
		out = append(out, Product(p))
	}
	return out
}
