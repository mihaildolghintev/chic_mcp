// Package tracing wires the process to an OpenTelemetry collector — in
// practice an Arize Phoenix instance — and exposes the OpenInference semantic
// attributes the LLM/agent/tool spans carry. Phoenix renders a trace as a
// nested AGENT → LLM → TOOL tree only when spans set openinference.span.kind
// and the input/output/token attributes below, so those keys live here as
// typed helpers rather than being scattered as bare strings across packages.
//
// Instrumentation is opt-in: with no collector endpoint configured Init installs
// a no-op provider, every span becomes a cheap non-recording span, and the bot
// runs exactly as before. This keeps tracing free to leave wired in production
// and toggled by a single env var.
package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// NewTransport wraps an http.RoundTripper so outbound calls (to the LLM
// providers and MoySklad) become child HTTP spans under the LLM/TOOL span that
// issued them — the piece that tells "our code is slow" apart from "the
// upstream is slow". With tracing disabled it is a cheap no-op wrapper.
func NewTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base)
}

// tracerName is the instrumentation scope reported for every span this process
// emits; it shows up in Phoenix as the span's library.
const tracerName = "mcp.chic.md"

// OpenInference span kinds. Phoenix uses these to pick the icon and detail
// panel for a span (an LLM span shows messages and tokens, a TOOL span shows
// arguments/result, an AGENT span frames the whole request).
const (
	SpanKindAgent = "AGENT"
	SpanKindLLM   = "LLM"
	SpanKindTool  = "TOOL"
	SpanKindChain = "CHAIN"
)

// OpenInference attribute keys. These are a stable string contract with
// Phoenix, not OTel semconv — kept as named constants so a typo can't silently
// hide a span's messages or token counts in the UI.
const (
	keySpanKind      = "openinference.span.kind"
	keyInputValue    = "input.value"
	keyInputMime     = "input.mime_type"
	keyOutputValue   = "output.value"
	keyOutputMime    = "output.mime_type"
	keyLLMModelName  = "llm.model_name"
	keyLLMProvider   = "llm.provider"
	keyLLMTokPrompt  = "llm.token_count.prompt"
	keyLLMTokComplet = "llm.token_count.completion"
	keyLLMTokTotal   = "llm.token_count.total"
	keyToolName      = "tool.name"
	keyToolParams    = "tool.parameters"

	// Structured LLM message/tool keys — Phoenix renders these as per-message
	// cards (role, content, tool calls) and a tools panel, which is what makes a
	// prompt actually readable and diffable instead of one JSON blob.
	keyInputMsgPrefix  = "llm.input_messages"
	keyOutputMsgPrefix = "llm.output_messages"
	keyLLMTools        = "llm.tools"
	keyLLMInvocation   = "llm.invocation_parameters"
	keySessionID       = "session.id"

	mimeJSON = "application/json"
	mimeText = "text/plain"
)

// Tracer returns the process tracer. Callers Start spans off it; before Init
// (or with tracing disabled) this is the global no-op tracer, so spans are safe
// to create unconditionally.
func Tracer() trace.Tracer { return otel.Tracer(tracerName) }

// SpanKind tags a span with its OpenInference kind (SpanKind* constant).
func SpanKind(kind string) attribute.KeyValue { return attribute.String(keySpanKind, kind) }

// SpanID returns the span's 16-hex id when the span is recording, or "" when
// tracing is disabled (the no-op tracer yields an invalid span context). It is
// what anchors a Phoenix feedback annotation to this span — and doubles as the
// signal for "is there anything to annotate", so a disabled tracer means no
// 👍/👎 affordance is shown at all.
func SpanID(span trace.Span) string {
	if sc := span.SpanContext(); sc.IsValid() {
		return sc.SpanID().String()
	}
	return ""
}

// Input/Output record the span's payload. The JSON variants tell Phoenix to
// pretty-print the value; use them for messages and tool arguments, the plain
// variants for a user question or a final answer.
func Input(v string) attribute.KeyValue { return attribute.String(keyInputValue, v) }
func InputJSON(v string) []attribute.KeyValue {
	return []attribute.KeyValue{attribute.String(keyInputValue, v), attribute.String(keyInputMime, mimeJSON)}
}
func Output(v string) attribute.KeyValue { return attribute.String(keyOutputValue, v) }
func OutputJSON(v string) []attribute.KeyValue {
	return []attribute.KeyValue{attribute.String(keyOutputValue, v), attribute.String(keyOutputMime, mimeJSON)}
}

// LLM-span attributes: the model, the provider it routed to, and the token
// accounting the completion reported.
func ModelName(m string) attribute.KeyValue { return attribute.String(keyLLMModelName, m) }
func Provider(p string) attribute.KeyValue  { return attribute.String(keyLLMProvider, p) }
func Tokens(prompt, completion, total int) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Int(keyLLMTokPrompt, prompt),
		attribute.Int(keyLLMTokComplet, completion),
		attribute.Int(keyLLMTokTotal, total),
	}
}

// ToolName / ToolParams describe a TOOL span: the function invoked and the raw
// JSON arguments the model passed.
func ToolName(n string) attribute.KeyValue   { return attribute.String(keyToolName, n) }
func ToolParams(p string) attribute.KeyValue { return attribute.String(keyToolParams, p) }

