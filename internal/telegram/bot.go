// Package telegram wires the go-telegram/bot library into the chic bot. The
// library owns transport (Bot API calls, webhook receiving, secret-token
// verification, worker pool); this package owns policy: the user allowlist,
// update deduplication and the Handler seam the LLM agent will fill.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Reply is what a Handler produces: a final answer (Options == nil) or a
// clarifying question (Options non-empty) the bot renders as an inline
// keyboard. AllowCustom adds a free-text "свой вариант" button.
type Reply struct {
	Text        string
	Options     []string
	AllowCustom bool
}

// Handler processes one allowed message and returns a Reply. The bot owns
// allowlist checks and delivery, so a Handler is pure message-in/reply-out —
// the echo handler today, the LLM agent later.
type Handler interface {
	Handle(ctx context.Context, msg *models.Message) (Reply, error)
}

// HandlerFunc adapts a function to the Handler interface.
type HandlerFunc func(ctx context.Context, msg *models.Message) (Reply, error)

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, msg *models.Message) (Reply, error) {
	return f(ctx, msg)
}

// Echo replies with the incoming text verbatim — the skeleton handler that
// proves the receive→process→reply loop before any AI is attached.
func Echo() Handler {
	return HandlerFunc(func(_ context.Context, msg *models.Message) (Reply, error) {
		if len(msg.Photo) > 0 {
			return Reply{Text: "картинки будут позже"}, nil
		}
		return Reply{Text: msg.Text}, nil
	})
}

// The "new session" affordance: a /new command in the bot menu and an inline
// button under every answer, both resetting the agent's dialog memory so the
// next question starts from a clean slate.
const (
	callbackNewSession = "new_session"
	// MsgSessionReset confirms a session reset; exported so the /new command
	// handler in main answers with the same wording as the button.
	MsgSessionReset = "🆕 Начали заново — прошлый диалог больше не учитывается."
	msgResetFailed  = "Не получилось сбросить контекст, попробуйте ещё раз."

	// Clarifying-question buttons. callback_data is capped at 64 bytes, so an
	// option button carries only its index (ask:0, ask:1, …) and the label is
	// recovered from the button itself on callback. ask:custom is the
	// "свой вариант" button — the free-text answer arrives as the next message.
	callbackAskPrefix = "ask:"
	callbackAskCustom = "ask:custom"
	msgAskCustom      = "Напишите свой вариант ответом 👇"
)

var newSessionKeyboard = &models.InlineKeyboardMarkup{
	InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "🆕 Новый диалог", CallbackData: callbackNewSession}},
	},
}

// Bot is the go-telegram/bot instance plus chic's policy layer. It answers
// rejected users with a fixed refusal so the bot doesn't look dead to a
// stranger (there are only two allowed users).
type Bot struct {
	api          *bot.Bot
	secret       string
	allowed      map[int64]struct{}
	handler      Handler
	onNewSession func(ctx context.Context, chatID int64) error
	seen         *dedupe
	logger       *slog.Logger
}

