// Package llm is a minimal OpenAI-compatible Chat Completions client with a
// two-provider registry: DeepSeek handles plain text (an order of magnitude
// cheaper), OpenAI handles messages that carry images (DeepSeek has no vision
// models). One wire format, two base URLs — no SDK dependency.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"mcp.chic.md/internal/tracing"
)

// ErrNoVisionProvider is returned when a request carries an image but no
// configured provider supports vision (e.g. only DEEPSEEK_API_KEY is set).
var ErrNoVisionProvider = errors.New("llm: no vision-capable provider configured")

// Provider is one OpenAI-compatible endpoint the client can talk to.
type Provider struct {
	Name           string
	BaseURL        string // without trailing slash; /chat/completions is appended
	APIKey         string
	Model          string
	SupportsVision bool
}

// Client routes chat requests to the cheapest capable provider.
type Client struct {
	providers []Provider
	http      *http.Client
}

// New builds a client over the given providers. Order matters: text requests
// go to the first provider, vision requests to the first with SupportsVision.
func New(providers ...Provider) (*Client, error) {
	if len(providers) == 0 {
		return nil, errors.New("llm: no providers configured")
	}
	return &Client{
		providers: providers,
		// A generous backstop; per-request deadlines come from ctx. The transport
		// is instrumented so each completion's HTTP round trip shows up as a child
		// span (a no-op when tracing is disabled).
		http: &http.Client{
			Timeout:   3 * time.Minute,
			Transport: tracing.NewTransport(http.DefaultTransport),
		},
	}, nil
}

// FromEnv assembles the registry from DEEPSEEK_API_KEY and OPENAI_API_KEY
// (either may be absent, not both). Models and base URLs have sane defaults
// overridable via DEEPSEEK_MODEL/OPENAI_MODEL and *_BASE_URL.
func FromEnv() (*Client, error) {
	var providers []Provider
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		providers = append(providers, Provider{
			Name:    "deepseek",
			BaseURL: envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com/v1"),
			APIKey:  key,
			Model:   envOr("DEEPSEEK_MODEL", "deepseek-chat"),
		})
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		providers = append(providers, Provider{
			Name:           "openai",
			BaseURL:        envOr("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			APIKey:         key,
			Model:          envOr("OPENAI_MODEL", "gpt-4o"),
			SupportsVision: true,
		})
	}
	if len(providers) == 0 {
		return nil, errors.New("llm: set DEEPSEEK_API_KEY and/or OPENAI_API_KEY")
	}
	return New(providers...)
}

// HasVision reports whether any provider can handle images — lets callers
// reject a photo before downloading it.
func (c *Client) HasVision() bool {
	for _, p := range c.providers {
		if p.SupportsVision {
			return true
		}
	}
	return false
}

// Request is one chat completion call. The provider is picked by content: any
// image in Messages routes to a vision provider.
type Request struct {
	Messages  []Message
	Tools     []Tool
	MaxTokens int
}

// Response is the first choice of a completion plus accounting.
type Response struct {
	Provider     string
	Message      Message
	FinishReason string
	Usage        Usage
}

type wireRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Tools     []Tool    `json:"tools,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

type wireResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat picks a provider for the request content and runs one completion. It
// opens an OpenInference LLM span so Phoenix shows the model, provider, token
// counts and the messages in/out; the actual HTTP round trip lives in chat.
func (c *Client) Chat(ctx context.Context, req Request) (*Response, error) {
	p, err := c.pick(req.Messages)
	if err != nil {
		return nil, err
	}

	ctx, span := tracing.Tracer().Start(ctx, "llm."+p.Name)
	defer span.End()
	span.SetAttributes(
		tracing.SpanKind(tracing.SpanKindLLM),
		tracing.ModelName(p.Model),
		tracing.Provider(p.Name),
	)
	recordPromptIn(span, p, req)

	resp, err := c.chat(ctx, p, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	span.SetAttributes(tracing.Tokens(resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)...)
	recordPromptOut(span, resp.Message)
	return resp, nil
}

// recordPromptIn lays the full request onto the LLM span the way Phoenix reads
// it: the raw blob (input.value), then every message as an indexed card, the
// tool calls carried on assistant/tool turns, the tools offered to the model,
// and the invocation parameters. This is what makes a prompt inspectable and
// diffable in the UI rather than an opaque string.
func recordPromptIn(span trace.Span, p *Provider, req Request) {
	if in, err := json.Marshal(req.Messages); err == nil {
		span.SetAttributes(tracing.InputJSON(string(in))...)
	}
	for i, m := range req.Messages {
		span.SetAttributes(tracing.InputMessage(i, m.Role, messageContent(m))...)
		for j, tc := range m.ToolCalls {
			span.SetAttributes(tracing.InputMessageToolCall(i, j, tc.Function.Name, tc.Function.Arguments)...)
		}
	}
	for i, t := range req.Tools {
		if schema, err := json.Marshal(t); err == nil {
			span.SetAttributes(tracing.ToolSchema(i, string(schema)))
		}
	}
	params := map[string]any{"model": p.Model}
	if req.MaxTokens > 0 {
		params["max_tokens"] = req.MaxTokens
	}
	if b, err := json.Marshal(params); err == nil {
		span.SetAttributes(tracing.InvocationParameters(string(b)))
	}
}

// recordPromptOut records the completion message: the raw blob plus the
// assistant turn as an output-message card with any tool calls it requested.
func recordPromptOut(span trace.Span, msg Message) {
	if out, err := json.Marshal(msg); err == nil {
		span.SetAttributes(tracing.OutputJSON(string(out))...)
	}
	span.SetAttributes(tracing.OutputMessage(0, msg.Role, msg.Text)...)
	for j, tc := range msg.ToolCalls {
		span.SetAttributes(tracing.OutputMessageToolCall(0, j, tc.Function.Name, tc.Function.Arguments)...)
	}
}

// messageContent renders a message's text for a span card: the plain Text, or
// the text parts of a multimodal message with a marker where an image sits (the
// base64 data URI itself would bloat the span and isn't useful to read).
func messageContent(m Message) string {
	if len(m.Parts) == 0 {
		return m.Text
	}
	var b strings.Builder
	for _, part := range m.Parts {
		switch part.Type {
		case "text":
			b.WriteString(part.Text)
		case "image_url":
			b.WriteString("[image]")
		}
	}
	return b.String()
}

// chat runs one completion against an already-picked provider.
func (c *Client) chat(ctx context.Context, p *Provider, req Request) (*Response, error) {
	body, err := json.Marshal(wireRequest{
		Model:     p.Model,
		Messages:  req.Messages,
		Tools:     req.Tools,
		MaxTokens: req.MaxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("llm: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm: %s request: %w", p.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Error payloads are small; cap the read defensively either way.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("llm: read %s response: %w", p.Name, err)
	}

	var wire wireResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("llm: %s returned status %d with unparseable body: %w", p.Name, resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := "unknown error"
		if wire.Error != nil {
			msg = wire.Error.Message
		}
		return nil, fmt.Errorf("llm: %s status %d: %s", p.Name, resp.StatusCode, msg)
	}
	if len(wire.Choices) == 0 {
		return nil, fmt.Errorf("llm: %s returned no choices", p.Name)
	}

	return &Response{
		Provider:     p.Name,
		Message:      wire.Choices[0].Message,
		FinishReason: wire.Choices[0].FinishReason,
		Usage:        wire.Usage,
	}, nil
}

// pick routes by content: images need a vision provider, text takes the first
// (cheapest) one.
func (c *Client) pick(messages []Message) (*Provider, error) {
	needVision := false
	for _, m := range messages {
		if m.HasImage() {
			needVision = true
			break
		}
	}
	if !needVision {
		return &c.providers[0], nil
	}
	for i := range c.providers {
		if c.providers[i].SupportsVision {
			return &c.providers[i], nil
		}
	}
	return nil, ErrNoVisionProvider
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
