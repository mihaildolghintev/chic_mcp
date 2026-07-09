package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
)

// Handler processes one allowed message and returns the reply text. The
// worker pool owns allowlist checks and reply delivery, so a Handler is pure
// message-in/text-out — the echo handler today, the LLM agent later.
type Handler interface {
	Handle(ctx context.Context, msg *Message) (string, error)
}

// HandlerFunc adapts a function to the Handler interface.
type HandlerFunc func(ctx context.Context, msg *Message) (string, error)

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, msg *Message) (string, error) {
	return f(ctx, msg)
}

// Echo replies with the incoming text verbatim — the skeleton handler that
// proves the receive→process→reply loop before any AI is attached.
func Echo() Handler {
	return HandlerFunc(func(_ context.Context, msg *Message) (string, error) {
		if len(msg.Photo) > 0 {
			return "картинки будут позже", nil
		}
		return msg.Text, nil
	})
}

// Bot consumes webhook updates with a pool of workers: allowlist check, then
// Handler, then reply. It answers rejected users with a fixed refusal so the
// bot doesn't look dead to a stranger (there are only two allowed users).
type Bot struct {
	client  *Client
	allowed map[int64]struct{}
	handler Handler
	workers int
	logger  *slog.Logger
}

// NewBot wires a worker pool over the client. allowed maps Telegram user IDs
// permitted to talk to the bot.
func NewBot(client *Client, allowed map[int64]struct{}, handler Handler, workers int, logger *slog.Logger) *Bot {
	if workers <= 0 {
		workers = 4
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Bot{client: client, allowed: allowed, handler: handler, workers: workers, logger: logger}
}

// Run consumes updates until ctx is cancelled and all workers drain.
func (b *Bot) Run(ctx context.Context, updates <-chan Update) {
	var wg sync.WaitGroup
	for range b.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case u := <-updates:
					b.process(ctx, u)
				}
			}
		}()
	}
	wg.Wait()
}

// process handles one update end to end; errors are logged, never fatal.
func (b *Bot) process(ctx context.Context, u Update) {
	msg := u.Message
	if msg == nil || msg.From == nil {
		return // not a message update (subscribed to messages only, but be safe)
	}
	log := b.logger.With("update_id", u.UpdateID, "user_id", msg.From.ID, "chat_id", msg.Chat.ID)

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
	if err := b.client.SendMessage(ctx, chatID, text); err != nil {
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
