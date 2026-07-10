package telegram

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot/models"
)

// TestMenuCommandShowsTemplates: /menu answers with the question library as an
// inline keyboard whose buttons carry quick-reply callbacks.
func TestMenuCommandShowsTemplates(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())

	process(b, update(1, 100, "/menu"))

	sent := f.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("want 1 menu message, got %+v", sent)
	}
	if !strings.Contains(sent[0].ReplyMarkup, quickReplyPrefix+"0") {
		t.Errorf("menu keyboard missing quick-reply buttons: %q", sent[0].ReplyMarkup)
	}
	// The first template's label must reach the user.
	if !strings.Contains(sent[0].ReplyMarkup, questionTemplates[0].Label) {
		t.Errorf("menu keyboard missing template label: %q", sent[0].ReplyMarkup)
	}
}

// TestStartCommandShowsMenu: /start and /help are aliases for /menu so a new
// user's first screen shows what to ask.
func TestStartCommandShowsMenu(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())

	process(b, update(1, 100, "/start"))
	process(b, update(2, 100, "/help@chic_bot"))

	if sent := f.sentMessages(); len(sent) != 2 {
		t.Fatalf("want a menu for /start and /help, got %+v", sent)
	}
}

// TestQuickReplyRunsQuestion: tapping a template button echoes the question and
// runs it through the agent handler, then answers with the new-session button.
func TestQuickReplyRunsQuestion(t *testing.T) {
	f := newFakeAPI(t)
	var gotText string
	b := newTestBot(t, f, HandlerFunc(func(_ context.Context, msg *models.Message) (string, error) {
		gotText = msg.Text
		return "ответ агента", nil
	}))

	process(b, callbackUpdate(1, 100, 100, quickReplyPrefix+"0"))

	if gotText != questionTemplates[0].Question {
		t.Errorf("handler got %q, want the template question %q", gotText, questionTemplates[0].Question)
	}
	sent := f.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("want echoed question + answer, got %+v", sent)
	}
	if !strings.Contains(sent[0].Text, questionTemplates[0].Question) {
		t.Errorf("first message must echo the question, got %q", sent[0].Text)
	}
	if sent[0].ReplyMarkup != "" {
		t.Errorf("question echo must not carry a keyboard, got %q", sent[0].ReplyMarkup)
	}
	if sent[1].Text != "ответ агента" || !strings.Contains(sent[1].ReplyMarkup, callbackNewSession) {
		t.Errorf("answer must carry the new-session button, got %+v", sent[1])
	}
}

// TestQuickReplyBadIndexIgnored: an out-of-range quick-reply index is dropped,
// not run — no message, no panic.
func TestQuickReplyBadIndexIgnored(t *testing.T) {
	f := newFakeAPI(t)
	ran := false
	b := newTestBot(t, f, HandlerFunc(func(context.Context, *models.Message) (string, error) {
		ran = true
		return "x", nil
	}))

	process(b, callbackUpdate(1, 100, 100, quickReplyPrefix+"999"))

	if ran {
		t.Error("out-of-range quick reply must not reach the handler")
	}
	if sent := f.sentMessages(); len(sent) != 0 {
		t.Fatalf("want no replies, got %+v", sent)
	}
}

// fakeMemory is an in-memory preference store for the /memory tests.
type fakeMemory struct {
	mu    sync.Mutex
	items []MemoryItem
}

func (m *fakeMemory) list(context.Context, int64) ([]MemoryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]MemoryItem(nil), m.items...), nil
}

func (m *fakeMemory) forget(_ context.Context, _ int64, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.items[:0]
	for _, it := range m.items {
		if it.Key != key {
			out = append(out, it)
		}
	}
	m.items = out
	return nil
}

// TestMemoryCommandLists: /memory renders the stored profile with a delete
// button per preference.
func TestMemoryCommandLists(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())
	mem := &fakeMemory{items: []MemoryItem{
		{Key: "language", Value: "английский"},
		{Key: "reply_style", Value: "кратко"},
	}}
	b.OnMemory(mem.list, mem.forget)

	process(b, update(1, 100, "/memory"))

	sent := f.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("want 1 memory message, got %+v", sent)
	}
	if !strings.Contains(sent[0].Text, "language") || !strings.Contains(sent[0].Text, "английский") {
		t.Errorf("memory list missing a stored preference: %q", sent[0].Text)
	}
	for _, i := range []string{"0", "1"} {
		if !strings.Contains(sent[0].ReplyMarkup, memForgetPrefix+i) {
			t.Errorf("memory keyboard missing delete button %s: %q", i, sent[0].ReplyMarkup)
		}
	}
}

