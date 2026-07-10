package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"mcp.chic.md/internal/llm"
	"mcp.chic.md/internal/mcpserver"
	"mcp.chic.md/internal/moysklad"
	"mcp.chic.md/internal/store"
)

// fakeAPI implements mcpserver.MoyskladAPI with canned data, recording the
// list_products call the scripted LLM makes.
type fakeAPI struct {
	products    []moysklad.Product
	gotProducts *moysklad.ListOptions
}

func (f *fakeAPI) ListProducts(_ context.Context, opts moysklad.ListOptions) ([]moysklad.Product, error) {
	f.gotProducts = &opts
	return f.products, nil
}
func (f *fakeAPI) GetDashboard(context.Context, string) (*moysklad.Dashboard, error) {
	return &moysklad.Dashboard{}, nil
}
func (f *fakeAPI) ProfitByProduct(context.Context, bool, moysklad.ProfitOptions) ([]moysklad.ProfitByProductRow, error) {
	return nil, nil
}
func (f *fakeAPI) ProfitByEntity(context.Context, string, moysklad.ProfitOptions) ([]moysklad.ProfitByEntityRow, error) {
	return nil, nil
}
func (f *fakeAPI) GetTurnover(context.Context, moysklad.ProfitOptions) ([]moysklad.TurnoverRow, error) {
	return nil, nil
}
func (f *fakeAPI) GetStock(context.Context, moysklad.StockOptions) ([]moysklad.StockRow, error) {
	return nil, nil
}
func (f *fakeAPI) GetCounterpartyReport(context.Context, []string, int) ([]moysklad.CounterpartyRow, error) {
	return nil, nil
}
func (f *fakeAPI) GetMoneySeries(context.Context, string, string, string) (*moysklad.MoneySeries, error) {
	return nil, nil
}
func (f *fakeAPI) SearchDocuments(context.Context, moysklad.DocumentType, moysklad.DocumentQuery) ([]moysklad.Document, error) {
	return nil, nil
}
func (f *fakeAPI) GetDocument(context.Context, moysklad.DocumentType, string, []string) (*moysklad.Document, error) {
	return nil, nil
}
func (f *fakeAPI) SearchCounterparties(context.Context, moysklad.ListOptions) ([]moysklad.Counterparty, error) {
	return nil, nil
}
func (f *fakeAPI) AccountCurrency(context.Context) (*moysklad.Currency, error) {
	return &moysklad.Currency{ISOCode: "MDL", Name: "лей", Default: true}, nil
}

// scriptedLLM replies with each canned response in turn and records every
// request body it saw.
type scriptedLLM struct {
	responses []string
	requests  []map[string]any
}

func (s *scriptedLLM) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode llm request: %v", err)
		}
		s.requests = append(s.requests, body)
		if len(s.requests) > len(s.responses) {
			t.Errorf("unexpected llm request #%d", len(s.requests))
			http.Error(w, "out of script", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(s.responses[len(s.requests)-1]))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func final(text string, tokens int) string {
	raw, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"message":       map[string]any{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"total_tokens": tokens},
	})
	return string(raw)
}

func toolCall(name, args string, tokens int) string {
	raw, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]any{{
					"id":       "call_1",
					"type":     "function",
					"function": map[string]any{"name": name, "arguments": args},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"total_tokens": tokens},
	})
	return string(raw)
}

