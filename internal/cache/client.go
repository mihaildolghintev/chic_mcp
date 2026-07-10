package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"mcp.chic.md/internal/moysklad"
)

// Source is the MoySklad API surface the cache wraps. *moysklad.Client
// satisfies it, and Client (below) implements the same method set — so a
// *cache.Client is a drop-in for mcpserver.MoyskladAPI.
type Source interface {
	ListProducts(ctx context.Context, opts moysklad.ListOptions) ([]moysklad.Product, error)
	GetDashboard(ctx context.Context, period string) (*moysklad.Dashboard, error)
	ProfitByProduct(ctx context.Context, variant bool, opts moysklad.ProfitOptions) ([]moysklad.ProfitByProductRow, error)
	ProfitByEntity(ctx context.Context, dimension string, opts moysklad.ProfitOptions) ([]moysklad.ProfitByEntityRow, error)
	GetTurnover(ctx context.Context, opts moysklad.ProfitOptions) ([]moysklad.TurnoverRow, error)
	GetStock(ctx context.Context, opts moysklad.StockOptions) ([]moysklad.StockRow, error)
	GetCounterpartyReport(ctx context.Context, filter []string, limit int) ([]moysklad.CounterpartyRow, error)
	GetMoneySeries(ctx context.Context, from, to, interval string) (*moysklad.MoneySeries, error)
	SearchDocuments(ctx context.Context, docType moysklad.DocumentType, q moysklad.DocumentQuery) ([]moysklad.Document, error)
	GetDocument(ctx context.Context, docType moysklad.DocumentType, id string, expand []string) (*moysklad.Document, error)
	SearchCounterparties(ctx context.Context, opts moysklad.ListOptions) ([]moysklad.Counterparty, error)
	AccountCurrency(ctx context.Context) (*moysklad.Currency, error)
}

// TTLs holds the cache lifetime per call class. Zero disables caching for that
// class. Defaults are applied by DefaultTTLs.
type TTLs struct {
	Products     time.Duration
	Dashboard    time.Duration
	Reports      time.Duration // profit, turnover, stock, counterparty, money
	Documents    time.Duration
	Counterparty time.Duration // entity/counterparty search
	Currency     time.Duration // entity/currency (account currency)
}

// DefaultTTLs returns sensible cache lifetimes. Reports change on the order of
// minutes; catalog data far less often.
func DefaultTTLs() TTLs {
	return TTLs{
		Products:     30 * time.Minute,
		Dashboard:    5 * time.Minute,
		Reports:      10 * time.Minute,
		Documents:    5 * time.Minute,
		Counterparty: 30 * time.Minute,
		Currency:     24 * time.Hour,
	}
}

// Client is a caching decorator around a Source.
type Client struct {
	src   Source
	store *Store
	ttl   TTLs
}

// New wraps src with a cache backed by store.
func New(src Source, store *Store, ttl TTLs) *Client {
	return &Client{src: src, store: store, ttl: ttl}
}

// do memoizes fetch under a key derived from method + params.
func do[T any](c *Client, ttl time.Duration, method string, params any, fetch func() (T, error)) (T, error) {
	var zero T
	if ttl <= 0 {
		return fetch()
	}
	k := key(method, params)
	if b, ok := c.store.Get(k); ok {
		var v T
		if err := json.Unmarshal(b, &v); err == nil {
			return v, nil
		}
	}
	v, err := fetch()
	if err != nil {
		return zero, err
	}
	if b, err := json.Marshal(v); err == nil {
		c.store.Set(k, b, ttl)
	}
	return v, nil
}

// key hashes the method name and JSON-encoded params into a stable cache key.
func key(method string, params any) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	if b, err := json.Marshal(params); err == nil {
		h.Write(b)
	}
	return method + ":" + hex.EncodeToString(h.Sum(nil)[:16])
}

// ---- Source method implementations ---------------------------------------

