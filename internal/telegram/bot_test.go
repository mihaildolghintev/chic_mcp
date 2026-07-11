package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// fakeAPI is a stub Telegram Bot API server recording sendMessage calls.
// With rejectHTML set it answers any HTML-mode sendMessage with the 400 a
// real parse failure produces, to exercise the plain-text fallback.
type fakeAPI struct {
	srv        *httptest.Server
	rejectHTML bool

	mu         sync.Mutex
	sent       []sentMessage
	edited     int      // editMessageReplyMarkup calls (bare keyboard clears)
	editedText []string // text of each editMessageText call (answered questions)
}

type sentMessage struct {
	ChatID      int64  `json:"chat_id"`
	Text        string `json:"text"`
	ParseMode   string `json:"parse_mode"`
	ReplyMarkup string `json:"reply_markup"`
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result any = map[string]any{}
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			// The library posts params as multipart/form-data.
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("sendMessage form: %v", err)
			}
			chatID, err := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
			if err != nil {
				t.Errorf("sendMessage chat_id: %v", err)
			}
			m := sentMessage{
				ChatID:      chatID,
				Text:        r.FormValue("text"),
				ParseMode:   r.FormValue("parse_mode"),
				ReplyMarkup: r.FormValue("reply_markup"),
			}
			f.mu.Lock()
			f.sent = append(f.sent, m)
			f.mu.Unlock()
			if f.rejectHTML && m.ParseMode == "HTML" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok": false, "error_code": 400,
					"description": "Bad Request: can't parse entities",
				})
				return
			}
			result = map[string]any{"message_id": 1, "date": 0, "chat": map[string]any{"id": m.ChatID}}
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("editMessageText form: %v", err)
			}
			chatID, _ := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
			f.mu.Lock()
			f.editedText = append(f.editedText, r.FormValue("text"))
			f.mu.Unlock()
			// Return an edited Message so the library doesn't error and fall back.
			result = map[string]any{"message_id": 1, "date": 0,
				"chat": map[string]any{"id": chatID}, "text": r.FormValue("text")}
		case strings.HasSuffix(r.URL.Path, "/editMessageReplyMarkup"):
			f.mu.Lock()
			f.edited++
			f.mu.Unlock()
			result = true
		case strings.HasSuffix(r.URL.Path, "/setWebhook"),
			strings.HasSuffix(r.URL.Path, "/setMyCommands"),
			strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			result = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) sentMessages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

func (f *fakeAPI) editCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.edited
}

func (f *fakeAPI) editedTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.editedText...)
}

func newTestBot(t *testing.T, f *fakeAPI, h Handler) *Bot {
	t.Helper()
	b, err := New("42:token", "s3cret", map[int64]struct{}{100: {}}, h, 1,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		bot.WithServerURL(f.srv.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

func update(id, userID int64, text string) *models.Update {
	return &models.Update{
		ID: id,
		Message: &models.Message{
			From: &models.User{ID: userID},
			Chat: models.Chat{ID: userID},
			Text: text,
		},
	}
}

// process feeds an update through the library dispatch synchronously
// (handlers are registered with WithNotAsyncHandlers).
func process(b *Bot, u *models.Update) {
	b.API().ProcessUpdate(context.Background(), u)
}

func TestAllowedUserGetsEcho(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())

	process(b, update(1, 100, "привет"))

	sent := f.sentMessages()
	if len(sent) != 1 || sent[0].Text != "привет" || sent[0].ChatID != 100 {
		t.Fatalf("want echo to chat 100, got %+v", sent)
	}
}

// TestReplyRendersMarkdownToHTML: the agent answers in Markdown; replies go out
// with ParseMode=HTML, markup rendered to allowed tags and stray specials (and
// any literal HTML) escaped so Telegram can't reject the parse.
func TestReplyRendersMarkdownToHTML(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, HandlerFunc(func(context.Context, *models.Message) (Reply, error) {
		return Reply{Text: "**итог**: 1 < 2 <script>x</script>"}, nil
	}))

	process(b, update(1, 100, "отчёт"))

	sent := f.sentMessages()
	want := "<b>итог</b>: 1 &lt; 2 &lt;script&gt;x&lt;/script&gt;"
	if len(sent) != 1 || sent[0].Text != want || sent[0].ParseMode != "HTML" {
		t.Fatalf("want rendered HTML reply %q, got %+v", want, sent)
	}
}

// TestHTMLRejectFallsBackToPlainText: if Telegram still answers 400 to the
// HTML send, the same chunk is resent without ParseMode and without tags.
func TestHTMLRejectFallsBackToPlainText(t *testing.T) {
	f := newFakeAPI(t)
	f.rejectHTML = true
	b := newTestBot(t, f, HandlerFunc(func(context.Context, *models.Message) (Reply, error) {
		return Reply{Text: "**жирный** текст"}, nil
	}))

	process(b, update(1, 100, "отчёт"))

	sent := f.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("want HTML attempt + plain retry, got %+v", sent)
	}
	if sent[0].ParseMode != "HTML" {
		t.Errorf("first send must be HTML, got %+v", sent[0])
	}
	if sent[1].ParseMode != "" || sent[1].Text != "жирный текст" {
		t.Errorf("retry must be plain text without tags, got %+v", sent[1])
	}
}