// New builds the bot. It calls getMe under the hood, so a bad token fails
// here, before anything is served. allowed maps Telegram user IDs permitted
// to talk to the bot; workers bounds concurrent update processing. extra
// options are appended last (tests inject WithServerURL/WithSkipGetMe).
func New(token, webhookSecret string, allowed map[int64]struct{}, handler Handler, workers int, logger *slog.Logger, extra ...bot.Option) (*Bot, error) {
	if workers <= 0 {
		workers = 4
	}
	if logger == nil {
		logger = slog.Default()
	}
	b := &Bot{
		secret:  webhookSecret,
		allowed: allowed,
		handler: handler,
		seen:    newDedupe(1024),
		logger:  logger,
	}
	opts := append([]bot.Option{
		bot.WithDefaultHandler(b.onUpdate),
		bot.WithWebhookSecretToken(webhookSecret),
		// A fixed pool of synchronous workers, not a goroutine per update —
		// keeps concurrency bounded like the pre-library worker pool did.
		bot.WithWorkers(workers),
		bot.WithNotAsyncHandlers(),
		bot.WithErrorsHandler(func(err error) { logger.Error("telegram library", "err", err) }),
		bot.WithDebugHandler(func(format string, args ...any) { logger.Debug(fmt.Sprintf(format, args...)) }),
	}, extra...)

	api, err := bot.New(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	b.api = api
	return b, nil
}

// API exposes the underlying library client for calls this package doesn't
// wrap (SendPhoto, SendDocument, inline keyboards, ...).
func (b *Bot) API() *bot.Bot { return b.api }

// OnNewSession installs the reset hook the "new dialog" button and /new
// command trigger. Must be called before StartWebhook.
func (b *Bot) OnNewSession(f func(ctx context.Context, chatID int64) error) { b.onNewSession = f }

// Me returns the bot's own account (startup logging).
func (b *Bot) Me(ctx context.Context) (*models.User, error) {
	return b.api.GetMe(ctx)
}

// WebhookHandler is the HTTP endpoint for Telegram deliveries. The library
// verifies the secret token and queues updates; StartWebhook consumes them.
func (b *Bot) WebhookHandler() http.Handler { return b.api.WebhookHandler() }

// StartWebhook runs the worker pool until ctx is cancelled.
func (b *Bot) StartWebhook(ctx context.Context) { b.api.StartWebhook(ctx) }

// RegisterWebhook points Telegram at url, subscribing to messages and
// inline-button presses. Telegram echoes the secret token back on every
// delivery, which the webhook handler verifies. It also (re)publishes the
// bot's command menu. Re-registering the same URL is idempotent.
func (b *Bot) RegisterWebhook(ctx context.Context, url string) error {
	if _, err := b.api.SetWebhook(ctx, &bot.SetWebhookParams{
		URL:            url,
		SecretToken:    b.secret,
		AllowedUpdates: []string{"message", "callback_query"},
	}); err != nil {
		return err
	}
	_, err := b.api.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: []models.BotCommand{
			{Command: "new", Description: "Новый диалог — забыть контекст"},
		},
	})
	return err
}

// onUpdate handles one update end to end; errors are logged, never fatal.
func (b *Bot) onUpdate(ctx context.Context, _ *bot.Bot, u *models.Update) {
	// Telegram re-delivers an update if it doubts our 200 reached it, and
	// processing one twice would double-reply. The library doesn't dedupe.
	if !b.seen.firstSeen(u.ID) {
		b.logger.Debug("duplicate update dropped", "update_id", u.ID)
		return
	}

	if u.CallbackQuery != nil {
		b.onCallback(ctx, u.CallbackQuery)
		return
	}

	msg := u.Message
	if msg == nil || msg.From == nil {
		return // not an update we subscribed to, but be safe
	}
	log := b.logger.With("update_id", u.ID, "user_id", msg.From.ID, "chat_id", msg.Chat.ID)

	if _, ok := b.allowed[msg.From.ID]; !ok {
		log.Warn("message from user not in allowlist")
		b.reply(ctx, log, msg.Chat.ID, "Извините, этот бот приватный.", false)
		return
	}

	b.handleMessage(ctx, log, msg)
}

// handleMessage runs one allowed message through the handler and delivers the
// result: a plain answer, or a clarifying question as an inline keyboard. It is
// the shared path for real messages and for resuming after an inline choice,
// so both get the typing indicator and question rendering.
func (b *Bot) handleMessage(ctx context.Context, log *slog.Logger, msg *models.Message) {
	// Agent answers take seconds to minutes; keep the "typing…" indicator
	// alive (Telegram drops it after ~5s) until the handler returns.
	stopTyping := b.startTyping(ctx, msg.Chat.ID)
	rep, err := b.handler.Handle(ctx, msg)
	stopTyping()
	if err != nil {
		log.Error("handler failed", "err", err)
		b.reply(ctx, log, msg.Chat.ID, "Что-то пошло не так, попробуйте ещё раз.", false)
		return
	}
	if len(rep.Options) > 0 {
		b.askQuestion(ctx, log, msg.Chat.ID, rep)
		return
	}
	if rep.Text == "" {
		return
	}
	b.reply(ctx, log, msg.Chat.ID, rep.Text, true)
}

