package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"mcp.chic.md/internal/llm"
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
