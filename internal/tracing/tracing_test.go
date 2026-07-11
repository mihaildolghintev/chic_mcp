package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestAttributeKeys pins the OpenInference key strings. They are a contract with
// Phoenix, not something the compiler checks — a typo here silently hides a
// span's messages, tools or session in the UI, so lock the exact keys.
func TestAttributeKeys(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{string(SpanKind(SpanKindLLM).Key), "openinference.span.kind"},
		{string(SessionID("42").Key), "session.id"},
		{string(ModelName("m").Key), "llm.model_name"},
		{string(InvocationParameters("{}").Key), "llm.invocation_parameters"},
		{string(InputMessage(0, "user", "hi")[0].Key), "llm.input_messages.0.message.role"},
		{string(InputMessage(0, "user", "hi")[1].Key), "llm.input_messages.0.message.content"},
		{string(OutputMessage(0, "assistant", "yo")[0].Key), "llm.output_messages.0.message.role"},
		{string(InputMessageToolCall(1, 2, "get_profit", "{}")[0].Key), "llm.input_messages.1.message.tool_calls.2.tool_call.function.name"},
		{string(InputMessageToolCall(1, 2, "get_profit", "{}")[1].Key), "llm.input_messages.1.message.tool_calls.2.tool_call.function.arguments"},
		{string(ToolSchema(3, "{}").Key), "llm.tools.3.tool.json_schema"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("key = %q, want %q", c.got, c.want)
		}
	}
}

// TestInitDisabled verifies that with no collector endpoint configured Init is
// a no-op: it returns a shutdown func that succeeds and installs nothing that
// would try to reach a network.
func TestInitDisabled(t *testing.T) {
	t.Setenv("PHOENIX_COLLECTOR_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := Init(context.Background(), Options{})
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

	shutdown, err := Init(context.Background(), Options{})
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
