package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"

	"mcp.chic.md/internal/llm"
	"mcp.chic.md/internal/store"
	"mcp.chic.md/internal/tracing"
)

// The agent-local memory tools. Unlike the MoySklad tools (served by the
// in-process MCP server), these mutate durable per-user state in app.db, so
// the dispatch loop binds them to the current user id instead of forwarding
// them to MCP. Names are matched in dispatchTool.
const (
	toolRememberPreference = "remember_preference"
	toolForgetPreference   = "forget_preference"
)

// Bounds on what one preference may hold. Values render verbatim into every
// future system prompt, so a single long value would bloat every request — cap
// it. The key is a short identifier. Both are enforced at write time so the
// store never holds anything unbounded.
const (
	maxPreferenceKeyLen   = 64
	maxPreferenceValueLen = 200
)

// memoryTools are appended to the tool list advertised to the model. Kept as
// package-level values (schemas are static) so New only pays to build them
// once per process.
var memoryTools = []llm.Tool{
	{
		Type: "function",
		Function: llm.Function{
			Name: toolRememberPreference,
			Description: "Сохранить устойчивое предпочтение пользователя, которое должно " +
				"помниться между диалогами (язык общения, стиль/формат ответов, " +
				"специфика бизнеса). Повторный вызов с тем же key перезаписывает значение. " +
				"Значение — короткое и по сути (до 200 символов). " +
				"Не для разовых вопросов и не для данных из отчётов.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"key": {
						"type": "string",
						"description": "Стабильный идентификатор на латинице, напр. language, reply_style, main_warehouse."
					},
					"value": {
						"type": "string",
						"description": "Значение предпочтения, напр. \"английский\" или \"кратко, без копеек\"."
					}
				},
				"required": ["key", "value"],
				"additionalProperties": false
			}`),
		},
	},
	{
		Type: "function",
		Function: llm.Function{
			Name: toolForgetPreference,
			Description: "Удалить ранее сохранённое предпочтение пользователя по его key " +
				"(когда пожелание отменено или больше не актуально).",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"key": {
						"type": "string",
						"description": "Ключ ранее сохранённого предпочтения."
					}
				},
				"required": ["key"],
				"additionalProperties": false
			}`),
		},
	},
}

// callMemoryTool executes one memory tool against the store for userID. Like
// callTool, failures are returned as text for the model to read rather than as
// errors that would abort the turn.
func (a *Agent) callMemoryTool(ctx context.Context, userID int64, tc llm.ToolCall) string {
	var args struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return "ERROR: invalid tool arguments JSON: " + err.Error()
		}
	}
	// Both key and value render verbatim into every future system prompt, so
	// collapse whitespace in each: a newline would break the "- key: value"
	// list and could smuggle a fake instruction line.
	key := strings.Join(strings.Fields(args.Key), " ")
	if key == "" {
		return "ERROR: key обязателен и не может быть пустым"
	}
	if utf8.RuneCountInString(key) > maxPreferenceKeyLen {
		return fmt.Sprintf("ERROR: key слишком длинный (макс %d символов)", maxPreferenceKeyLen)
	}

	switch tc.Function.Name {
	case toolRememberPreference:
		// Bound the length too, so the profile can't bloat the prompt one long
		// value at a time.
		value := strings.Join(strings.Fields(args.Value), " ")
		if value == "" {
			return "ERROR: value обязателен для remember_preference"
		}
		if utf8.RuneCountInString(value) > maxPreferenceValueLen {
			return fmt.Sprintf("ERROR: value слишком длинный (макс %d символов); сохрани короче и по сути", maxPreferenceValueLen)
		}
		if err := a.store.SetPreference(ctx, userID, key, value); err != nil {
			return "ERROR: не удалось сохранить предпочтение: " + err.Error()
		}
		// A profile that has grown large is where duplicate/synonymous keys and
		// stale contradictions accumulate — fold them together now, silently and
		// best-effort, so the next prompt carries a clean profile.
		a.maybeConsolidate(ctx, userID)
		return fmt.Sprintf("OK: сохранено %s = %s", key, value)
	case toolForgetPreference:
		if err := a.store.DeletePreference(ctx, userID, key); err != nil {
			return "ERROR: не удалось удалить предпочтение: " + err.Error()
		}
		return fmt.Sprintf("OK: предпочтение %s удалено", key)
	default:
		return "ERROR: неизвестный инструмент памяти: " + tc.Function.Name
	}
}

// Preferences returns the user's durable preferences — the read side the
// /memory command renders. It is a thin pass-through to the store so the bot
// layer never depends on the store package directly.
func (a *Agent) Preferences(ctx context.Context, userID int64) ([]store.Preference, error) {
	return a.store.Preferences(ctx, userID)
}

// ForgetPreference deletes one durable preference for userID — the write side
// of the /memory command's delete buttons. Deleting a missing key is a no-op,
// mirroring forget_preference.
func (a *Agent) ForgetPreference(ctx context.Context, userID int64, key string) error {
	return a.store.DeletePreference(ctx, userID, key)
}

// consolidateThreshold is the profile size at or above which maybeConsolidate
// runs a merge pass. Below it, a handful of keys can't have accumulated enough
// duplication or contradiction to be worth an LLM round-trip. It is a soft cap,
// well under maxRenderedPreferences.
const consolidateThreshold = 8

