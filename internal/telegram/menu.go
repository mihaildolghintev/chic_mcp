package telegram

import (
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
)

// The bot's two menu affordances, both surfaced as commands and inline
// keyboards:
//   - a library of ready-made questions (/menu): a tap sends the question
//     through the agent exactly as if the user had typed it, solving the
//     "blank field" problem and advertising what the bot can answer.
//   - a preference viewer (/memory): lists what the bot durably remembers about
//     the user, each with a delete button.
const (
	// quickReplyPrefix + index is the callback data of a question button. Index,
	// not the question text, keeps callback_data well under Telegram's 64-byte
	// cap regardless of how long the template is.
	quickReplyPrefix = "ask:"
	// memForgetPrefix + index is the callback data of a "delete this preference"
	// button. Index (into the freshly-read, key-sorted profile), not the key,
	// keeps it under the 64-byte cap even for a maximum-length key.
	memForgetPrefix = "mf:"

	msgMenuPrompt    = "Выберите готовый вопрос или напишите свой:"
	msgMemoryEmpty   = "🧠 Пока я не запомнил никаких ваших предпочтений.\n\nКогда вы зададите устойчивое пожелание (язык общения, формат ответов, основной склад…), я сохраню его и покажу здесь."
	msgMemoryHeader  = "🧠 <b>Что я о вас помню</b>\n\nНажмите 🗑, чтобы удалить пункт."
	msgMemoryDeleted = "Удалил. "
)

// Template is one ready-made question: a short button Label and the full
// Question text that gets sent to the agent when it is tapped.
type Template struct {
	Label    string
	Question string
}

// questionTemplates is the static library shown by /menu. Each maps onto a real
// MoySklad tool the agent can answer with — sales, profit, stock, dead stock,
// period comparison, receivables, cash. Kept short and business-first so a new
// user sees, from the first screen, the kind of thing worth asking.
var questionTemplates = []Template{
	{"📊 Продажи за сегодня", "Покажи продажи за сегодня"},
	{"💰 Прибыль за неделю", "Какая прибыль за последнюю неделю?"},
	{"📦 Что заканчивается", "Какие товары заканчиваются на складе?"},
	{"🏆 Топ товаров за месяц", "Топ-10 товаров по продажам за последний месяц"},
	{"🐢 Мёртвый сток", "Покажи неликвид — что залежалось на складе"},
	{"📉 Этот месяц vs прошлый", "Сравни продажи и прибыль этого месяца с прошлым"},
}

// menuKeyboard is the inline keyboard for /menu: one question per row (the
// labels are too long to pair) with an index-encoded callback.
func menuKeyboard() *models.InlineKeyboardMarkup {
	rows := make([][]models.InlineKeyboardButton, 0, len(questionTemplates))
	for i, t := range questionTemplates {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: t.Label, CallbackData: quickReplyPrefix + strconv.Itoa(i)},
		})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// templateByCallback resolves a quick-reply callback back to its Question, or
// ("", false) if the data isn't a quick-reply or the index is out of range.
func templateByCallback(data string) (string, bool) {
	idx, ok := indexFromCallback(data, quickReplyPrefix)
	if !ok || idx >= len(questionTemplates) {
		return "", false
	}
	return questionTemplates[idx].Question, true
}

// indexFromCallback parses "prefix<int>" callback data into its integer index.
func indexFromCallback(data, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(data, prefix)
	if !ok {
		return 0, false
	}
	idx, err := strconv.Atoi(rest)
	if err != nil || idx < 0 {
		return 0, false
	}
	return idx, true
}

// MemoryItem is one durable preference as the /memory view renders it — a
// package-local shape so the telegram layer never imports the store package.
type MemoryItem struct {
	Key   string
	Value string
}

// renderMemory turns a profile into the /memory message text and its per-item
// delete keyboard. Empty profile → the friendly empty-state and a nil keyboard.
// Key and value are HTML-escaped: they are user/model-authored and must not be
// able to inject markup into the rendered message.
func renderMemory(items []MemoryItem) (string, *models.InlineKeyboardMarkup) {
	if len(items) == 0 {
		return msgMemoryEmpty, nil
	}
	var b strings.Builder
	b.WriteString(msgMemoryHeader)
	b.WriteString("\n\n")
	rows := make([][]models.InlineKeyboardButton, 0, len(items))
	for i, it := range items {
		b.WriteString("• <b>")
		b.WriteString(html.EscapeString(it.Key))
		b.WriteString("</b>: ")
		b.WriteString(html.EscapeString(it.Value))
		b.WriteString("\n")
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "🗑 " + it.Key, CallbackData: memForgetPrefix + strconv.Itoa(i)},
		})
	}
	return b.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}
