package moysklad

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return b
}

// newTestClient wires a Client to an httptest server with instant backoff and a
// small page size so pagination is exercised without huge fixtures.
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewClient("test-token",
		WithBaseURL(srv.URL),
		WithPageLimit(2),
		WithBaseDelay(time.Millisecond),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
	)
}

func TestListProducts_Pagination(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)

		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q, want Bearer test-token", got)
		}
		q := r.URL.Query()
		if got := q.Get("limit"); got != "2" {
			t.Errorf("limit = %q, want 2", got)
		}

		switch n {
		case 1:
			if got := q.Get("offset"); got != "0" {
				t.Errorf("page 1 offset = %q, want 0", got)
			}
			w.Write(golden(t, "products_page1.json"))
		case 2:
			if got := q.Get("offset"); got != "2" {
				t.Errorf("page 2 offset = %q, want 2", got)
			}
			w.Write(golden(t, "products_page2.json"))
		default:
			t.Fatalf("unexpected extra request #%d", n)
		}
	})

	got, err := c.ListProducts(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListProducts: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d products, want 3", len(got))
	}
	if got[0].Name != "Кофе зерновой 1кг" {
		t.Errorf("product[0].Name = %q", got[0].Name)
	}
	if got[0].SalePrices[0].Value != 129900 { // kopecks, untouched by client
		t.Errorf("product[0] price = %v, want 129900", got[0].SalePrices[0].Value)
	}
	if !got[2].Archived {
		t.Errorf("product[2].Archived = false, want true")
	}
	if calls != 2 {
		t.Errorf("made %d HTTP calls, want 2", calls)
	}
}

func TestListProducts_QueryEncoding(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("search"); got != "кофе" {
			t.Errorf("search = %q, want кофе", got)
		}
		if got := q.Get("expand"); got != "supplier,images" {
			t.Errorf("expand = %q, want supplier,images", got)
		}
		filters := q["filter"]
		if len(filters) != 2 || filters[0] != "archived=false" || filters[1] != "code=CF-001" {
			t.Errorf("filter = %v", filters)
		}
		if got := q.Get("order"); got != "name,asc" {
			t.Errorf("order = %q, want name,asc", got)
		}
		// short page -> single call
		w.Write(golden(t, "products_page2.json"))
	})

	_, err := c.ListProducts(context.Background(), ListOptions{
		Search: "кофе",
		Filter: []string{"archived=false", "code=CF-001"},
		Expand: []string{"supplier", "images"},
		Order:  "name,asc",
	})
	if err != nil {
		t.Fatalf("ListProducts: %v", err)
	}
}

func TestAccountCurrency_PicksDefault(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/entity/currency" {
			t.Errorf("path = %q, want /entity/currency", got)
		}
		if got := r.URL.Query().Get("filter"); got != "default=true" {
			t.Errorf("filter = %q, want default=true", got)
		}
		// Include a non-default row to prove the picker doesn't just take [0].
		// meta.size == len(rows) so getAll stops after this single short page.
		w.Write([]byte(`{"meta":{"size":2},"rows":[` +
			`{"id":"1","name":"руб.","isoCode":"RUB","code":"643","default":false},` +
			`{"id":"2","name":"лей","isoCode":"MDL","code":"498","default":true}` +
			`]}`))
	})

	cur, err := c.AccountCurrency(context.Background())
	if err != nil {
		t.Fatalf("AccountCurrency: %v", err)
	}
	if cur.ISOCode != "MDL" || cur.Name != "лей" || !cur.Default {
		t.Errorf("got %+v, want the MDL default row", cur)
	}
}

func TestDoGet_RetryOn429(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("X-Lognex-Retry-After", "50")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write(golden(t, "error_429.json"))
			return
		}
		w.Write(golden(t, "products_page2.json"))
	})

	got, err := c.ListProducts(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListProducts after 429 retry: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d products, want 1", len(got))
	}
	if calls != 2 {
		t.Errorf("made %d calls, want 2 (one 429 + one success)", calls)
	}
}

func TestDoGet_RetryOn5xxThenGiveUp(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"errors":[{"error":"upstream","code":500}]}`))
	})

	_, err := c.ListProducts(context.Background(), ListOptions{})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", apiErr.StatusCode)
	}
	// default maxRetries = 3 -> 1 initial + 3 retries = 4 calls
	if calls != 4 {
		t.Errorf("made %d calls, want 4", calls)
	}
}

func TestDoGet_ClientErrorNoRetry(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errors":[{"error":"Неверный токен","code":1053}]}`))
	})

	_, err := c.ListProducts(context.Background(), ListOptions{})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", apiErr.StatusCode)
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1 (4xx must not retry)", calls)
	}
}
