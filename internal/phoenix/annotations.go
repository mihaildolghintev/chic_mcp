// Package phoenix posts human-feedback span annotations to an Arize Phoenix
// instance. The bot's tracing already sends spans over OTLP; this is the
// companion write path for the 👍/👎 a user taps under an answer — Phoenix
// renders those as annotations on the answer's root span, so an audit can
// filter a trace list down to the dialogs that got a thumbs-down.
//
// It targets Phoenix's REST endpoint (POST /v1/span_annotations), reusing the
// same collector base URL and auth headers as the OTLP exporter. With no
// endpoint configured it is a no-op, exactly like tracing itself.
package phoenix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Annotator writes span annotations. The zero value (endpoint == "") is a
// working no-op, so callers never need to nil-check it.
type Annotator struct {
	endpoint string            // full URL to /v1/span_annotations, "" = disabled
	headers  map[string]string // auth/routing headers, e.g. api_key=...
	client   *http.Client
}

// Annotation is one feedback signal attached to a span. Identifier makes the
// write idempotent: Phoenix upserts on (span_id, name, identifier), so a repeat
// tap overwrites the same annotation instead of stacking duplicates — the
// safety net for old messages whose buttons we couldn't remove.
type Annotation struct {
	SpanID     string
	Name       string  // annotation name, e.g. "user_feedback"
	Label      string  // categorical result, e.g. "thumbs_up"
	Score      float64 // numeric result, e.g. 1 or 0
	Identifier string  // upsert key, e.g. "<user>:<span>"
	Metadata   map[string]string
}

// NewFromEnv builds an Annotator from the same environment the tracer uses:
// PHOENIX_COLLECTOR_ENDPOINT (falling back to OTEL_EXPORTER_OTLP_ENDPOINT) for
// the base URL, and OTEL_EXPORTER_OTLP_HEADERS for auth (Phoenix Cloud's
// api_key, a bearer token, …). No endpoint → a no-op annotator.
func NewFromEnv(logger *slog.Logger) *Annotator {
	if logger == nil {
		logger = slog.Default()
	}
	base := firstNonEmpty(os.Getenv("PHOENIX_COLLECTOR_ENDPOINT"), os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if base == "" {
		logger.Info("phoenix feedback disabled (set PHOENIX_COLLECTOR_ENDPOINT to enable)")
		return &Annotator{}
	}
	return &Annotator{
		endpoint: strings.TrimRight(base, "/") + "/v1/span_annotations",
		headers:  parseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")),
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

// Enabled reports whether annotations will actually be sent.
func (a *Annotator) Enabled() bool { return a.endpoint != "" }

// wire is the request body Phoenix expects: a batch of annotations, each with a
// HUMAN annotator kind and a result carrying the label and/or score.
type wire struct {
	Data []wireAnnotation `json:"data"`
}

type wireAnnotation struct {
	SpanID        string            `json:"span_id"`
	Name          string            `json:"name"`
	AnnotatorKind string            `json:"annotator_kind"`
	Result        wireResult        `json:"result"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Identifier    string            `json:"identifier,omitempty"`
}

type wireResult struct {
	Label string  `json:"label,omitempty"`
	Score float64 `json:"score"`
}

// Annotate posts one annotation. It is best-effort: a disabled annotator or a
// blank span id returns nil (nothing to do), and transport/HTTP failures are
// returned so the caller can log them — they must never break the user's tap.
func (a *Annotator) Annotate(ctx context.Context, ann Annotation) error {
	if a.endpoint == "" || ann.SpanID == "" {
		return nil
	}
	body, err := json.Marshal(wire{Data: []wireAnnotation{{
		SpanID:        ann.SpanID,
		Name:          ann.Name,
		AnnotatorKind: "HUMAN",
		Result:        wireResult{Label: ann.Label, Score: ann.Score},
		Metadata:      ann.Metadata,
		Identifier:    ann.Identifier,
	}}})
	if err != nil {
		return fmt.Errorf("phoenix: marshal annotation: %w", err)
	}

	// sync=false lets Phoenix accept and queue the write, returning fast — this
	// runs inline on the callback path, so a slow synchronous insert would sit in
	// front of the "thanks" the user is waiting on.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"?sync=false", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("phoenix: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range a.headers {
		req.Header.Set(k, v)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("phoenix: post annotation: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("phoenix: annotation rejected: %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// parseHeaders reads OTEL_EXPORTER_OTLP_HEADERS ("k1=v1,k2=v2", values
// percent-encoded per the OTel spec) into a header map. Malformed pairs are
// skipped rather than failing — a bad header shouldn't disable feedback wholesale.
func parseHeaders(raw string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			continue
		}
		if dec, err := url.PathUnescape(v); err == nil {
			v = dec
		}
		out[k] = v
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