// TestReplyCarriesNewSessionButton: handler answers get the inline "new
// dialog" button; service notices (like the refusal) don't.
func TestReplyCarriesNewSessionButton(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())

	process(b, update(1, 100, "привет"))
	process(b, update(2, 999, "пусти"))

	sent := f.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("want 2 messages, got %+v", sent)
	}
	if !strings.Contains(sent[0].ReplyMarkup, callbackNewSession) {
		t.Errorf("answer must carry the new-session button, got %+v", sent[0])
	}
	if sent[1].ReplyMarkup != "" {
		t.Errorf("refusal must not carry a keyboard, got %+v", sent[1])
	}
}

func callbackUpdate(id, userID, chatID int64, data string) *models.Update {
	return &models.Update{
		ID: id,
		CallbackQuery: &models.CallbackQuery{
			ID:   strconv.FormatInt(id, 10),
			From: models.User{ID: userID},
			Data: data,
			Message: models.MaybeInaccessibleMessage{
				Type:    models.MaybeInaccessibleMessageTypeMessage,
				Message: &models.Message{Chat: models.Chat{ID: chatID}},
			},
		},
	}
}

// TestNewSessionCallbackResetsAndConfirms: a button press from an allowed
// user fires the reset hook and answers with the confirmation.
func TestNewSessionCallbackResetsAndConfirms(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())
	var resetChat int64
	b.OnNewSession(func(_ context.Context, chatID int64) error {
		resetChat = chatID
		return nil
	})

	process(b, callbackUpdate(1, 100, 100, callbackNewSession))

	if resetChat != 100 {
		t.Errorf("reset hook got chat %d, want 100", resetChat)
	}
	sent := f.sentMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "Начали заново") {
		t.Fatalf("want reset confirmation, got %+v", sent)
	}
	if sent[0].ReplyMarkup != "" {
		t.Errorf("confirmation must not carry a keyboard, got %+v", sent[0])
	}
}

// TestNewSessionCallbackFromStrangerIgnored: allowlist applies to button
// presses too — no reset, no reply.
func TestNewSessionCallbackFromStrangerIgnored(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())
	reset := false
	b.OnNewSession(func(context.Context, int64) error { reset = true; return nil })

	process(b, callbackUpdate(1, 999, 999, callbackNewSession))

	if reset {
		t.Error("stranger's callback must not reset a session")
	}
	if sent := f.sentMessages(); len(sent) != 0 {
		t.Fatalf("want no replies, got %+v", sent)
	}
}

// askCallbackUpdate is a button press under a clarifying question: the pressed
// message carries the option keyboard so buttonLabel can recover the label.
func askCallbackUpdate(id, userID, chatID int64, data string, options []string) *models.Update {
	rows := make([][]models.InlineKeyboardButton, 0, len(options)+1)
	for i, opt := range options {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: opt, CallbackData: callbackAskPrefix + strconv.Itoa(i)},
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "✏️ Свой вариант", CallbackData: callbackAskCustom},
	})
	u := callbackUpdate(id, userID, chatID, data)
	u.CallbackQuery.Message.Message.Text = "За какой период?"
	u.CallbackQuery.Message.Message.ReplyMarkup = &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	return u
}

// TestClarifyingQuestionRendersKeyboard: a Reply with options goes out as one
// message whose inline keyboard carries an indexed button per option plus the
// "свой вариант" button — and no new-session button (it's a question).
func TestClarifyingQuestionRendersKeyboard(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, HandlerFunc(func(context.Context, *models.Message) (Reply, error) {
		return Reply{Text: "За какой период?", Options: []string{"За неделю", "За месяц"}, AllowCustom: true}, nil
	}))

	process(b, update(1, 100, "покажи продажи"))

	sent := f.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("want 1 question message, got %+v", sent)
	}
	mk := sent[0].ReplyMarkup
	for _, want := range []string{`"ask:0"`, `"ask:1"`, `"ask:custom"`, "За неделю", "За месяц", "Свой вариант"} {
		if !strings.Contains(mk, want) {
			t.Errorf("keyboard missing %q, got %s", want, mk)
		}
	}
	if strings.Contains(mk, callbackNewSession) {
		t.Errorf("question must not carry the new-session button, got %s", mk)
	}
}

// TestAskOptionCallbackResumes: tapping an option clears the keyboard and feeds
// the chosen label back through the handler as the next user turn.
func TestAskOptionCallbackResumes(t *testing.T) {
	f := newFakeAPI(t)
	// Echo the incoming text so the resumed turn's answer is the chosen label.
	b := newTestBot(t, f, HandlerFunc(func(_ context.Context, msg *models.Message) (Reply, error) {
		return Reply{Text: "выбрано: " + msg.Text}, nil
	}))

	process(b, askCallbackUpdate(1, 100, 100, "ask:1", []string{"За неделю", "За месяц"}))

	// The question message is rewritten with the choice (which also drops its
	// keyboard) — not a bare keyboard clear.
	edits := f.editedTexts()
	if len(edits) != 1 || !strings.Contains(edits[0], "За какой период?") || !strings.Contains(edits[0], "✅ За месяц") {
		t.Fatalf("want question rewritten with choice, got %+v", edits)
	}
	sent := f.sentMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "выбрано: За месяц") {
		t.Fatalf("want resume with chosen label, got %+v", sent)
	}
}

