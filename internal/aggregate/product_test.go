package aggregate

import (
	"testing"

	"mcp.chic.md/internal/moysklad"
)

func TestKopecksToRubles(t *testing.T) {
	cases := []struct {
		kopecks float64
		want    float64
	}{
		{0, 0},
		{129900, 1299.00},
		{45050, 450.50},
		{1, 0.01},
		{99, 0.99},
		{100, 1.00},
	}
	for _, c := range cases {
		if got := KopecksToRubles(c.kopecks); got != c.want {
			t.Errorf("KopecksToRubles(%v) = %v, want %v", c.kopecks, got, c.want)
		}
	}
}

func TestProduct_FullConversion(t *testing.T) {
	raw := moysklad.Product{
		ID:       "abc",
		Name:     "Кофе зерновой 1кг",
		Code:     "COFFEE-1KG",
		Article:  "CF-001",
		Archived: false,
		SalePrices: []moysklad.SalePrice{
			{Value: 129900},
			{Value: 150000}, // should be ignored (only first)
		},
		BuyPrice: &moysklad.BuyPrice{Value: 75000},
	}
	got := Product(raw)
	if got.SalePrice != 1299.00 {
		t.Errorf("SalePrice = %v, want 1299.00", got.SalePrice)
	}
	if got.BuyPrice != 750.00 {
		t.Errorf("BuyPrice = %v, want 750.00", got.BuyPrice)
	}
	if got.Name != "Кофе зерновой 1кг" {
		t.Errorf("Name = %q", got.Name)
	}
}

func TestProduct_EmptyPrices(t *testing.T) {
	got := Product(moysklad.Product{ID: "x", Name: "No prices"})
	if got.SalePrice != 0 {
		t.Errorf("SalePrice = %v, want 0 when no sale prices", got.SalePrice)
	}
	if got.BuyPrice != 0 {
		t.Errorf("BuyPrice = %v, want 0 when no buy price", got.BuyPrice)
	}
}

func TestProducts_EmptySlice(t *testing.T) {
	got := Products(nil)
	if got == nil {
		t.Fatal("Products(nil) = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}
