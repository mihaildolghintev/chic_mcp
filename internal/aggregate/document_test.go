package aggregate

import (
	"testing"

	"mcp.chic.md/internal/moysklad"
)

// A discounted line must report price*qty net of the discount, so the positions
// reconcile with the document header (which МойСклад already nets of discounts).
func TestDocumentDetailOf_PositionTotalAppliesDiscount(t *testing.T) {
	doc := moysklad.Document{
		Name: "D-1",
		Positions: &moysklad.ListResponse[moysklad.Position]{
			Rows: []moysklad.Position{
				{Assortment: moysklad.NamedRef{Name: "Widget"}, Quantity: 4, Price: 100_00, Discount: 25, Vat: 20},
			},
		},
	}
	det := DocumentDetailOf(doc)
	if len(det.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(det.Positions))
	}
	p := det.Positions[0]
	// 4 * 100.00 = 400, minus 25% = 300.
	if p.Total != 300 {
		t.Errorf("total = %v, want 300 (discount applied)", p.Total)
	}
	if p.Vat != 20 {
		t.Errorf("vat = %v, want 20 (per-line VAT surfaced)", p.Vat)
	}
}

// A document in a non-base currency must be labelled with its ISO code so the
// amount is not silently read as base currency.
func TestDocumentSummaryOf_CurrencyLabel(t *testing.T) {
	doc := moysklad.Document{
		Name: "USD-invoice",
		Sum:  1000_00,
		Rate: &moysklad.Rate{Value: 90, Currency: &moysklad.Currency{ISOCode: "USD"}},
	}
	s := DocumentSummaryOf(doc)
	if s.Currency != "USD" {
		t.Errorf("currency = %q, want USD", s.Currency)
	}
	// Base-currency documents (no rate) stay unlabelled.
	base := DocumentSummaryOf(moysklad.Document{Name: "base", Sum: 500_00})
	if base.Currency != "" {
		t.Errorf("base currency = %q, want empty", base.Currency)
	}
}