// TestAskCustomCallbackNudges: the "свой вариант" button clears the keyboard
// and asks for a written answer without invoking the agent.
func TestAskCustomCallbackNudges(t *testing.T) {
	f := newFakeAPI(t)
	called := false
	b := newTestBot(t, f, HandlerFunc(func(context.Context, *models.Message) (Reply, error) {
		called = true
		return Reply{Text: "не должно вызваться"}, nil
	}))

	process(b, askCallbackUpdate(1, 100, 100, callbackAskCustom, []string{"За неделю", "За месяц"}))

	if called {
		t.Error("custom-variant press must not invoke the handler")
	}
	if f.editCount() != 1 {
		t.Errorf("keyboard not cleared: %d edits", f.editCount())
	}
	sent := f.sentMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "Напишите свой вариант") {
		t.Fatalf("want free-text nudge, got %+v", sent)
	}
}

func TestUnknownUserGetsRefusal(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())

	process(b, update(1, 999, "пусти"))

	sent := f.sentMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "приватный") {
		t.Fatalf("want refusal, got %+v", sent)
	}
}

func TestDuplicateUpdateProcessedOnce(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())

	process(b, update(7, 100, "раз"))
	process(b, update(7, 100, "раз"))

	if sent := f.sentMessages(); len(sent) != 1 {
		t.Fatalf("want 1 reply for duplicate update, got %d", len(sent))
	}
}

func TestHandlerErrorRepliesApology(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, HandlerFunc(func(context.Context, *models.Message) (Reply, error) {
		return Reply{}, errors.New("boom")
	}))

	process(b, update(1, 100, "сломайся"))

	sent := f.sentMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "попробуйте ещё раз") {
		t.Fatalf("want apology, got %+v", sent)
	}
}

func TestEmptyReplySendsNothing(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, HandlerFunc(func(context.Context, *models.Message) (Reply, error) {
		return Reply{}, nil
	}))

	process(b, update(1, 100, "молчи"))

	if sent := f.sentMessages(); len(sent) != 0 {
		t.Fatalf("want no replies, got %+v", sent)
	}
}

func TestNonMessageUpdateIgnored(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())

	process(b, &models.Update{ID: 1})

	if sent := f.sentMessages(); len(sent) != 0 {
		t.Fatalf("want no replies, got %+v", sent)
	}
}

// TestWebhookEndToEnd drives the full path: HTTP delivery with the secret
// header → library queue → worker → reply on the fake API.
func TestWebhookEndToEnd(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { b.StartWebhook(ctx); close(done) }()

	wh := httptest.NewServer(b.WebhookHandler())
	defer wh.Close()

	body, _ := json.Marshal(update(1, 100, "через вебхук"))
	req, _ := http.NewRequest(http.MethodPost, wh.URL, strings.NewReader(string(body)))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "s3cret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webhook POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook status = %d", resp.StatusCode)
	}

	waitFor(t, func() bool { return len(f.sentMessages()) == 1 })
	if sent := f.sentMessages(); sent[0].Text != "через вебхук" {
		t.Fatalf("want echo, got %+v", sent)
	}

	// A delivery with a wrong secret must be dropped, not processed.
	req, _ = http.NewRequest(http.MethodPost, wh.URL, strings.NewReader(string(body)))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webhook POST: %v", err)
	}
	_ = resp.Body.Close()

	time.Sleep(50 * time.Millisecond)
	if sent := f.sentMessages(); len(sent) != 1 {
		t.Fatalf("bad-secret delivery must not be processed, got %+v", sent)
	}

	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

func TestRegisterWebhook(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f, Echo())

	if err := b.RegisterWebhook(context.Background(), "https://bot.chic.md/tg/s3cret"); err != nil {
		t.Fatalf("RegisterWebhook: %v", err)
	}
}

func TestParseAllowedIDs(t *testing.T) {
	got, err := ParseAllowedIDs(" 123, 456 ,")
	if err != nil {
		t.Fatalf("ParseAllowedIDs: %v", err)
	}
	if _, ok := got[123]; !ok {
		t.Error("missing 123")
	}
	if _, ok := got[456]; !ok {
		t.Error("missing 456")
	}
	if len(got) != 2 {
		t.Errorf("want 2 ids, got %d", len(got))
	}

	for _, bad := range []string{"", "abc", " , "} {
		if _, err := ParseAllowedIDs(bad); err == nil {
			t.Errorf("ParseAllowedIDs(%q): want error", bad)
		}
	}
}

func TestEchoPhotoStub(t *testing.T) {
	msg := &models.Message{Photo: []models.PhotoSize{{FileID: "x"}}}
	rep, err := Echo().Handle(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Text == "" || rep.Text == msg.Text {
		t.Fatalf("want photo stub reply, got %q", rep.Text)
	}
}
