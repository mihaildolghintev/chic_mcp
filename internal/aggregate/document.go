package aggregate

import "mcp.chic.md/internal/moysklad"

// DocumentSummary is the compact form of a document list row. Sums in major units.
type DocumentSummary struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Moment       string  `json:"moment"`
	Sum          float64 `json:"sum"`
	Paid         float64 `json:"paid,omitempty"`
	Counterparty string  `json:"counterparty,omitempty"`
	State        string  `json:"state,omitempty"`
	Store        string  `json:"store,omitempty"`
	Channel      string  `json:"channel,omitempty"`
	// Currency is the ISO code the amounts on THIS document are denominated in.
	// Empty means the account base currency. When set and different from the
	// account base, Sum/Paid are in this currency — do not mix with base-currency
	// totals. Only populated when the request expands rate.currency.
	Currency   string `json:"currency,omitempty"`
	Applicable bool   `json:"applicable"`
}

func DocumentSummaryOf(d moysklad.Document) DocumentSummary {
	s := DocumentSummary{
		ID:         d.ID,
		Name:       d.Name,
		Moment:     d.Moment,
		Sum:        MinorToMajor(d.Sum),
		Paid:       MinorToMajor(d.PayedSum),
		Applicable: d.Applicable,
	}
	if d.Rate != nil && d.Rate.Currency != nil {
		s.Currency = d.Rate.Currency.ISOCode
	}
	if d.Agent != nil {
		s.Counterparty = d.Agent.Name
	}
	if d.State != nil {
		s.State = d.State.Name
	}
	if d.Store != nil {
		s.Store = d.Store.Name
	}
	if d.SalesChannel != nil {
		s.Channel = d.SalesChannel.Name
	}
	return s
}

func DocumentSummaries(docs []moysklad.Document) []DocumentSummary {
	out := make([]DocumentSummary, 0, len(docs))
	for _, d := range docs {
		out = append(out, DocumentSummaryOf(d))
	}
	return out
}

// DocumentTotals are the grand totals across every matching document — so the
// model can answer "total sales for the period" without summing rows itself.
type DocumentTotals struct {
	Sum  float64 `json:"sum"`
	Paid float64 `json:"paid"`
}

// DocumentReport summarizes documents, totals over all of them, and truncates
// the detail list to limit. Order is preserved (the API sorts by moment desc),
// so Rows is the most recent documents while Totals covers the whole period.
func DocumentReport(docs []moysklad.Document, limit int) Report[DocumentSummary, DocumentTotals] {
	rows := DocumentSummaries(docs)
	var t DocumentTotals
	for _, r := range rows {
		t.Sum += r.Sum
		t.Paid += r.Paid
	}
	t.Sum = round2(t.Sum)
	t.Paid = round2(t.Paid)
	return newReport(rows, t, limit)
}

// DocumentDetail is a document with its expanded positions.
type DocumentDetail struct {
	DocumentSummary
	Description string        `json:"description,omitempty"`
	VatSum      float64       `json:"vatSum,omitempty"`
	DueDate     string        `json:"paymentDueDate,omitempty"`
	Positions   []PositionRow `json:"positions,omitempty"`
}

type PositionRow struct {
	Name     string  `json:"name"`
	Code     string  `json:"code,omitempty"`
	Quantity float64 `json:"quantity"`
	Price    float64 `json:"price"`
	Discount float64 `json:"discount,omitempty"` // percent
	Vat      int     `json:"vat,omitempty"`      // VAT rate, percent
	Total    float64 `json:"total"`              // price*qty net of discount
}

func DocumentDetailOf(d moysklad.Document) DocumentDetail {
	det := DocumentDetail{
		DocumentSummary: DocumentSummaryOf(d),
		Description:     d.Description,
		VatSum:          MinorToMajor(d.VatSum),
		DueDate:         d.PaymentPlannedMoment,
	}
	if d.Positions != nil {
		for _, p := range d.Positions.Rows {
			price := MinorToMajor(p.Price)
			// МойСклад's document Sum is already net of line discounts, so the
			// line total must subtract the discount too or the positions won't
			// reconcile with the header.
			total := price * p.Quantity * (1 - p.Discount/100)
			det.Positions = append(det.Positions, PositionRow{
				Name:     p.Assortment.Name,
				Code:     p.Assortment.Code,
				Quantity: p.Quantity,
				Price:    price,
				Discount: p.Discount,
				Vat:      p.Vat,
				Total:    round2(total),
			})
		}
	}
	return det
}
