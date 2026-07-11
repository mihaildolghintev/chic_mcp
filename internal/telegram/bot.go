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
	"go.opentelemetry.io/otel/attribute"

	"mcp.chic.md/internal/tracing"
)

// Handler processes one allowed message and returns the reply text. The bot
// owns allowlist checks and reply delivery, so a Handler is pure
// message-in/text-out — the echo handler today, the LLM agent later.
type Handler interface {
	Handle(ctx context.Context, msg *models.Message) (string, error)
}

// HandlerFunc adapts a function to the Handler interface.
type HandlerFunc func(ctx context.Context, msg *models.Message) (string, error)

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, msg *models.Message) (string, error) {
	return f(ctx, msg)
}

// Echo replies with the incoming text verbatim — the skeleton handler that
// proves the receive→process→reply loop before any AI is attached.
func Echo() Handler {
	return HandlerFunc(func(_ context.Context, msg *models.Message) (string, error) {
		if len(msg.Photo) > 0 {
			return "картинки будут позже", nil
		}
		return msg.Text, nil
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
	api            *bot.Bot
	secret         string
	allowed        map[int64]struct{}
	handler        Handler
	onNewSession   func(ctx context.Context, userID int64) error
	onMemoryList   func(ctx context.Context, userID int64) ([]MemoryItem, error)
	onMemoryForget func(ctx context.Context, userID int64, key string) error
	seen           *dedupe
	logger         *slog.Logger
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
// command trigger. The hook resets one user's dialog memory. Must be called
// before StartWebhook.
func (b *Bot) OnNewSession(f func(ctx context.Context, userID int64) error) { b.onNewSession = f }

// OnMemory installs the read/delete hooks the /memory command uses: list a
// user's durable preferences and forget one by key. Both must be set for the
// command to work; without them /memory is silently inert. Must be called
// before StartWebhook.
func (b *Bot) OnMemory(
	list func(ctx context.Context, userID int64) ([]MemoryItem, error),
	forget func(ctx context.Context, userID int64, key string) error,
) {
	b.onMemoryList = list
	b.onMemoryForget = forget
}

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
			{Command: "menu", Description: "Готовые вопросы"},
			{Command: "memory", Description: "Что бот о вас помнит"},
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

	// Bot-owned commands (menu, memory) are answered here with their own
	// keyboards, before the message reaches the agent handler. Commands the bot
	// doesn't own (like /new) fall through to the handler unchanged.
	if cmd := parseCommand(msg.Text); cmd != "" && b.handleCommand(ctx, log, msg, cmd) {
		return
	}

	// Root span for the whole delivery: it parents the photo download, the
	// worker-pool hop and the agent's own spans, so a trace covers Telegram→agent
	// end to end instead of starting at the LLM. No-op when tracing is disabled.
	ctx, span := tracing.Tracer().Start(ctx, "telegram.message")
	defer span.End()
	span.SetAttributes(
		tracing.SpanKind(tracing.SpanKindChain),
		attribute.Int64("update_id", u.ID),
		attribute.Int64("user_id", msg.From.ID),
		attribute.Int64("chat_id", msg.Chat.ID),
		attribute.Bool("has_photo", len(msg.Photo) > 0),
	)

	// Agent answers take seconds to minutes; keep the "typing…" indicator
	// alive (Telegram drops it after ~5s) until the handler returns.
	stopTyping := b.startTyping(ctx, msg.Chat.ID)
	text, err := b.handler.Handle(ctx, msg)
	stopTyping()
	if err != nil {
		span.RecordError(err)
		log.Error("handler failed", "err", err)
		b.reply(ctx, log, msg.Chat.ID, "Что-то пошло не так, попробуйте ещё раз.", false)
		return
	}
	if text == "" {
		return
	}
	b.reply(ctx, log, msg.Chat.ID, text, true)
}

// parseCommand extracts a leading "/command" token from message text, stripping
// any "@botname" suffix and arguments. Returns "" when the text isn't a command.
func parseCommand(text string) string {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "/") {
		return ""
	}
	if i := strings.IndexAny(t, " \t\n"); i >= 0 {
		t = t[:i]
	}
	if i := strings.IndexByte(t, '@'); i >= 0 {
		t = t[:i]
	}
	return t
}

// handleCommand answers the bot-owned commands and reports whether it did. The
// menu commands render an inline keyboard the plain message pipe can't attach,
// so they are served here rather than through the agent handler. Anything the
// bot doesn't own returns false to fall through to the handler.
func (b *Bot) handleCommand(ctx context.Context, log *slog.Logger, msg *models.Message, cmd string) bool {
	switch cmd {
	case "/menu", "/start", "/help":
		b.sendRaw(ctx, log, msg.Chat.ID, msgMenuPrompt, menuKeyboard())
		return true
	case "/memory":
		b.showMemory(ctx, log, msg.From.ID, msg.Chat.ID)
		return true
	default:
		return false
	}
}

// showMemory answers /memory with the user's durable profile and its delete
// buttons. With no memory hooks wired it does nothing (the command is inert).
func (b *Bot) showMemory(ctx context.Context, log *slog.Logger, userID, chatID int64) {
	if b.onMemoryList == nil {
		return
	}
	items, err := b.onMemoryList(ctx, userID)
	if err != nil {
		log.Error("memory list failed", "err", err)
		b.reply(ctx, log, chatID, "Не удалось прочитать память, попробуйте позже.", false)
		return
	}
	text, markup := renderMemory(items)
	b.sendRaw(ctx, log, chatID, text, asReplyMarkup(markup))
}

// editMemory re-renders the /memory message in place after a deletion.
func (b *Bot) editMemory(ctx context.Context, log *slog.Logger, q *models.CallbackQuery, items []MemoryItem) {
	text, markup := renderMemory(items)
	_, err := b.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      q.Message.Message.Chat.ID,
		MessageID:   q.Message.Message.ID,
		Text:        text,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: asReplyMarkup(markup),
	})
	if err != nil {
		log.Debug("edit memory message failed", "err", err)
	}
}

