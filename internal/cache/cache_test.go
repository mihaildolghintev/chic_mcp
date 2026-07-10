package cache

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"mcp.chic.md/internal/moysklad"
)

func TestStore_SetGetExpiry(t *testing.T) {
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Unix(1_000_000, 0)
	s.now = func() time.Time { return now }

	s.Set("k", []byte("v"), time.Minute)
	if b, ok := s.Get("k"); !ok || string(b) != "v" {
		t.Fatalf("Get after Set = %q, %v; want v, true", b, ok)
	}

	// Advance past the TTL: entry is now expired.
	now = now.Add(2 * time.Minute)
	if _, ok := s.Get("k"); ok {
		t.Error("expired entry still returned")
	}
	if n := s.Purge(); n != 1 {
		t.Errorf("Purge removed %d, want 1", n)
	}
}

func TestStore_ZeroTTLNoStore(t *testing.T) {
	s, _ := OpenStore(":memory:")
	t.Cleanup(func() { s.Close() })
	s.Set("k", []byte("v"), 0)
	if _, ok := s.Get("k"); ok {
		t.Error("zero-TTL entry should not be stored")
	}
}

// countingSource counts how many times each method reaches the upstream.
type countingSource struct {
	products atomic.Int32
	reports  atomic.Int32
	err      error
}

func (c *countingSource) ListProducts(context.Context, moysklad.ListOptions) ([]moysklad.Product, error) {
	c.products.Add(1)
	return []moysklad.Product{{ID: "p1", Name: "Coffee"}}, c.err
}
func (c *countingSource) GetDashboard(context.Context, string) (*moysklad.Dashboard, error) {
	return &moysklad.Dashboard{}, c.err
}
func (c *countingSource) ProfitByProduct(context.Context, bool, moysklad.ProfitOptions) ([]moysklad.ProfitByProductRow, error) {
	c.reports.Add(1)
	return []moysklad.ProfitByProductRow{{Assortment: moysklad.NamedRef{Name: "Coffee"}, SellSum: 1000}}, c.err
}
func (c *countingSource) ProfitByEntity(context.Context, string, moysklad.ProfitOptions) ([]moysklad.ProfitByEntityRow, error) {
	return nil, c.err
}
func (c *countingSource) GetTurnover(context.Context, moysklad.ProfitOptions) ([]moysklad.TurnoverRow, error) {
	return nil, c.err
}
func (c *countingSource) GetStock(context.Context, moysklad.StockOptions) ([]moysklad.StockRow, error) {
	return nil, c.err
}
func (c *countingSource) GetCounterpartyReport(context.Context, []string, int) ([]moysklad.CounterpartyRow, error) {
	return nil, c.err
}
func (c *countingSource) GetMoneySeries(context.Context, string, string, string) (*moysklad.MoneySeries, error) {
	return &moysklad.MoneySeries{}, c.err
}
func (c *countingSource) SearchDocuments(context.Context, moysklad.DocumentType, moysklad.DocumentQuery) ([]moysklad.Document, error) {
	return nil, c.err
}
func (c *countingSource) GetDocument(context.Context, moysklad.DocumentType, string, []string) (*moysklad.Document, error) {
	return &moysklad.Document{}, c.err
}
func (c *countingSource) SearchCounterparties(context.Context, moysklad.ListOptions) ([]moysklad.Counterparty, error) {
	return nil, c.err
}
func (c *countingSource) AccountCurrency(context.Context) (*moysklad.Currency, error) {
	return &moysklad.Currency{ISOCode: "MDL", Name: "лей", Default: true}, c.err
}

func TestClient_MemoizesRepeatedCalls(t *testing.T) {
	store, _ := OpenStore(":memory:")
	t.Cleanup(func() { store.Close() })
	src := &countingSource{}
	c := New(src, store, DefaultTTLs())
	ctx := context.Background()

	opts := moysklad.ListOptions{Search: "coffee", Limit: 100}
	for i := 0; i < 3; i++ {
		got, err := c.ListProducts(ctx, opts)
		if err != nil {
			t.Fatalf("ListProducts: %v", err)
		}
		if len(got) != 1 || got[0].Name != "Coffee" {
			t.Fatalf("result %d wrong: %+v", i, got)
		}
	}
	if n := src.products.Load(); n != 1 {
		t.Errorf("upstream ListProducts hit %d times, want 1 (cached)", n)
	}

	// Different params -> distinct key -> a second upstream call.
	if _, err := c.ListProducts(ctx, moysklad.ListOptions{Search: "tea"}); err != nil {
		t.Fatal(err)
	}
	if n := src.products.Load(); n != 2 {
		t.Errorf("distinct params: upstream hit %d times, want 2", n)
	}
}

func TestClient_DistinctMethodsDistinctKeys(t *testing.T) {
	store, _ := OpenStore(":memory:")
	t.Cleanup(func() { store.Close() })
	src := &countingSource{}
	c := New(src, store, DefaultTTLs())
	ctx := context.Background()

	// Same params shape but different report variant flag must not collide.
	if _, err := c.ProfitByProduct(ctx, false, moysklad.ProfitOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ProfitByProduct(ctx, true, moysklad.ProfitOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := src.reports.Load(); n != 2 {
		t.Errorf("variant flag ignored in key: upstream hit %d, want 2", n)
	}
}

func TestClient_ErrorsNotCached(t *testing.T) {
	store, _ := OpenStore(":memory:")
	t.Cleanup(func() { store.Close() })
	src := &countingSource{err: context.DeadlineExceeded}
	c := New(src, store, DefaultTTLs())

	for i := 0; i < 2; i++ {
		if _, err := c.ListProducts(context.Background(), moysklad.ListOptions{}); err == nil {
			t.Fatal("expected error")
		}
	}
	if n := src.products.Load(); n != 2 {
		t.Errorf("errors were cached: upstream hit %d, want 2", n)
	}
}
