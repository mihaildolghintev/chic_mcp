package agent

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"

	"mcp.chic.md/internal/llm"
	"mcp.chic.md/internal/store"
	"mcp.chic.md/internal/tracing"
)

// Keeping a long dialog in the prompt is what "переполнение окна" means here:
// every replayed turn costs tokens on every request, and the biggest turns are
// tool-laden assistant answers. Rather than blindly drop the oldest turns
// (losing what the user referred back to), we fold everything but the most
// recent turns into a short LLM-written summary and prepend that instead.
const (
	// summaryKeepRecentTurns is how many trailing turns stay verbatim — recent
	// context the user is most likely to say "а по нему?" about must not be
	// paraphrased away.
	summaryKeepRecentTurns = 6
	// summaryMaxChars caps the summary we accept back, a backstop against a
	// model that ignores the "коротко" instruction and re-bloats the prompt.
	summaryMaxChars = 1200
	// maxUnsummarizedScan bounds the tail pulled per request when folding, so a
	// pathological session can't load unbounded rows. Far above any real
	// session; when a session somehow exceeds it, folding just catches up over
	// the next few requests instead of reading everything at once.
	maxUnsummarizedScan = 400
)

// summarySystemPrompt steers the condense call. It asks for a dense,
// fact-preserving recap in the dialog's own language — names, numbers, periods
// and the still-open question must survive, prose must not.
const summarySystemPrompt = `Ты сжимаешь историю диалога пользователя с ассистентом по данным МойСклад.
Верни КРАТКОЕ содержание (до 800 символов, можно списком), сохранив: о чём спрашивали,
какие сущности/товары/склады/контрагенты и периоды упоминались, ключевые цифры и
незакрытые уточнения. Не выдумывай ничего сверх текста. Без вступлений и без «вот сводка» —
сразу содержание. Язык — тот же, что в диалоге.`

// condenseHistory returns the context to replay for userID's current session
// (epoch): a rolling summary of the older turns plus the recent turns verbatim.
// The summary is persisted and folded incrementally — each request only
// summarizes the turns that have scrolled past the budget since last time,
// instead of re-summarizing the whole history. Best-effort throughout: any
// store or LLM failure degrades to a bounded window, never an error.
func (a *Agent) condenseHistory(ctx context.Context, log *slog.Logger, userID, epoch int64) (summary string, recent []store.Message) {
	if a.opts.SummaryCharBudget < 0 { // summarization disabled — plain last-N window
		recent, err := a.store.RecentMessages(ctx, userID, a.opts.HistoryDepth)
		if err != nil {
			log.Warn("history read failed, answering without it", "err", err)
		}
		return "", recent
	}

	// Load the running summary and read only the turns not yet folded into it.
	summary, upToID, err := a.store.GetSessionSummary(ctx, userID, epoch)
	if err != nil {
		log.Warn("session summary read failed", "err", err)
	}
	if upToID < epoch {
		// Nothing folded yet (or a stale/absent watermark): this session's turns
		// are exactly those after its boundary.
		upToID = epoch
	}
	tail, err := a.store.MessagesSince(ctx, userID, upToID, maxUnsummarizedScan)
	if err != nil {
		log.Warn("session tail read failed, replaying summary only", "err", err)
		return summary, nil
	}

	// Under budget, or too few turns to bother: keep the whole tail verbatim.
	// Budget is counted in runes (not bytes), so the number means characters —
	// a Cyrillic letter counts as one, matching how a reader would size a dialog.
	total := 0
	for _, m := range tail {
		total += utf8.RuneCountInString(m.Content)
	}
	if total <= a.opts.SummaryCharBudget || len(tail) <= summaryKeepRecentTurns {
		return summary, tail
	}

	// Fold everything but the recent tail into the running summary.
	foldable := tail[:len(tail)-summaryKeepRecentTurns]
	recent = tail[len(tail)-summaryKeepRecentTurns:]

	next := a.summarizeInto(ctx, summary, foldable)
	if next == "" {
		// Couldn't summarize — keep the window bounded by dropping the foldable
		// turns this round. A shorter memory beats an overflowing prompt or an
		// error; the existing summary (if any) still stands.
		log.Warn("history summarization failed, dropping older turns", "dropped", len(foldable))
		return summary, recent
	}
	// Persist so the next request reuses this instead of re-summarizing.
	if err := a.store.PutSessionSummary(ctx, userID, epoch, next, foldable[len(foldable)-1].ID); err != nil {
		log.Warn("persist session summary failed", "err", err)
	}
	log.Info("history condensed", "folded_turns", len(foldable), "kept_turns", len(recent), "summary_chars", len(next))
	return next, recent
}

// summarizeInto asks the (cheap, text) provider to fold a run of turns into the
// running summary — prev is the summary so far (empty on the first fold), msgs
// are the new turns to absorb. Returns "" on any failure so the caller degrades
// gracefully.
func (a *Agent) summarizeInto(ctx context.Context, prev string, msgs []store.Message) string {
	if len(msgs) == 0 {
		return prev
	}
	// A named span so the history-condense LLM call is distinguishable in Phoenix
	// from the main answer's completions instead of being an anonymous llm.* span.
	ctx, span := tracing.Tracer().Start(ctx, "history.summarize")
	defer span.End()
	span.SetAttributes(
		tracing.SpanKind(tracing.SpanKindChain),
		attribute.Int("summarized_turns", len(msgs)),
		attribute.Bool("incremental", prev != ""),
	)

	var b strings.Builder
	if prev != "" {
		b.WriteString("Предыдущее краткое содержание:\n")
		b.WriteString(prev)
		b.WriteString("\n\nНовые сообщения диалога:\n")
	}
	for _, m := range msgs {
		who := "Пользователь"
		if m.Role == "assistant" {
			who = "Ассистент"
		}
		b.WriteString(who)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}

	resp, err := a.llm.Chat(ctx, llm.Request{
		Messages:  []llm.Message{llm.System(summarySystemPrompt), llm.User(b.String())},
		MaxTokens: 500,
	})
	if err != nil {
		return ""
	}
	return utf8Truncate(strings.TrimSpace(resp.Message.Text), summaryMaxChars)
}

// utf8Truncate trims s to at most n runes (not bytes), so a cut never lands
// inside a multibyte character.
func utf8Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