func (c *Client) ListProducts(ctx context.Context, opts moysklad.ListOptions) ([]moysklad.Product, error) {
	return do(c, c.ttl.Products, "ListProducts", opts, func() ([]moysklad.Product, error) {
		return c.src.ListProducts(ctx, opts)
	})
}

func (c *Client) GetDashboard(ctx context.Context, period string) (*moysklad.Dashboard, error) {
	return do(c, c.ttl.Dashboard, "GetDashboard", period, func() (*moysklad.Dashboard, error) {
		return c.src.GetDashboard(ctx, period)
	})
}

func (c *Client) ProfitByProduct(ctx context.Context, variant bool, opts moysklad.ProfitOptions) ([]moysklad.ProfitByProductRow, error) {
	return do(c, c.ttl.Reports, "ProfitByProduct", []any{variant, opts}, func() ([]moysklad.ProfitByProductRow, error) {
		return c.src.ProfitByProduct(ctx, variant, opts)
	})
}

func (c *Client) ProfitByEntity(ctx context.Context, dimension string, opts moysklad.ProfitOptions) ([]moysklad.ProfitByEntityRow, error) {
	return do(c, c.ttl.Reports, "ProfitByEntity", []any{dimension, opts}, func() ([]moysklad.ProfitByEntityRow, error) {
		return c.src.ProfitByEntity(ctx, dimension, opts)
	})
}

func (c *Client) GetTurnover(ctx context.Context, opts moysklad.ProfitOptions) ([]moysklad.TurnoverRow, error) {
	return do(c, c.ttl.Reports, "GetTurnover", opts, func() ([]moysklad.TurnoverRow, error) {
		return c.src.GetTurnover(ctx, opts)
	})
}

func (c *Client) GetStock(ctx context.Context, opts moysklad.StockOptions) ([]moysklad.StockRow, error) {
	return do(c, c.ttl.Reports, "GetStock", opts, func() ([]moysklad.StockRow, error) {
		return c.src.GetStock(ctx, opts)
	})
}

func (c *Client) GetCounterpartyReport(ctx context.Context, filter []string, limit int) ([]moysklad.CounterpartyRow, error) {
	return do(c, c.ttl.Reports, "GetCounterpartyReport", []any{filter, limit}, func() ([]moysklad.CounterpartyRow, error) {
		return c.src.GetCounterpartyReport(ctx, filter, limit)
	})
}

func (c *Client) GetMoneySeries(ctx context.Context, from, to, interval string) (*moysklad.MoneySeries, error) {
	return do(c, c.ttl.Reports, "GetMoneySeries", []any{from, to, interval}, func() (*moysklad.MoneySeries, error) {
		return c.src.GetMoneySeries(ctx, from, to, interval)
	})
}

func (c *Client) SearchDocuments(ctx context.Context, docType moysklad.DocumentType, q moysklad.DocumentQuery) ([]moysklad.Document, error) {
	return do(c, c.ttl.Documents, "SearchDocuments", []any{docType, q}, func() ([]moysklad.Document, error) {
		return c.src.SearchDocuments(ctx, docType, q)
	})
}

func (c *Client) GetDocument(ctx context.Context, docType moysklad.DocumentType, id string, expand []string) (*moysklad.Document, error) {
	return do(c, c.ttl.Documents, "GetDocument", []any{docType, id, expand}, func() (*moysklad.Document, error) {
		return c.src.GetDocument(ctx, docType, id, expand)
	})
}

func (c *Client) SearchCounterparties(ctx context.Context, opts moysklad.ListOptions) ([]moysklad.Counterparty, error) {
	return do(c, c.ttl.Counterparty, "SearchCounterparties", opts, func() ([]moysklad.Counterparty, error) {
		return c.src.SearchCounterparties(ctx, opts)
	})
}

func (c *Client) AccountCurrency(ctx context.Context) (*moysklad.Currency, error) {
	return do(c, c.ttl.Currency, "AccountCurrency", nil, func() (*moysklad.Currency, error) {
		return c.src.AccountCurrency(ctx)
	})
}
