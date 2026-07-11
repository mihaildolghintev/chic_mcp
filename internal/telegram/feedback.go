package telegram

import (
	"context"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// The 👍/👎 affordance under every answer. A tap records a rating against the
// trace's root span in Phoenix, so an audit of the traces can single out the
// answers that got a thumbs-down. The span id rides inside the button's
// callback data — 16 hex chars, well under Telegram's 64-byte cap — so a press
// arriving minutes later carries everything needed to annotate the right span,
// with no message-id→span mapping to store.
const (
	feedbackPrefix  = "fb:"
	feedbackLike    = "fb:l:"
	feedbackDislike = "fb:d:"

	// ratingLike/ratingDislike are the values handed to the feedback hook; the
	// hook maps them onto Phoenix's label/score.
	ratingLike    = "like"
	ratingDislike = "dislike"

	msgFeedbackThanks = "Спасибо за оценку 🙏"
)

// FeedbackFunc records one user rating. rating is ratingLike or ratingDislike;
// spanID is the root span the rating annotates. chatID is 0 when the button's
// message is too old to be accessible — the rating is still recorded.
type FeedbackFunc func(ctx context.Context, spanID, rating string, userID, chatID int64) error

// feedbackKeyboard builds the 👍/👎 row for a fresh answer, encoding the trace's
// root span id into each button. Returns nil when spanID is empty (tracing
// disabled) so no affordance is shown when there is nothing to annotate.
func feedbackKeyboard(spanID string) *models.InlineKeyboardMarkup {
	if spanID == "" {
		return nil
	}
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "👍", CallbackData: feedbackLike + spanID},
			{Text: "👎", CallbackData: feedbackDislike + spanID},
		}},
	}
}

// parseFeedback splits a feedback callback into its rating and span id, or
// ("", "", false) when the data isn't a feedback press or carries no span.
func parseFeedback(data string) (rating, spanID string, ok bool) {
	switch {
	case strings.HasPrefix(data, feedbackLike):
		spanID = strings.TrimPrefix(data, feedbackLike)
		rating = ratingLike
	case strings.HasPrefix(data, feedbackDislike):
		spanID = strings.TrimPrefix(data, feedbackDislike)
		rating = ratingDislike
	default:
		return "", "", false
	}
	if spanID == "" {
		return "", "", false
	}
	return rating, spanID, true
}

// doFeedback records the tapped rating and then removes the buttons so the vote
// is one-shot. Recording comes first and does not depend on the message being
// accessible: the span id is in the callback data, which lets the audit workflow
// rate dialogs older than 48h (whose content Telegram strips and whose keyboard
// can no longer be edited). Clearing the buttons is best-effort — on an old or
// inaccessible message it just fails and the annotation's upsert identifier keeps
// a stray re-tap from duplicating.
func (b *Bot) doFeedback(ctx context.Context, log *slog.Logger, q *models.CallbackQuery) {
	rating, spanID, ok := parseFeedback(q.Data)
	if !ok || b.onFeedback == nil {
		return
	}

	var chatID int64
	if q.Message.Message != nil {
		chatID = q.Message.Message.Chat.ID
	}
	if err := b.onFeedback(ctx, spanID, rating, q.From.ID, chatID); err != nil {
		log.Warn("record feedback failed", "err", err, "rating", rating)
	} else {
		log.Info("feedback recorded", "rating", rating, "span_id", spanID)
	}

	if q.Message.Message == nil {
		return // too old to edit; annotation is already recorded
	}
	if _, err := b.api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:    q.Message.Message.Chat.ID,
		MessageID: q.Message.Message.ID,
	}); err != nil {
		log.Debug("clear feedback buttons failed", "err", err)
	}
}
