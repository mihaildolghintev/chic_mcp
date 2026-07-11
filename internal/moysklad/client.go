// Package moysklad is a thin, testable HTTP client for the MoySklad JSON API
// (https://api.moysklad.ru/api/remap/1.2). It handles static Bearer-token auth,
// the account rate limit (45 requests / 3 seconds), retry with backoff on
// 429/5xx, and offset/limit pagination.
package moysklad

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"mcp.chic.md/internal/tracing"
)

// DefaultBaseURL is the MoySklad JSON API 1.2 root.
const DefaultBaseURL = "https://api.moysklad.ru/api/remap/1.2"

// defaultPageLimit is MoySklad's maximum page size for most list endpoints.
const defaultPageLimit = 1000

// Client is a MoySklad API client. It is safe for concurrent use.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	limiter    *rate.Limiter
	maxRetries int
	baseDelay  time.Duration
	sleep      func(context.Context, time.Duration) error
	userAgent  string
	pageLimit  int
	logger     *slog.Logger
}

// Option configures a Client.
type Option func(*Client)

// NewClient builds a Client authenticated with a static MoySklad access token
// (Settings → Users → Access tokens). The rate limiter defaults to MoySklad's
// documented 45 requests / 3 seconds.
func NewClient(token string, opts ...Option) *Client {
	c := &Client{
		baseURL:    DefaultBaseURL,
		token:      token,
		// Instrumented transport: each MoySklad call becomes a child HTTP span
		// under the TOOL span, separating upstream latency from our own. A no-op
		// when tracing is disabled.
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: tracing.NewTransport(http.DefaultTransport),
		},
		limiter:    rate.NewLimiter(rate.Limit(15), 45), // 45 / 3s == 15/s sustained
		maxRetries: 3,
		baseDelay:  500 * time.Millisecond,
		userAgent:  "mcp-moysklad/0.1",
		pageLimit:  defaultPageLimit,
	}
	c.sleep = sleepCtx
	for _, o := range opts {
		o(c)
	}
	return c
}

// WithBaseURL overrides the API root (used to point tests at httptest).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithHTTPClient injects a custom *http.Client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// WithMaxRetries sets how many times a request is retried on 429/5xx.
func WithMaxRetries(n int) Option { return func(c *Client) { c.maxRetries = n } }

// WithRateLimit sets a custom token-bucket rate (n requests per window).
func WithRateLimit(n int, window time.Duration) Option {
	return func(c *Client) {
		c.limiter = rate.NewLimiter(rate.Limit(float64(n)/window.Seconds()), n)
	}
}

// WithBaseDelay sets the exponential-backoff base for 5xx retries.
func WithBaseDelay(d time.Duration) Option { return func(c *Client) { c.baseDelay = d } }

// WithPageLimit sets the per-page row count for paginated list calls. Defaults
// to MoySklad's maximum of 1000; mainly useful to exercise pagination in tests.
func WithPageLimit(n int) Option {
	return func(c *Client) {
		if n > 0 {
			c.pageLimit = n
		}
	}
}

// WithSleeper overrides the backoff sleep function (used in tests to avoid
// real delays). It must honor context cancellation.
func WithSleeper(f func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = f }
}

// WithLogger overrides the retry-diagnostics logger (default: slog.Default()).
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.logger = l } }

func (c *Client) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// get performs a rate-limited GET with retry and unmarshals the JSON body into
// out. path is relative to the base URL (e.g. "/entity/product").
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	body, err := c.doGet(ctx, path, query)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("moysklad: decode %s: %w", path, err)
	}
	return nil
}

// doGet issues the request, honoring the rate limiter and retrying on 429/5xx.
func (c *Client) doGet(ctx context.Context, path string, query url.Values) ([]byte, error) {
	full := c.baseURL + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json;charset=utf-8")
		req.Header.Set("User-Agent", c.userAgent)
		// MoySklad omits nested collections' rows unless we opt out of the
		// "no arrays without expand" behavior; Accept-Encoding gzip is handled
		// transparently by net/http.

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			c.log().Warn("moysklad request failed, retrying", "path", path, "attempt", attempt, "err", err)
			if wErr := c.backoff(ctx, attempt, 0); wErr != nil {
				return nil, wErr
			}
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			c.log().Warn("moysklad response read failed, retrying", "path", path, "attempt", attempt, "err", readErr)
			if wErr := c.backoff(ctx, attempt, 0); wErr != nil {
				return nil, wErr
			}
			continue
		}

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			lastErr = parseAPIError(resp.StatusCode, body)
			if attempt == c.maxRetries {
				return nil, lastErr
			}
			c.log().Warn("moysklad rate limited, retrying", "path", path, "attempt", attempt)
			if wErr := c.backoff(ctx, attempt, retryAfter(resp.Header)); wErr != nil {
				return nil, wErr
			}
			continue
		case resp.StatusCode >= 500:
			lastErr = parseAPIError(resp.StatusCode, body)
			if attempt == c.maxRetries {
				return nil, lastErr
			}
			c.log().Warn("moysklad server error, retrying", "path", path, "attempt", attempt, "status", resp.StatusCode)
			if wErr := c.backoff(ctx, attempt, 0); wErr != nil {
				return nil, wErr
			}
			continue
		default:
			return nil, parseAPIError(resp.StatusCode, body)
		}
	}
	return nil, lastErr
}

// backoff waits before the next attempt. If retryAfter > 0 (from a 429), it is
// honored; otherwise an exponential backoff on baseDelay is used.
func (c *Client) backoff(ctx context.Context, attempt int, retryAfter time.Duration) error {
	d := retryAfter
	if d <= 0 {
		d = c.baseDelay * (1 << attempt)
	}
	return c.sleep(ctx, d)
}

// retryAfter extracts a wait duration from MoySklad's rate-limit headers.
// The API sends X-Lognex-Retry-After in milliseconds; Retry-After is seconds.
func retryAfter(h http.Header) time.Duration {
	if v := h.Get("X-Lognex-Retry-After"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			return time.Duration(ms) * time.Millisecond
		}
	}
	if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.Atoi(v); err == nil {
			return time.Duration(s) * time.Second
		}
	}
	return 0
}

func parseAPIError(status int, body []byte) *APIError {
	apiErr := &APIError{StatusCode: status}
	_ = json.Unmarshal(body, apiErr) // best-effort; body may not be JSON
	return apiErr
}
