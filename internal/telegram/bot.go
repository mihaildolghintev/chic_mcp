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
	api          *bot.Bot
	secret       string
	allowed      map[int64]struct{}
	handler      Handler
	onNewSession func(ctx context.Context, userID int64) error
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
// command trigger. The hook resets one user's dialog memory. Must be called
// before StartWebhook.
func (b *Bot) OnNewSession(f func(ctx context.Context, userID int64) error) { b.onNewSession = f }

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

	// Agent answers take seconds to minutes; keep the "typing…" indicator
	// alive (Telegram drops it after ~5s) until the handler returns.
	stopTyping := b.startTyping(ctx, msg.Chat.ID)
	text, err := b.handler.Handle(ctx, msg)
	stopTyping()
	if err != nil {
		log.Error("handler failed", "err", err)
		b.reply(ctx, log, msg.Chat.ID, "Что-то пошло не так, попробуйте ещё раз.", false)
		return
	}
	if text == "" {
		return
	}
	b.reply(ctx, log, msg.Chat.ID, text, true)
}

// onCallback handles an inline-button press. The only button today is
// "new dialog", which resets the agent's memory for the chat.
func (b *Bot) onCallback(ctx context.Context, q *models.CallbackQuery) {
	log := b.logger.With("callback_id", q.ID, "user_id", q.From.ID)
	// Always ack, even on the ignore paths — otherwise the client spins.
	defer func() {
		if _, err := b.api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID}); err != nil {
			log.Debug("answerCallbackQuery failed", "err", err)
		}
	}()

	if _, ok := b.allowed[q.From.ID]; !ok {
		log.Warn("callback from user not in allowlist")
		return
	}
	// Message can be inaccessible when the button is older than 48h — then
	// there is no chat to answer in, so drop the press.
	if q.Data != callbackNewSession || q.Message.Message == nil || b.onNewSession == nil {
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
