package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestInitDisabled verifies that with no collector endpoint configured Init is
// a no-op: it returns a shutdown func that succeeds and installs nothing that
// would try to reach a network.
func TestInitDisabled(t *testing.T) {
	t.Setenv("PHOENIX_COLLECTOR_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init disabled: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestInitExportsSpans points Init at a fake OTLP/HTTP collector and asserts a
// span created off the process tracer is delivered to /v1/traces on shutdown.
// It exercises the full path: endpoint parsing, exporter build, span emission,
// and the flush-on-shutdown that keeps the last batch from being dropped.
func TestInitExportsSpans(t *testing.T) {
	var (
		mu   sync.Mutex
		hits []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("PHOENIX_COLLECTOR_ENDPOINT", srv.URL) // http://127.0.0.1:PORT
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	_, span := Tracer().Start(context.Background(), "llm.test")
	span.SetAttributes(SpanKind(SpanKindLLM), ModelName("deepseek-chat"), Provider("deepseek"))
	span.SetAttributes(Tokens(10, 20, 30)...)
	span.End()

	// Shutdown flushes the batch processor synchronously.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hits) == 0 {
		t.Fatal("collector received no export requests")
	}
	if hits[0] != "/v1/traces" {
		t.Fatalf("exported to %q, want /v1/traces", hits[0])
	}
}