// SessionID groups a trace into a Phoenix Session — set it on the root span of
// a request so a user's whole conversation reads as one thread.
func SessionID(id string) attribute.KeyValue { return attribute.String(keySessionID, id) }

// InvocationParameters records the call's knobs (model, max_tokens, …) as a JSON
// object Phoenix shows alongside the messages.
func InvocationParameters(jsonObj string) attribute.KeyValue {
	return attribute.String(keyLLMInvocation, jsonObj)
}

// InputMessage / OutputMessage flatten one chat message into the indexed
// OpenInference keys Phoenix expects (role + content). Index is the message's
// position in the request (input) or the completion's choices (output).
func InputMessage(i int, role, content string) []attribute.KeyValue {
	return message(keyInputMsgPrefix, i, role, content)
}
func OutputMessage(i int, role, content string) []attribute.KeyValue {
	return message(keyOutputMsgPrefix, i, role, content)
}

// InputMessageToolCall / OutputMessageToolCall attach one tool call (function
// name + raw JSON arguments) to message i — how Phoenix shows what the model
// asked to invoke, and what tool results were fed back.
func InputMessageToolCall(i, j int, name, args string) []attribute.KeyValue {
	return toolCall(keyInputMsgPrefix, i, j, name, args)
}
func OutputMessageToolCall(i, j int, name, args string) []attribute.KeyValue {
	return toolCall(keyOutputMsgPrefix, i, j, name, args)
}

// ToolSchema advertises one tool offered to the model (its full JSON Schema),
// so a trace shows not just what the model called but everything it could have.
func ToolSchema(i int, jsonSchema string) attribute.KeyValue {
	return attribute.String(fmt.Sprintf("%s.%d.tool.json_schema", keyLLMTools, i), jsonSchema)
}

func message(prefix string, i int, role, content string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(fmt.Sprintf("%s.%d.message.role", prefix, i), role),
		attribute.String(fmt.Sprintf("%s.%d.message.content", prefix, i), content),
	}
}

func toolCall(prefix string, i, j int, name, args string) []attribute.KeyValue {
	base := fmt.Sprintf("%s.%d.message.tool_calls.%d.tool_call.function", prefix, i, j)
	return []attribute.KeyValue{
		attribute.String(base+".name", name),
		attribute.String(base+".arguments", args),
	}
}

// Options label the traced service. They surface in Phoenix so you can tell
// which build produced a trace — essential when the whole point is to change a
// prompt and compare the before/after.
type Options struct {
	ServiceName    string // default "chic-bot"
	ServiceVersion string // build version, e.g. buildinfo.Version
	Revision       string // VCS revision, so a trace pins to an exact commit
	Environment    string // deployment env label (prod, dev, local)
}

// Init installs a global TracerProvider exporting OTLP/HTTP to the collector at
// PHOENIX_COLLECTOR_ENDPOINT (falling back to OTEL_EXPORTER_OTLP_ENDPOINT). The
// endpoint is a base URL like http://phoenix:6006 — the exporter appends
// /v1/traces. Phoenix Cloud additionally needs an api_key header, which the
// exporter reads from the standard OTEL_EXPORTER_OTLP_HEADERS env var.
//
// Resource attributes from OTEL_RESOURCE_ATTRIBUTES are merged in (e.g.
// openinference.project.name=chic-bot to route traces to a named Phoenix
// project); explicit opts win over the environment.
//
// With no endpoint set, tracing is disabled and the returned shutdown is a
// no-op. The returned func must be called on shutdown to flush buffered spans;
// a batch processor holds spans in memory and would otherwise drop the last
// batch on exit.
func Init(ctx context.Context, opts Options) (func(context.Context) error, error) {
	endpoint := firstNonEmpty(os.Getenv("PHOENIX_COLLECTOR_ENDPOINT"), os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		slog.Info("tracing disabled (set PHOENIX_COLLECTOR_ENDPOINT to enable)")
		return func(context.Context) error { return nil }, nil
	}

	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("tracing: invalid collector endpoint %q: %w", endpoint, err)
	}

	expOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(u.Host)}
	// Phoenix speaks plain HTTP by default; only a TLS-terminated deployment
	// (https endpoint) keeps the transport secure.
	if u.Scheme != "https" {
		expOpts = append(expOpts, otlptracehttp.WithInsecure())
	}

	exp, err := otlptracehttp.New(ctx, expOpts...)
	if err != nil {
		return nil, fmt.Errorf("tracing: build OTLP exporter: %w", err)
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceName(firstNonEmpty(opts.ServiceName, os.Getenv("OTEL_SERVICE_NAME"), "chic-bot")),
	}
	if opts.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(opts.ServiceVersion))
	}
	if opts.Revision != "" {
		attrs = append(attrs, attribute.String("vcs.revision", opts.Revision))
	}
	if opts.Environment != "" {
		attrs = append(attrs, attribute.String("deployment.environment", opts.Environment))
	}

	// WithFromEnv first so explicit attributes below override any collision.
	res, err := resource.New(ctx, resource.WithFromEnv(), resource.WithAttributes(attrs...))
	if err != nil {
		return nil, fmt.Errorf("tracing: build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	slog.Info("tracing enabled", "collector", endpoint, "transport", "otlp/http")

	return tp.Shutdown, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