// asReplyMarkup boxes a possibly-nil keyboard into the ReplyMarkup interface
// while preserving nil-ness — a typed nil pointer in the interface would
// serialize as an empty markup instead of being omitted.
func asReplyMarkup(m *models.InlineKeyboardMarkup) models.ReplyMarkup {
	if m == nil {
		return nil
	}
	return m
}

// sendRaw sends already-rendered Telegram-HTML (menu prompt, memory list) with
// an optional keyboard. Unlike reply, it does not run the text through the
// Markdown renderer — the caller owns the markup and escaping. A 400 parse
// error falls back to a tag-stripped plain-text send.
func (b *Bot) sendRaw(ctx context.Context, log *slog.Logger, chatID int64, htmlText string, markup models.ReplyMarkup) {
	_, err := b.api.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        htmlText,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: markup,
	})
	if err != nil && errors.Is(err, bot.ErrorBadRequest) {
		log.Warn("HTML message rejected, resending as plain text", "err", err)
		_, err = b.api.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        stripTags(htmlText),
			ReplyMarkup: markup,
		})
	}
	if err != nil {
		log.Error("sendMessage failed", "err", err)
	}
}

// onCallback handles an inline-button press and routes it by callback data:
// the "new dialog" reset, a quick-reply question, or a memory-delete button.
func (b *Bot) onCallback(ctx context.Context, q *models.CallbackQuery) {
	log := b.logger.With("callback_id", q.ID, "user_id", q.From.ID)
	// Ack up front, not on return: a quick-reply press runs the agent for
	// seconds to minutes, and Telegram rejects an answerCallbackQuery that
	// arrives after its ~15s window — leaving the button spinner stuck. Acking
	// first clears the spinner immediately and covers every path below.
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

	switch {
	case q.Data == callbackNewSession:
		b.doNewSession(ctx, log, q)
	case strings.HasPrefix(q.Data, quickReplyPrefix):
		b.doQuickReply(ctx, log, q)
	case strings.HasPrefix(q.Data, memForgetPrefix):
		b.doMemoryForget(ctx, log, q)
	}
}

// doNewSession resets the agent's dialog memory for the chat behind q.
func (b *Bot) doNewSession(ctx context.Context, log *slog.Logger, q *models.CallbackQuery) {
	if b.onNewSession == nil {
		return
	}
	chatID := q.Message.Message.Chat.ID
	userID := q.From.ID
	if err := b.onNewSession(ctx, userID); err != nil {
		log.Error("new session reset failed", "err", err)
		b.reply(ctx, log, chatID, msgResetFailed, false)
		return
	}
	log.Info("new session started", "user_id", userID)
	b.reply(ctx, log, chatID, MsgSessionReset, false)
}

// doQuickReply runs the tapped template question through the agent exactly as
// if the user had typed it: it echoes the question so the chat reads as a
// dialog, then answers with the usual "new dialog" button attached.
func (b *Bot) doQuickReply(ctx context.Context, log *slog.Logger, q *models.CallbackQuery) {
	question, ok := templateByCallback(q.Data)
	if !ok || b.handler == nil {
		return
	}
	chatID := q.Message.Message.Chat.ID
	b.reply(ctx, log, chatID, "❓ "+question, false)

	ctx, span := tracing.Tracer().Start(ctx, "telegram.quick_reply")
	defer span.End()
	span.SetAttributes(
		tracing.SpanKind(tracing.SpanKindChain),
		attribute.Int64("user_id", q.From.ID),
		attribute.Int64("chat_id", chatID),
	)

	synthetic := &models.Message{From: &q.From, Chat: q.Message.Message.Chat, Text: question}
	stopTyping := b.startTyping(ctx, chatID)
	text, err := b.handler.Handle(ctx, synthetic)
	stopTyping()
	if err != nil {
		log.Error("quick-reply handler failed", "err", err)
		b.reply(ctx, log, chatID, "Что-то пошло не так, попробуйте ещё раз.", false)
		return
	}
	if text == "" {
		return
	}
	b.reply(ctx, log, chatID, text, true)
}

// doMemoryForget deletes the preference behind a delete button, then re-renders
// the /memory message in place. The index is resolved against a fresh read: an
// out-of-range stale index just re-renders instead of erroring. (In this private
// 1-on-1 bot the profile only changes on the same user's actions, so an
// in-range index racing a concurrent edit is not a practical concern.)
func (b *Bot) doMemoryForget(ctx context.Context, log *slog.Logger, q *models.CallbackQuery) {
	if b.onMemoryList == nil || b.onMemoryForget == nil {
		return
	}
	idx, ok := indexFromCallback(q.Data, memForgetPrefix)
	if !ok {
		return
	}
	userID := q.From.ID
	items, err := b.onMemoryList(ctx, userID)
	if err != nil {
		log.Error("memory list failed", "err", err)
		return
	}
	if idx < len(items) {
		if err := b.onMemoryForget(ctx, userID, items[idx].Key); err != nil {
			log.Error("memory forget failed", "err", err)
			return
		}
		log.Info("preference deleted via /memory", "key", items[idx].Key)
		if items, err = b.onMemoryList(ctx, userID); err != nil {
			log.Error("memory re-list failed", "err", err)
			return
		}
	}
	b.editMemory(ctx, log, q, items)
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