// onCallback handles an inline-button press. The only button today is
// "new dialog", which resets the agent's memory for the chat.
func (b *Bot) onCallback(ctx context.Context, q *models.CallbackQuery) {
	log := b.logger.With("callback_id", q.ID, "user_id", q.From.ID)
	// Ack up front, before any handler work — an ask: resume runs the full
	// agent loop (seconds to minutes), and Telegram expires the callback (and
	// spins the button) if the ack waits until after that. Acking on every
	// path, including the ignore ones, is what keeps the client from spinning.
	if _, err := b.api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID}); err != nil {
		log.Debug("answerCallbackQuery failed", "err", err)
	}

	if _, ok := b.allowed[q.From.ID]; !ok {
		log.Warn("callback from user not in allowlist")
		return
	}
	// Message can be inaccessible when the button is older than 48h — then
	// there is no chat to answer in, so drop the press.
	if q.Message.Message == nil {
		return
	}
	chatID := q.Message.Message.Chat.ID

	switch {
	case strings.HasPrefix(q.Data, callbackAskPrefix):
		b.onAskAnswer(ctx, log, q)
	case q.Data == callbackNewSession && b.onNewSession != nil:
		if err := b.onNewSession(ctx, chatID); err != nil {
			log.Error("new session reset failed", "err", err)
			b.reply(ctx, log, chatID, msgResetFailed, false)
			return
		}
		log.Info("new session started", "chat_id", chatID)
		b.reply(ctx, log, chatID, MsgSessionReset, false)
	}
}

// onAskAnswer resumes the dialog after the user taps an inline option under a
// clarifying question. The chosen label is read back from the pressed button
// (callback_data only carries the index), the keyboard is removed so the
// question can't be answered twice, and the choice is fed through the normal
// message path as the next user turn — the agent resumes from history.
func (b *Bot) onAskAnswer(ctx context.Context, log *slog.Logger, q *models.CallbackQuery) {
	msg := q.Message.Message
	chatID := msg.Chat.ID

	if q.Data == callbackAskCustom {
		// The answer is still to come as free text; just drop the keyboard and
		// nudge. The agent already has the question in history.
		b.clearKeyboard(ctx, log, chatID, msg.ID)
		b.reply(ctx, log, chatID, msgAskCustom, false)
		return
	}

	label := buttonLabel(msg.ReplyMarkup, q.Data)
	if label == "" {
		log.Warn("ask callback with no matching button", "data", q.Data)
		b.clearKeyboard(ctx, log, chatID, msg.ID)
		return
	}
	// Reflect the choice in the question message (and drop the keyboard) so the
	// chat shows what was picked, then route the choice through the same path
	// as a real message — typing indicator, rendering and any follow-up
	// question all just work.
	b.markAnswered(ctx, log, chatID, msg.ID, msg.Text, label)
	b.handleMessage(ctx, log, &models.Message{
		ID:   msg.ID,
		From: &q.From,
		Chat: msg.Chat,
		Text: label,
	})
}

// markAnswered rewrites an answered question to show the chosen option under it
// and removes the keyboard in the same edit (editMessageText without a markup
// drops it). Plain text, no parse mode: the question came back from Telegram as
// plain text, so re-sending it can't reject on entities. Best-effort — a
// failure just leaves the original question and its (now stale) buttons.
func (b *Bot) markAnswered(ctx context.Context, log *slog.Logger, chatID int64, msgID int, question, choice string) {
	text := strings.TrimSpace(question)
	if text != "" {
		text += "\n\n"
	}
	text += "✅ " + choice
	if _, err := b.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: msgID,
		Text:      text,
	}); err != nil {
		log.Debug("mark answered failed", "err", err)
		b.clearKeyboard(ctx, log, chatID, msgID)
	}
}

