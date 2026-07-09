package aggregate

import "mcp.chic.md/internal/moysklad"

// DocumentSummary is the compact form of a document list row. Sums in rubles.
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
	Applicable   bool    `json:"applicable"`
}

func DocumentSummaryOf(d moysklad.Document) DocumentSummary {
	s := DocumentSummary{
		ID:         d.ID,
		Name:       d.Name,
		Moment:     d.Moment,
		Sum:        KopecksToRubles(d.Sum),
		Paid:       KopecksToRubles(d.PayedSum),
		Applicable: d.Applicable,
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
	Discount float64 `json:"discount,omitempty"`
	Total    float64 `json:"total"`
}

func DocumentDetailOf(d moysklad.Document) DocumentDetail {
	det := DocumentDetail{
		DocumentSummary: DocumentSummaryOf(d),
		Description:     d.Description,
		VatSum:          KopecksToRubles(d.VatSum),
		DueDate:         d.PaymentPlannedMoment,
	}
	if d.Positions != nil {
		for _, p := range d.Positions.Rows {
			price := KopecksToRubles(p.Price)
			det.Positions = append(det.Positions, PositionRow{
				Name:     p.Assortment.Name,
				Code:     p.Assortment.Code,
				Quantity: p.Quantity,
				Price:    price,
				Discount: p.Discount,
				Total:    round2(price * p.Quantity),
			})
		}
	}
	return det
}
