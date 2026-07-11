package agent

import (
	"context"
	"log/slog"
	"strings"

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
)

// summarySystemPrompt steers the condense call. It asks for a dense,
// fact-preserving recap in the dialog's own language — names, numbers, periods
// and the still-open question must survive, prose must not.
const summarySystemPrompt = `Ты сжимаешь историю диалога пользователя с ассистентом по данным МойСклад.
Верни КРАТКОЕ содержание (до 800 символов, можно списком), сохранив: о чём спрашивали,
какие сущности/товары/склады/контрагенты и периоды упоминались, ключевые цифры и
незакрытые уточнения. Не выдумывай ничего сверх текста. Без вступлений и без «вот сводка» —
сразу содержание. Язык — тот же, что в диалоге.`

// condenseHistory bounds the replayed history to roughly opts.SummaryCharBudget.
// Under budget it returns the history untouched and an empty summary. Over
// budget it summarizes everything but the last summaryKeepRecentTurns turns and
// returns (summary, recentTurns). It is best-effort: if the summarization call
// fails, it degrades to dropping the older turns (window still bounded) rather
// than failing the user's request — logged, never fatal.
func (a *Agent) condenseHistory(ctx context.Context, log *slog.Logger, history []store.Message) (summary string, recent []store.Message) {
	if a.opts.SummaryCharBudget < 0 { // summarization disabled
		return "", history
	}
	total := 0
	for _, m := range history {
		total += len(m.Content)
	}
	if total <= a.opts.SummaryCharBudget || len(history) <= summaryKeepRecentTurns {
		return "", history
	}

	older := history[:len(history)-summaryKeepRecentTurns]
	recent = history[len(history)-summaryKeepRecentTurns:]

	summary = a.summarize(ctx, older)
	if summary == "" {
		// Couldn't summarize — keep the window bounded by dropping the old turns
		// outright. A shorter memory beats an overflowing prompt or an error.
		log.Warn("history summarization failed, dropping oldest turns", "dropped", len(older))
		return "", recent
	}
	log.Info("history condensed", "summarized_turns", len(older), "kept_turns", len(recent), "summary_chars", len(summary))
	return summary, recent
}

// summarize asks the (cheap, text) provider to compress a run of turns into a
// short recap. Returns "" on any failure so the caller can degrade gracefully.
func (a *Agent) summarize(ctx context.Context, msgs []store.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	// A named span so the history-condense LLM call is distinguishable in Phoenix
	// from the main answer's completions instead of being an anonymous llm.* span.
	ctx, span := tracing.Tracer().Start(ctx, "history.summarize")
	defer span.End()
	span.SetAttributes(
		tracing.SpanKind(tracing.SpanKindChain),
		attribute.Int("summarized_turns", len(msgs)),
	)

	var b strings.Builder
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
