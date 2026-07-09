package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPI records calls per method (thread-safe: bot workers send
// concurrently) and serves canned responses.
type fakeAPI struct {
	t  *testing.T
	mu sync.Mutex

	calls   map[string][]map[string]any // method -> decoded request bodies
	respond map[string]string           // method -> raw JSON response
}

func newFakeAPI(t *testing.T) (*fakeAPI, *Client) {
	f := &fakeAPI{t: t, calls: map[string][]map[string]any{}, respond: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/botTOKEN/")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode %s body: %v", method, err)
		}
		f.mu.Lock()
		f.calls[method] = append(f.calls[method], body)
		resp, ok := f.respond[method]
		f.mu.Unlock()
		if !ok {
			resp = `{"ok":true,"result":{}}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return f, NewClient("TOKEN", WithBaseURL(srv.URL))
}

// callsTo returns a snapshot of recorded payloads for a method.
func (f *fakeAPI) callsTo(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.calls[method]...)
}

func (f *fakeAPI) respondWith(method, raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respond[method] = raw
}

func TestGetMe(t *testing.T) {
	f, c := newFakeAPI(t)
	f.respondWith("getMe", `{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"chic","username":"chic_bot"}}`)

	u, err := c.GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if u.ID != 42 || u.Username != "chic_bot" {
		t.Errorf("GetMe = %+v, want id 42 username chic_bot", u)
	}
}

func TestSendMessagePayload(t *testing.T) {
	f, c := newFakeAPI(t)

	if err := c.SendMessage(context.Background(), 1001, "привет"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	calls := f.callsTo("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("got %d sendMessage calls, want 1", len(calls))
	}
	if calls[0]["chat_id"] != float64(1001) || calls[0]["text"] != "привет" {
		t.Errorf("sendMessage payload = %v", calls[0])
	}
}

func TestSetWebhookPayload(t *testing.T) {
	f, c := newFakeAPI(t)

	err := c.SetWebhook(context.Background(), "https://mcp.chic.md/tg/s3cret", "s3cret", []string{"message"})
	if err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	calls := f.callsTo("setWebhook")
	if len(calls) != 1 {
		t.Fatalf("got %d setWebhook calls, want 1", len(calls))
	}
	got := calls[0]
	if got["url"] != "https://mcp.chic.md/tg/s3cret" || got["secret_token"] != "s3cret" {
		t.Errorf("setWebhook payload = %v", got)
	}
}

func TestAPIError(t *testing.T) {
	f, c := newFakeAPI(t)
	f.respondWith("sendMessage", `{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`)

	err := c.SendMessage(context.Background(), 1, "x")
	if err == nil {
		t.Fatal("want error for ok=false response")
	}
	want := "telegram sendMessage: api error 403: bot was blocked by the user"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
}

func TestParseAllowedIDs(t *testing.T) {
	ids, err := ParseAllowedIDs("123, 456")
	if err != nil {
		t.Fatalf("ParseAllowedIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("got %d ids, want 2", len(ids))
	}
	if _, ok := ids[456]; !ok {
		t.Error("456 missing from allowlist")
	}

	if _, err := ParseAllowedIDs(""); err == nil {
		t.Error("want error for empty list")
	}
	if _, err := ParseAllowedIDs("abc"); err == nil {
		t.Error("want error for non-numeric id")
	}
}
