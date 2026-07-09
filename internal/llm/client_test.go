package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capture spins up a fake OpenAI-compatible endpoint that records the last
// request body and replies with a canned completion.
func capture(t *testing.T, reply string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		got["_auth"] = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

const textReply = `{"choices":[{"message":{"role":"assistant","content":"привет"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

func TestChat_BuildsDeepSeekPayload(t *testing.T) {
	srv, got := capture(t, textReply)
	c, err := New(Provider{Name: "deepseek", BaseURL: srv.URL, APIKey: "sk-ds", Model: "deepseek-chat"})
	if err != nil {
		t.Fatal(err)
	}

	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{System("ты бот"), User("сколько товаров?")},
		Tools:    []Tool{{Type: "function", Function: Function{Name: "list_products", Description: "list", Parameters: schema}}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	body := *got
	if body["model"] != "deepseek-chat" {
		t.Errorf("model = %v, want deepseek-chat", body["model"])
	}
	if body["_auth"] != "Bearer sk-ds" {
		t.Errorf("auth header = %v", body["_auth"])
	}
	msgs := body["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "ты бот" {
		t.Errorf("system message wrong: %v", first)
	}
	tools := body["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "list_products" {
		t.Errorf("tool name = %v", fn["name"])
	}
	if _, ok := fn["parameters"].(map[string]any)["properties"]; !ok {
		t.Errorf("tool parameters schema not passed through: %v", fn["parameters"])
	}

	if resp.Message.Text != "привет" || resp.Usage.TotalTokens != 15 {
		t.Errorf("response parsed wrong: %+v", resp)
	}
}

func TestChat_RoutesImagesToVisionProvider(t *testing.T) {
	dsSrv, dsGot := capture(t, textReply)
	oaSrv, oaGot := capture(t, textReply)
	c, err := New(
		Provider{Name: "deepseek", BaseURL: dsSrv.URL, APIKey: "a", Model: "deepseek-chat"},
		Provider{Name: "openai", BaseURL: oaSrv.URL, APIKey: "b", Model: "gpt-4o", SupportsVision: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	// Plain text goes to the first (cheap) provider.
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{User("текст")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "deepseek" || (*dsGot)["model"] != "deepseek-chat" {
		t.Errorf("text routed to %q, want deepseek", resp.Provider)
	}

	// An image routes to the vision provider, content marshaled as parts.
	resp, err = c.Chat(context.Background(), Request{
		Messages: []Message{UserImage("что на фото?", "data:image/jpeg;base64,AAAA")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "openai" || (*oaGot)["model"] != "gpt-4o" {
		t.Errorf("image routed to %q, want openai", resp.Provider)
	}
	msgs := (*oaGot)["messages"].([]any)
	parts := msgs[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("content parts = %d, want 2 (caption+image)", len(parts))
	}
	img := parts[1].(map[string]any)
	if img["type"] != "image_url" {
		t.Errorf("second part type = %v", img["type"])
	}
	url := img["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Errorf("image url = %q, want data URI", url)
	}
}

func TestChat_ImageWithoutVisionProviderFails(t *testing.T) {
	srv, _ := capture(t, textReply)
	c, err := New(Provider{Name: "deepseek", BaseURL: srv.URL, APIKey: "a", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Chat(context.Background(), Request{
		Messages: []Message{UserImage("", "data:image/jpeg;base64,AAAA")},
	})
	if err != ErrNoVisionProvider {
		t.Errorf("err = %v, want ErrNoVisionProvider", err)
	}
	if c.HasVision() {
		t.Error("HasVision() = true for text-only registry")
	}
}

func TestChat_ParsesToolCalls(t *testing.T) {
	reply := `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
		{"id":"call_1","type":"function","function":{"name":"get_stock","arguments":"{\"query\":\"футболка\"}"}}
	]},"finish_reason":"tool_calls"}],"usage":{"total_tokens":42}}`
	srv, _ := capture(t, reply)
	c, err := New(Provider{Name: "deepseek", BaseURL: srv.URL, APIKey: "a", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{User("остатки?")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.Message.ToolCalls))
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "get_stock" {
		t.Errorf("tool call parsed wrong: %+v", tc)
	}
	if !strings.Contains(tc.Function.Arguments, "футболка") {
		t.Errorf("arguments = %q", tc.Function.Arguments)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q", resp.FinishReason)
	}
}

func TestChat_APIErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Provider{Name: "deepseek", BaseURL: srv.URL, APIKey: "bad", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Chat(context.Background(), Request{Messages: []Message{User("hi")}})
	if err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("err = %v, want provider error message surfaced", err)
	}
}

// The assistant tool-call turn must round-trip with tool_calls intact — the
// agent appends the model's own message back into the conversation.
func TestMessage_ToolCallRoundTrip(t *testing.T) {
	orig := Message{
		Role:      "assistant",
		ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "f", Arguments: "{}"}}},
	}
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var back Message
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.ToolCalls) != 1 || back.ToolCalls[0].ID != "c1" {
		t.Errorf("round trip lost tool calls: %+v", back)
	}
}

func TestFromEnv_RequiresAKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := FromEnv(); err == nil {
		t.Error("FromEnv with no keys should fail")
	}

	t.Setenv("DEEPSEEK_API_KEY", "sk-x")
	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv with deepseek key: %v", err)
	}
	if c.HasVision() {
		t.Error("deepseek-only registry must not claim vision")
	}
}