// consolidatedProfile is the strict JSON shape the model must return from a
// consolidation pass: the whole desired profile, deduplicated and free of
// contradictions.
type consolidatedProfile struct {
	Preferences []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"preferences"`
}

const consolidateSystemPrompt = `Тебе дают текущий профиль предпочтений пользователя (список key/value).
Верни его КАНОНИЧЕСКУЮ версию: объедини дубли и синонимичные ключи в один,
устрани противоречия (оставь наиболее конкретное/полное значение), выкини пустое и мусор.
Ключи — стабильные идентификаторы на латинице (language, reply_style, main_warehouse и т.п.),
значения — короткие (до 200 символов). Не добавляй ничего нового, только консолидируй имеющееся.
Ответ — СТРОГО JSON вида {"preferences":[{"key":"...","value":"..."}]} и ничего кроме него.`

// maybeConsolidate folds a large preference profile into a canonical, dedup'd,
// contradiction-free set. It runs only past consolidateThreshold and is fully
// best-effort: any read/LLM/parse failure leaves the stored profile exactly as
// it was — consolidation must never lose a user's preferences. Silent by design
// (like remember itself), it logs at debug for observability.
func (a *Agent) maybeConsolidate(ctx context.Context, userID int64) {
	log := slog.Default().With("user_id", userID)

	current, err := a.store.Preferences(ctx, userID)
	if err != nil || len(current) < consolidateThreshold {
		return
	}

	// Name the consolidation pass so its LLM call is legible in Phoenix rather
	// than an anonymous completion nested under the memory tool span.
	ctx, span := tracing.Tracer().Start(ctx, "memory.consolidate")
	defer span.End()
	span.SetAttributes(
		tracing.SpanKind(tracing.SpanKindChain),
		attribute.Int("profile_size", len(current)),
	)

	prompt, err := json.Marshal(consolidatedProfile{Preferences: toEntries(current)})
	if err != nil {
		return
	}
	resp, err := a.llm.Chat(ctx, llm.Request{
		Messages:  []llm.Message{llm.System(consolidateSystemPrompt), llm.User(string(prompt))},
		MaxTokens: 800,
	})
	if err != nil {
		return
	}

	desired, ok := parseConsolidated(resp.Message.Text)
	if !ok || len(desired) == 0 || len(desired) > len(current) {
		// Empty, unparseable, or somehow larger than the input — treat as "no
		// safe consolidation" and leave the profile untouched.
		log.Debug("consolidation skipped (no safe result)", "have", len(current))
		return
	}

	a.reconcile(ctx, log, userID, current, desired)
}

// reconcile makes the stored profile equal to desired: it deletes keys that are
// no longer present and writes every desired key (overwriting merged values).
// Each write is independent and best-effort — a partial reconcile is still a
// valid, smaller profile.
func (a *Agent) reconcile(ctx context.Context, log *slog.Logger, userID int64, current, desired []store.Preference) {
	keep := make(map[string]struct{}, len(desired))
	for _, p := range desired {
		keep[p.Key] = struct{}{}
	}
	deleted := 0
	for _, p := range current {
		if _, ok := keep[p.Key]; !ok {
			if err := a.store.DeletePreference(ctx, userID, p.Key); err != nil {
				log.Debug("consolidation delete failed", "key", p.Key, "err", err)
				continue
			}
			deleted++
		}
	}
	for _, p := range desired {
		if err := a.store.SetPreference(ctx, userID, p.Key, p.Value); err != nil {
			log.Debug("consolidation set failed", "key", p.Key, "err", err)
		}
	}
	log.Info("preferences consolidated", "before", len(current), "after", len(desired), "deleted", deleted)
}

// parseConsolidated extracts the profile from the model's reply. It tolerates a
// model that wraps the JSON in prose or a ```json fence by scanning to the outer
// braces, then keeps only entries that pass the same key/value bounds
// remember_preference enforces — so consolidation can never smuggle in an
// oversized or malformed preference.
func parseConsolidated(raw string) ([]store.Preference, bool) {
	s := strings.TrimSpace(raw)
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return nil, false
	}
	var parsed consolidatedProfile
	if err := json.Unmarshal([]byte(s[start:end+1]), &parsed); err != nil {
		return nil, false
	}
	out := make([]store.Preference, 0, len(parsed.Preferences))
	seen := make(map[string]struct{}, len(parsed.Preferences))
	for _, e := range parsed.Preferences {
		key := strings.Join(strings.Fields(e.Key), " ")
		value := strings.Join(strings.Fields(e.Value), " ")
		if key == "" || value == "" {
			continue
		}
		if utf8.RuneCountInString(key) > maxPreferenceKeyLen || utf8.RuneCountInString(value) > maxPreferenceValueLen {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, store.Preference{Key: key, Value: value})
	}
	return out, true
}

// toEntries adapts stored preferences to the JSON entry shape the prompt uses.
func toEntries(prefs []store.Preference) []struct {
	Key   string `json:"key"`
	Value string `json:"value"`
} {
	out := make([]struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}, len(prefs))
	for i, p := range prefs {
		out[i].Key, out[i].Value = p.Key, p.Value
	}
	return out
}