// askQuestion sends a clarifying question as an inline keyboard: one button per
// option (callback ask:<idx>), plus an optional "свой вариант" button. The
// labels live in the buttons, so the choice is recovered from the callback's
// own message — no server-side pending-question state.
func (b *Bot) askQuestion(ctx context.Context, log *slog.Logger, chatID int64, rep Reply) {
	rows := make([][]models.InlineKeyboardButton, 0, len(rep.Options)+1)
	for i, opt := range rep.Options {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: opt, CallbackData: fmt.Sprintf("%s%d", callbackAskPrefix, i)},
		})
	}
	if rep.AllowCustom {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "✏️ Свой вариант", CallbackData: callbackAskCustom},
		})
	}
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: rows}

	// Question text can exceed the message limit like any answer; hang the
	// keyboard under the last chunk.
	chunks := splitHTML(renderTelegramHTML(rep.Text), maxMessageLen)
	for i, chunk := range chunks {
		var m models.ReplyMarkup
		if i == len(chunks)-1 {
			m = markup
		}
		if _, err := b.api.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        chunk,
			ParseMode:   models.ParseModeHTML,
			ReplyMarkup: m,
		}); err != nil {
			log.Error("sendMessage question failed", "err", err)
			return
		}
	}
}

// clearKeyboard removes the inline keyboard from a message (its question is now
// answered). Best-effort: a failure only leaves stale buttons behind.
func (b *Bot) clearKeyboard(ctx context.Context, log *slog.Logger, chatID int64, msgID int) {
	if _, err := b.api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:    chatID,
		MessageID: msgID,
	}); err != nil {
		log.Debug("clear keyboard failed", "err", err)
	}
}

// buttonLabel returns the text of the inline button whose callback_data matches.
// callback_data only carries the option index (the 64-byte cap rules out the
// label), so the human-readable choice is recovered from the tapped keyboard.
func buttonLabel(markup *models.InlineKeyboardMarkup, data string) string {
	if markup == nil {
		return ""
	}
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == data {
				return btn.Text
			}
		}
	}
	return ""
}

// maxMessageLen is Telegram's hard cap on sendMessage text.
const maxMessageLen = 4096

// reply delivers text as Telegram-HTML. The agent answers in Markdown, which
// renderTelegramHTML turns into balanced, whitelisted Telegram-HTML (the
// renderer is what guarantees it parses); the result is split into limit-sized
// chunks with tags balanced per chunk, and if Telegram still rejects a chunk
// with 400 it is resent as plain text — a degraded answer beats a swallowed
// one. withKeyboard hangs the "new dialog" button under the last chunk —
// answers get it, service notices don't.
func (b *Bot) reply(ctx context.Context, log *slog.Logger, chatID int64, text string, withKeyboard bool) {
	chunks := splitHTML(renderTelegramHTML(text), maxMessageLen)
	for i, chunk := range chunks {
		var markup models.ReplyMarkup
		if withKeyboard && i == len(chunks)-1 {
			markup = newSessionKeyboard
		}
		_, err := b.api.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        chunk,
			ParseMode:   models.ParseModeHTML,
			ReplyMarkup: markup,
		})
		if err != nil && errors.Is(err, bot.ErrorBadRequest) {
			log.Warn("HTML reply rejected, resending as plain text", "err", err)
			_, err = b.api.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:      chatID,
				Text:        stripTags(chunk),
				ReplyMarkup: markup,
			})
		}
		if err != nil {
			log.Error("sendMessage failed", "err", err)
			return
		}
	}
}

// splitMessage cuts text into <=limit-rune chunks, preferring to break on the
// last newline of a window so lists and paragraphs survive the split.
func splitMessage(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var out []string
	for len(runes) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimRight(string(runes[:cut]), "\n"))
		runes = runes[cut:]
	}
	if rest := strings.TrimRight(string(runes), "\n"); rest != "" {
		out = append(out, rest)
	}
	return out
}

// startTyping shows the typing indicator now and re-sends it every few
// seconds until the returned stop function is called.
func (b *Bot) startTyping(ctx context.Context, chatID int64) (stop func()) {
	send := func() {
		if _, err := b.api.SendChatAction(ctx, &bot.SendChatActionParams{
			ChatID: chatID,
			Action: models.ChatActionTyping,
		}); err != nil {
			b.logger.Debug("sendChatAction failed", "err", err)
		}
	}
	send()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				send()
			}
		}
	}()
	return func() { close(done) }
}

// ParseAllowedIDs parses the comma-separated ALLOWED_USER_IDS env value
// ("123,456") into an allowlist set.
func ParseAllowedIDs(s string) (map[int64]struct{}, error) {
	out := make(map[int64]struct{})
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("allowed user id %q: %w", part, err)
		}
		out[id] = struct{}{}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no user ids in %q", s)
	}
	return out, nil
}
