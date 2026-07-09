package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func postUpdate(wh *Webhook, secret string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/tg/path-secret", strings.NewReader(body))
	if secret != "" {
		req.Header.Set(secretTokenHeader, secret)
	}
	rec := httptest.NewRecorder()
	wh.ServeHTTP(rec, req)
	return rec
}

func TestWebhookRejectsBadSecret(t *testing.T) {
	wh := NewWebhook("right", 8, slog.Default())

	if rec := postUpdate(wh, "wrong", `{"update_id":1}`); rec.Code != 401 {
		t.Errorf("wrong secret: code = %d, want 401", rec.Code)
	}
	if rec := postUpdate(wh, "", `{"update_id":1}`); rec.Code != 401 {
		t.Errorf("missing secret: code = %d, want 401", rec.Code)
	}
	if len(wh.Updates()) != 0 {
		t.Error("rejected update must not be enqueued")
	}
}

func TestWebhookAcceptsAndEnqueues(t *testing.T) {
	wh := NewWebhook("s", 8, slog.Default())

	rec := postUpdate(wh, "s", `{"update_id":7,"message":{"message_id":1,"chat":{"id":10},"text":"hi","from":{"id":123}}}`)
	if rec.Code != 200 {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	select {
	case u := <-wh.Updates():
		if u.UpdateID != 7 || u.Message.Text != "hi" {
			t.Errorf("update = %+v", u)
		}
	default:
		t.Fatal("update not enqueued")
	}
}

func TestWebhookDedupes(t *testing.T) {
	wh := NewWebhook("s", 8, slog.Default())

	for range 3 { // Telegram re-delivery: same update_id three times
		if rec := postUpdate(wh, "s", `{"update_id":42}`); rec.Code != 200 {
			t.Fatalf("code = %d, want 200 (duplicates still get 200)", rec.Code)
		}
	}
	if n := len(wh.Updates()); n != 1 {
		t.Errorf("enqueued %d copies, want 1", n)
	}
}

func TestWebhookFullQueueStill200(t *testing.T) {
	wh := NewWebhook("s", 1, slog.Default())

	for i := 1; i <= 3; i++ {
		body := fmt.Sprintf(`{"update_id":%d}`, i)
		if rec := postUpdate(wh, "s", body); rec.Code != 200 {
			t.Errorf("update %d: code = %d, want 200 even when queue is full", i, rec.Code)
		}
	}
}

func TestWebhookMalformedBodyStill200(t *testing.T) {
	wh := NewWebhook("s", 8, slog.Default())
	if rec := postUpdate(wh, "s", `{not json`); rec.Code != 200 {
		t.Errorf("code = %d, want 200 (retry would not help)", rec.Code)
	}
}

func TestDedupeEvictsOldest(t *testing.T) {
	d := newDedupe(2)
	if !d.firstSeen(1) || !d.firstSeen(2) {
		t.Fatal("fresh ids must be first-seen")
	}
	if d.firstSeen(1) {
		t.Error("1 still in window, must be duplicate")
	}
	if !d.firstSeen(3) { // evicts 1
		t.Fatal("3 is fresh")
	}
	if !d.firstSeen(1) {
		t.Error("1 was evicted, must be first-seen again")
	}
}

// waitFor polls cond until it returns true or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestBotEchoFlow drives webhook → worker pool → handler → sendMessage end to
// end against a fake Bot API: an allowed user gets an echo, a stranger gets a
// refusal.
func TestBotEchoFlow(t *testing.T) {
	f, client := newFakeAPI(t)
	wh := NewWebhook("s", 8, slog.Default())
	bot := NewBot(client, map[int64]struct{}{123: {}}, Echo(), 2, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { bot.Run(ctx, wh.Updates()); close(done) }()

	postUpdate(wh, "s", `{"update_id":1,"message":{"message_id":1,"chat":{"id":10},"text":"ping","from":{"id":123}}}`)
	postUpdate(wh, "s", `{"update_id":2,"message":{"message_id":2,"chat":{"id":11},"text":"hack","from":{"id":999}}}`)

	waitFor(t, "both replies", func() bool { return len(f.callsTo("sendMessage")) == 2 })
	cancel()
	<-done

	byChat := map[float64]string{}
	for _, call := range f.callsTo("sendMessage") {
		byChat[call["chat_id"].(float64)] = call["text"].(string)
	}
	if byChat[10] != "ping" {
		t.Errorf("allowed user echo = %q, want %q", byChat[10], "ping")
	}
	if !strings.Contains(byChat[11], "приватный") {
		t.Errorf("stranger reply = %q, want refusal", byChat[11])
	}
}