// TestMemoryCommandEmpty: with nothing stored, /memory shows the empty state
// and no keyboard.
func TestMemoryCommandEmpty(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())
	b.OnMemory((&fakeMemory{}).list, (&fakeMemory{}).forget)

	process(b, update(1, 100, "/memory"))

	sent := f.sentMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "не запомнил") {
		t.Fatalf("want empty-state message, got %+v", sent)
	}
	if sent[0].ReplyMarkup != "" {
		t.Errorf("empty memory must not carry a keyboard, got %q", sent[0].ReplyMarkup)
	}
}

// TestMemoryDeleteButtonRemovesAndRerenders: pressing a delete button forgets
// the preference and edits the message in place to the shorter list.
func TestMemoryDeleteButtonRemovesAndRerenders(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())
	mem := &fakeMemory{items: []MemoryItem{
		{Key: "language", Value: "английский"},
		{Key: "reply_style", Value: "кратко"},
	}}
	b.OnMemory(mem.list, mem.forget)

	// Delete index 0 ("language").
	process(b, callbackUpdate(1, 100, 100, memForgetPrefix+"0"))

	if got := mem.items; len(got) != 1 || got[0].Key != "reply_style" {
		t.Fatalf("delete did not remove the right preference: %+v", got)
	}
	edited := f.editedMessages()
	if len(edited) != 1 {
		t.Fatalf("want the message re-rendered in place, got %+v", edited)
	}
	if strings.Contains(edited[0].Text, "language") {
		t.Errorf("re-rendered list still shows the deleted key: %q", edited[0].Text)
	}
	if !strings.Contains(edited[0].Text, "reply_style") {
		t.Errorf("re-rendered list dropped the surviving key: %q", edited[0].Text)
	}
}

// TestMemoryDeleteLastEmpties: deleting the final preference edits to the
// empty-state with no keyboard.
func TestMemoryDeleteLastEmpties(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())
	mem := &fakeMemory{items: []MemoryItem{{Key: "language", Value: "en"}}}
	b.OnMemory(mem.list, mem.forget)

	process(b, callbackUpdate(1, 100, 100, memForgetPrefix+"0"))

	edited := f.editedMessages()
	if len(edited) != 1 || !strings.Contains(edited[0].Text, "не запомнил") {
		t.Fatalf("want empty-state after deleting the last item, got %+v", edited)
	}
	if edited[0].ReplyMarkup != "" && edited[0].ReplyMarkup != "{}" {
		t.Errorf("empty memory must not carry a keyboard, got %q", edited[0].ReplyMarkup)
	}
}

// TestMemoryCommandFromStrangerIgnored: /memory obeys the allowlist.
func TestMemoryCommandFromStrangerIgnored(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())
	listed := false
	b.OnMemory(func(context.Context, int64) ([]MemoryItem, error) {
		listed = true
		return nil, nil
	}, func(context.Context, int64, string) error { return nil })

	process(b, update(1, 999, "/memory"))

	if listed {
		t.Error("stranger's /memory must not read preferences")
	}
	sent := f.sentMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "приватный") {
		t.Fatalf("want refusal, got %+v", sent)
	}
}

func TestParseCommand(t *testing.T) {
	cases := map[string]string{
		"/menu":            "/menu",
		"  /memory  ":      "/memory",
		"/help@chic_bot":   "/help",
		"/new extra args":  "/new",
		"just text":        "",
		"":                 "",
		"/x@bot rest here": "/x",
	}
	for in, want := range cases {
		if got := parseCommand(in); got != want {
			t.Errorf("parseCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIndexFromCallback covers the shared prefix+index decoder.
func TestIndexFromCallback(t *testing.T) {
	if idx, ok := indexFromCallback(memForgetPrefix+"3", memForgetPrefix); !ok || idx != 3 {
		t.Errorf("got (%d,%v), want (3,true)", idx, ok)
	}
	for _, bad := range []string{"ask:1", memForgetPrefix + "x", memForgetPrefix + "-2", "3"} {
		if _, ok := indexFromCallback(bad, memForgetPrefix); ok {
			t.Errorf("indexFromCallback(%q) accepted a bad value", bad)
		}
	}
	// Sanity: the strconv path is exercised for larger indices too.
	if idx, _ := indexFromCallback(quickReplyPrefix+strconv.Itoa(12), quickReplyPrefix); idx != 12 {
		t.Errorf("index 12 not decoded")
	}
}