func newTestAgent(t *testing.T, script *scriptedLLM, api mcpserver.MoyskladAPI, opts Options) (*Agent, *store.DB) {
	t.Helper()
	srv := script.serve(t)
	llmClient, err := llm.New(llm.Provider{Name: "test", BaseURL: srv.URL, APIKey: "k", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	a, err := New(context.Background(), llmClient, mcpserver.New(api), st, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a, st
}

// TestHandle_EndToEnd drives the full loop: user question → LLM asks for
// list_products → tool runs against fakeAPI over in-process MCP → result goes
// back to the LLM → final text lands, dialog is persisted.
func TestHandle_EndToEnd(t *testing.T) {
	api := &fakeAPI{products: []moysklad.Product{{Name: "Футболка", SalePrices: []moysklad.SalePrice{{Value: 990_00}}}}}
	script := &scriptedLLM{responses: []string{
		toolCall("list_products", `{"query":"футболка"}`, 100),
		final("Нашёл 1 товар: Футболка, 990 ₽.", 50),
	}}
	a, st := newTestAgent(t, script, api, Options{})

	answer, err := a.Handle(context.Background(), 7, "что есть из футболок?", "")
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if answer != "Нашёл 1 товар: Футболка, 990 ₽." {
		t.Errorf("answer = %q", answer)
	}

	// The tool really executed against the fake MoySklad.
	if api.gotProducts == nil {
		t.Fatal("list_products never reached the API")
	}
	if api.gotProducts.Search != "футболка" {
		t.Errorf("tool arguments not passed through: %+v", api.gotProducts)
	}

	// Round 2's request must contain the tool result for the model to read.
	second := script.requests[1]
	msgs := second["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != "call_1" {
		t.Errorf("last message of round 2 = %v, want tool result", last)
	}
	if !strings.Contains(last["content"].(string), "Футболка") {
		t.Errorf("tool result content = %v", last["content"])
	}
	// Tools must be advertised to the model.
	if tools := second["tools"].([]any); len(tools) < 10 {
		t.Errorf("only %d tools advertised, want the full MoySklad surface", len(tools))
	}

	// Both dialog turns persisted.
	hist, err := st.RecentMessages(context.Background(), 7, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].Role != "user" || hist[1].Role != "assistant" {
		t.Errorf("history = %+v, want user+assistant", hist)
	}
}

// TestHandle_HistoryReplayed: a second question must carry the first exchange.
func TestHandle_HistoryReplayed(t *testing.T) {
	script := &scriptedLLM{responses: []string{final("ответ 1", 10), final("ответ 2", 10)}}
	a, _ := newTestAgent(t, script, &fakeAPI{}, Options{})

	ctx := context.Background()
	if _, err := a.Handle(ctx, 7, "вопрос 1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Handle(ctx, 7, "вопрос 2", ""); err != nil {
		t.Fatal(err)
	}

	msgs := script.requests[1]["messages"].([]any)
	// system + user("вопрос 1") + assistant("ответ 1") + user("вопрос 2")
	if len(msgs) != 4 {
		t.Fatalf("second request has %d messages, want 4: %v", len(msgs), msgs)
	}
	if m := msgs[1].(map[string]any); m["role"] != "user" || m["content"] != "вопрос 1" {
		t.Errorf("history user turn = %v", m)
	}
	if m := msgs[2].(map[string]any); m["role"] != "assistant" || m["content"] != "ответ 1" {
		t.Errorf("history assistant turn = %v", m)
	}
}

func TestHandle_RateLimited(t *testing.T) {
	script := &scriptedLLM{responses: []string{final("ок", 10)}}
	a, _ := newTestAgent(t, script, &fakeAPI{}, Options{RatePerHour: 1})

	ctx := context.Background()
	if _, err := a.Handle(ctx, 7, "первый", ""); err != nil {
		t.Fatal(err)
	}
	answer, err := a.Handle(ctx, 7, "второй", "")
	if err != nil {
		t.Fatal(err)
	}
	if answer != msgRateLimited {
		t.Errorf("answer = %q, want rate-limit message", answer)
	}
	if len(script.requests) != 1 {
		t.Errorf("rate-limited request still reached the LLM (%d calls)", len(script.requests))
	}
}

// TestHandle_TokenStopLoss: a huge first round must halt before more tool
// calls are funded — only one LLM request may be served.
func TestHandle_TokenStopLoss(t *testing.T) {
	script := &scriptedLLM{responses: []string{
		toolCall("list_products", `{}`, 999_999),
	}}
	a, _ := newTestAgent(t, script, &fakeAPI{}, Options{MaxTokens: 1000})

	answer, err := a.Handle(context.Background(), 7, "дорогой вопрос", "")
	if err != nil {
		t.Fatal(err)
	}
	if answer != msgBudgetSpent {
		t.Errorf("answer = %q, want token stop-loss message", answer)
	}
	if len(script.requests) != 1 {
		t.Errorf("stop-loss did not stop the loop: %d LLM calls", len(script.requests))
	}
}

func TestHandle_RoundLimit(t *testing.T) {
	// The model asks for a tool every round, forever.
	loop := toolCall("list_products", `{}`, 10)
	script := &scriptedLLM{responses: []string{loop, loop}}
	a, _ := newTestAgent(t, script, &fakeAPI{}, Options{MaxRounds: 2})

	answer, err := a.Handle(context.Background(), 7, "зациклись", "")
	if err != nil {
		t.Fatal(err)
	}
	if answer != msgTooManyloops {
		t.Errorf("answer = %q, want round-limit message", answer)
	}
}

func TestHandle_PhotoWithoutVision(t *testing.T) {
	script := &scriptedLLM{}
	a, _ := newTestAgent(t, script, &fakeAPI{}, Options{})

	answer, err := a.Handle(context.Background(), 7, "что на фото?", "data:image/jpeg;base64,AAAA")
	if err != nil {
		t.Fatal(err)
	}
	if answer != msgNoVision {
		t.Errorf("answer = %q, want no-vision message", answer)
	}
	if len(script.requests) != 0 {
		t.Error("photo request reached a text-only LLM")
	}
}
