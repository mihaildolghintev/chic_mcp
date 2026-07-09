// Package telegram wires the go-telegram/bot library into the chic bot. The
// library owns transport (Bot API calls, webhook receiving, secret-token
// verification, worker pool); this package owns policy: the user allowlist,
// update deduplication and the Handler seam the LLM agent will fill.
package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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

// Bot is the go-telegram/bot instance plus chic's policy layer. It answers
// rejected users with a fixed refusal so the bot doesn't look dead to a
// stranger (there are only two allowed users).
type Bot struct {
	api     *bot.Bot
	secret  string
	allowed map[int64]struct{}
	handler Handler
	seen    *dedupe
	logger  *slog.Logger
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

// Me returns the bot's own account (startup logging).
func (b *Bot) Me(ctx context.Context) (*models.User, error) {
	return b.api.GetMe(ctx)
}

// WebhookHandler is the HTTP endpoint for Telegram deliveries. The library
// verifies the secret token and queues updates; StartWebhook consumes them.
func (b *Bot) WebhookHandler() http.Handler { return b.api.WebhookHandler() }

// StartWebhook runs the worker pool until ctx is cancelled.
func (b *Bot) StartWebhook(ctx context.Context) { b.api.StartWebhook(ctx) }

// RegisterWebhook points Telegram at url, subscribing to message updates
// only. Telegram echoes the secret token back on every delivery, which the
// webhook handler verifies. Re-registering the same URL is idempotent.
func (b *Bot) RegisterWebhook(ctx context.Context, url string) error {
	_, err := b.api.SetWebhook(ctx, &bot.SetWebhookParams{
		URL:            url,
		SecretToken:    b.secret,
		AllowedUpdates: []string{"message"},
	})
	return err
}

// onUpdate handles one update end to end; errors are logged, never fatal.
func (b *Bot) onUpdate(ctx context.Context, _ *bot.Bot, u *models.Update) {
	msg := u.Message
	if msg == nil || msg.From == nil {
		return // not a message update (subscribed to messages only, but be safe)
	}
	log := b.logger.With("update_id", u.ID, "user_id", msg.From.ID, "chat_id", msg.Chat.ID)

	// Telegram re-delivers an update if it doubts our 200 reached it, and
	// processing one twice would double-reply. The library doesn't dedupe.
	if !b.seen.firstSeen(u.ID) {
		log.Debug("duplicate update dropped")
		return
	}

	if _, ok := b.allowed[msg.From.ID]; !ok {
		log.Warn("message from user not in allowlist")
		b.reply(ctx, log, msg.Chat.ID, "Извините, этот бот приватный.")
		return
	}

	text, err := b.handler.Handle(ctx, msg)
	if err != nil {
		log.Error("handler failed", "err", err)
		b.reply(ctx, log, msg.Chat.ID, "Что-то пошло не так, попробуйте ещё раз.")
		return
	}
	if text == "" {
		return
	}
	b.reply(ctx, log, msg.Chat.ID, text)
}

func (b *Bot) reply(ctx context.Context, log *slog.Logger, chatID int64, text string) {
	if _, err := b.api.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text}); err != nil {
		log.Error("sendMessage failed", "err", err)
	}
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
