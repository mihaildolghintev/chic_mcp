// Package agent is the bot's brain: it feeds a chat (history + new message)
// to an LLM, lets the model call MoySklad MCP tools, and loops until the
// model produces a final text answer. The MCP server runs in-process — the
// same mcpserver.New the stdio mode serves, driven through mcp-go's
// in-process client, so the bot dogfoods the exact public tool surface.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"golang.org/x/time/rate"

	"mcp.chic.md/internal/llm"
	"mcp.chic.md/internal/store"
)

// Friendly replies for the failure modes a user can do something about.
// Internal errors are returned as errors — the bot layer owns that wording.
const (
	msgRateLimited  = "Слишком много запросов — сделайте паузу и попробуйте позже."
	msgBudgetSpent  = "Запрос вышел слишком дорогим, я остановил обработку. Попробуйте сузить вопрос."
	msgTooManyloops = "Не смог собрать ответ за разумное число шагов. Попробуйте разбить вопрос на части."
	msgNoVision     = "Обработка фото не настроена (нет vision-провайдера). Пришлите вопрос текстом."
)

// Options tune the agent; zero values take the defaults below.
type Options struct {
	MaxRounds    int           // LLM⇄tools round trips per request (default 6)
	MaxTokens    int           // cumulative token stop-loss per request (default 200k)
	HistoryDepth int           // dialog turns replayed from the store (default 20)
	RatePerHour  int           // per-user requests per hour, 0 = default 30, <0 = unlimited
	Timeout      time.Duration // wall-clock cap per request (default 3m)

	// CurrencyCode and CurrencyName label monetary amounts in the system
	// prompt — the account's base currency (e.g. "MDL"/"лей"), resolved once at
	// startup. Empty falls back to a currency-neutral instruction.
	CurrencyCode string
	CurrencyName string
}

func (o Options) withDefaults() Options {
	if o.MaxRounds <= 0 {
		o.MaxRounds = 6
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = 200_000
	}
	if o.HistoryDepth <= 0 {
		o.HistoryDepth = 20
	}
	if o.RatePerHour == 0 {
		o.RatePerHour = 30
	}
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Minute
	}
	return o
}

// Agent holds the LLM client, the in-process MCP session and the dialog store.
type Agent struct {
	llm   *llm.Client
	mcp   *client.Client
	tools []llm.Tool
	store store.Store
	opts  Options

	mu       sync.Mutex
	limiters map[int64]*rate.Limiter
}

// New connects an in-process MCP client to srv, lists its tools and converts
// them to LLM function definitions. The client session lives for the process.
func New(ctx context.Context, llmClient *llm.Client, srv *server.MCPServer, st store.Store, opts Options) (*Agent, error) {
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		return nil, fmt.Errorf("agent: in-process client: %w", err)
	}
	if err := c.Start(ctx); err != nil {
		return nil, fmt.Errorf("agent: start mcp client: %w", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "chic-agent", Version: "1.0.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return nil, fmt.Errorf("agent: initialize mcp: %w", err)
	}

	list, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("agent: list mcp tools: %w", err)
	}
	tools, err := convertTools(list.Tools)
	if err != nil {
		return nil, err
	}
	// Memory tools are agent-local, not part of the public MoySklad MCP
	// surface: they mutate per-user state the MCP server has no notion of, so
	// the dispatch loop handles them against the store with the live user id.
	tools = append(tools, memoryTools...)
	slog.Info("agent ready", "tools", len(tools))

	return &Agent{
		llm:      llmClient,
		mcp:      c,
		tools:    tools,
		store:    st,
		opts:     opts.withDefaults(),
		limiters: make(map[int64]*rate.Limiter),
	}, nil
}

// Close tears down the MCP session.
func (a *Agent) Close() error { return a.mcp.Close() }

// Reset starts a fresh dialog session for userID: the next answer won't see
// anything said before this point. Old messages stay stored, and durable
// preferences (language, style, …) are untouched — /new forgets the
// conversation, not the person.
func (a *Agent) Reset(ctx context.Context, userID int64) error {
	return a.store.StartSession(ctx, userID)
}

// convertTools turns MCP tool schemas into OpenAI function definitions — the
// schemas are already JSON Schema objects, so this is a rename, not a mapping.
func convertTools(in []mcp.Tool) ([]llm.Tool, error) {
	out := make([]llm.Tool, 0, len(in))
	for _, t := range in {
		params, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("agent: marshal schema of %s: %w", t.Name, err)
		}
		out = append(out, llm.Tool{
			Type: "function",
			Function: llm.Function{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return out, nil
}

// Handle answers one user message. userID identifies the Telegram user (the
// key for both dialog history and durable memory); text is the message text
// (or the photo caption), imageDataURI is a base64 data URI when the message
// carries a photo. The returned string is always safe to send to the user.
func (a *Agent) Handle(ctx context.Context, userID int64, text, imageDataURI string) (string, error) {
	if !a.allow(userID) {
		return msgRateLimited, nil
	}
	if imageDataURI != "" && !a.llm.HasVision() {
		return msgNoVision, nil
	}
	ctx, cancel := context.WithTimeout(ctx, a.opts.Timeout)
	defer cancel()

	log := slog.Default().With("user_id", userID)

	// Both reads are context, not correctness — a failure degrades to a
	// memoryless answer instead of an error.
	history, err := a.store.RecentMessages(ctx, userID, a.opts.HistoryDepth)
	if err != nil {
		log.Warn("history read failed, answering without it", "err", err)
	}
	prefs, err := a.store.Preferences(ctx, userID)
	if err != nil {
		log.Warn("preferences read failed, answering without them", "err", err)
	}

	userMsg := llm.User(text)
	stored := text
	if imageDataURI != "" {
		userMsg = llm.UserImage(text, imageDataURI)
		stored = strings.TrimSpace("[фото] " + text)
	}

	msgs := make([]llm.Message, 0, len(history)+2)
	msgs = append(msgs, llm.System(systemPrompt(time.Now(), a.opts.CurrencyCode, a.opts.CurrencyName, prefs)))
	for _, h := range history {
		msgs = append(msgs, llm.Message{Role: h.Role, Text: h.Content})
	}
	msgs = append(msgs, userMsg)

	spent := 0
	for round := 0; round < a.opts.MaxRounds; round++ {
		resp, err := a.llm.Chat(ctx, llm.Request{Messages: msgs, Tools: a.tools})
		if err != nil {
			if errors.Is(err, llm.ErrNoVisionProvider) {
				return msgNoVision, nil
			}
			return "", fmt.Errorf("agent: llm round %d: %w", round+1, err)
		}
		spent += resp.Usage.TotalTokens
		msgs = append(msgs, resp.Message)

		if len(resp.Message.ToolCalls) == 0 {
			answer := strings.TrimSpace(resp.Message.Text)
			a.remember(ctx, log, userID, stored, answer)
			log.Info("agent answered", "rounds", round+1, "tokens", spent, "provider", resp.Provider)
			return answer, nil
		}

		// The stop-loss sits between rounds: one completion can't be undone,
		// but we can refuse to fund the next batch of tool calls.
		if spent > a.opts.MaxTokens {
			log.Warn("token stop-loss tripped", "tokens", spent, "budget", a.opts.MaxTokens)
			return msgBudgetSpent, nil
		}

		for _, tc := range resp.Message.ToolCalls {
			started := time.Now()
			result := a.dispatchTool(ctx, userID, tc)
			log.Info("tool called", "tool", tc.Function.Name, "took", time.Since(started).Round(time.Millisecond))
			msgs = append(msgs, llm.ToolResult(tc.ID, result))
		}
	}

	log.Warn("agent hit round limit", "rounds", a.opts.MaxRounds, "tokens", spent)
	return msgTooManyloops, nil
}

// dispatchTool routes one tool call. Memory tools are handled locally against
// the store (they need the live user id and mutate state the MCP server does
// not own); everything else goes to the in-process MoySklad MCP server.
func (a *Agent) dispatchTool(ctx context.Context, userID int64, tc llm.ToolCall) string {
	switch tc.Function.Name {
	case toolRememberPreference, toolForgetPreference:
		return a.callMemoryTool(ctx, userID, tc)
	default:
		return a.callTool(ctx, tc)
	}
}

// callTool executes one MCP tool call. Failures come back as text for the
// model to read — a tool error should steer the conversation, not kill it.
func (a *Agent) callTool(ctx context.Context, tc llm.ToolCall) string {
	var args map[string]any
	if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return "ERROR: invalid tool arguments JSON: " + err.Error()
		}
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = tc.Function.Name
	req.Params.Arguments = args
	res, err := a.mcp.CallTool(ctx, req)
	if err != nil {
		return "ERROR: tool call failed: " + err.Error()
	}

	var sb strings.Builder
	for _, content := range res.Content {
		switch c := content.(type) {
		case mcp.TextContent:
			sb.WriteString(c.Text)
		case *mcp.TextContent:
			sb.WriteString(c.Text)
		}
	}
	out := sb.String()
	if res.IsError {
		out = "ERROR: " + out
	}
	return truncate(out, maxToolResultChars)
}

// maxToolResultChars caps what one tool result feeds back into the model —
// a full catalog dump would blow the context window (and the budget).
const maxToolResultChars = 40_000

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…[результат обрезан]"
}

// remember persists the exchange; history is best-effort by design.
func (a *Agent) remember(ctx context.Context, log *slog.Logger, userID int64, userText, answer string) {
	if userText != "" {
		if err := a.store.AppendMessage(ctx, userID, "user", userText); err != nil {
			log.Warn("store user message failed", "err", err)
		}
	}
	if answer != "" {
		if err := a.store.AppendMessage(ctx, userID, "assistant", answer); err != nil {
			log.Warn("store assistant message failed", "err", err)
		}
	}
}

// allow enforces the per-user hourly rate limit.
func (a *Agent) allow(userID int64) bool {
	if a.opts.RatePerHour < 0 {
		return true
	}
	a.mu.Lock()
	lim, ok := a.limiters[userID]
	if !ok {
		// Full-burst limiter: RatePerHour requests immediately, then a steady
		// refill — matches "N запросов в час" the way a human expects it.
		lim = rate.NewLimiter(rate.Every(time.Hour/time.Duration(a.opts.RatePerHour)), a.opts.RatePerHour)
		a.limiters[userID] = lim
	}
	a.mu.Unlock()
	return lim.Allow()
}

// systemPrompt is rebuilt per request so "сегодня" is always today. currencyCode
// and currencyName label monetary amounts; empty falls back to a neutral hint,
// since the account's currency must never be assumed to be rubles. prefs are the
// user's durable preferences, rendered into the prompt so the model honours them
// across sessions without re-asking.
//
// The prompt itself is Russian (the operators' language), but it tells the model
// to answer in the question's language and never mix languages — English field
// names from tool results (revenue, turnover, stock…) must be translated into
// the answer's language, not echoed verbatim. A stored "language" preference
// overrides the question's language.
func systemPrompt(now time.Time, currencyCode, currencyName string, prefs []store.Preference) string {
	return fmt.Sprintf(`Ты — ассистент по данным МойСклад магазина Chic. Сегодня %s.

У тебя есть инструменты только для ЧТЕНИЯ данных МойСклад: товары, остатки,
продажи, прибыль, обороты, деньги, контрагенты, документы, аналитика
(ABC-анализ, сравнение периодов, мёртвый сток, дебиторка).

Правила:
- Отвечай кратко и по делу.
- Язык ответа — тот же, на котором задан текущий вопрос (русский → по-русски,
  английский → по-английски, румынский → по-румынски). Исключение: если в
  «Предпочтениях» ниже задан язык общения, используй его вместо языка вопроса.
- Весь ответ — строго на одном языке, без смешивания. Инструменты отдают названия
  показателей по-английски (revenue, profit, turnover, stock, reserve, margin,
  in-transit, cost) — переводи их на язык ответа (выручка, прибыль, оборот,
  остатки, резерв, маржа, в пути, себестоимость). Не оставляй английские слова в
  русском тексте и русские — в английском.
- Если у термина нет устоявшегося перевода — дай перевод, а оригинал приведи в
  скобках один раз при первом упоминании; дальше используй только перевод.
- %s
- Данные бери только из инструментов, ничего не выдумывай.
- Если вопрос про период ("за неделю", "в марте") — вычисли даты от сегодняшней.
- Если вопрос неоднозначный, задай короткий уточняющий вопрос вместо догадок.
- Для итогов за период бери поле "totals" из ответа инструмента — оно посчитано
  по ВСЕМ строкам. Никогда не суммируй "rows" вручную: это лишь верхушка списка,
  а "truncated"/"rowCount" показывают, что строк больше, чем показано.
- "Выручка" из get_profit = отчёт «Прибыльность» (отгрузки минус возвраты). Сумма
  документов «Отгрузка» (search_documents demand) включает услуги и не вычитает
  возвраты — поэтому цифры законно различаются, не выдумывай других причин.

Память о пользователе:
- Когда пользователь выражает УСТОЙЧИВОЕ пожелание (язык общения, стиль и формат
  ответов, специфику бизнеса — напр. основной склад) — сохрани его вызовом
  remember_preference со СТАБИЛЬНЫМ ключом на латинице (language, reply_style,
  main_warehouse и т.п.). Не сохраняй разовые вопросы и сами данные из отчётов.
- Если пожелание изменилось — перезапиши тем же ключом; если отменено — удали
  через forget_preference. Не переспрашивай то, что уже есть в «Предпочтениях».
- Сохранение — молча, не сообщай о нём отдельным предложением.
%s
Формат: пиши обычным Markdown — он отображается в Telegram.
- Разметка: **жирный**, *курсив*, `+"`моноширинный`"+`, > цитата, списки «- пункт»,
  ссылки [текст](url). Заголовок «# …» станет жирной строкой.
- Начинай с главного вывода или итоговой цифры, детали — ниже.
- Таблиц избегай: вместо таблицы делай карточки — строка «эмодзи **Название**»,
  под ней 1-3 коротких строки «показатель: значение» (каждая с новой строки),
  между карточками пустая строка.
- Длинный список (больше ~10 карточек) сверни: покажи первые позиции, остальные
  помести в цитату «> …» — длинная цитата в Telegram сворачивается.
- Артикулы, коды и номера документов оборачивай в `+"`моноширинный`"+`.
- Держи строки короткими: ответ читают с телефона.`,
		now.Format("2006-01-02 (Monday)"), moneyRule(currencyCode, currencyName), renderProfile(prefs))
}

// renderProfile turns stored preferences into a prompt block the model reads as
// established facts about the user. Empty preferences render to an empty string
// so a new user's prompt carries no dangling "Предпочтения" header.
func renderProfile(prefs []store.Preference) string {
	if len(prefs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nПредпочтения пользователя (уже известны, соблюдай их):\n")
	for _, p := range prefs {
		fmt.Fprintf(&b, "- %s: %s\n", p.Key, p.Value)
	}
	return b.String()
}

// moneyRule builds the currency-formatting line of the system prompt from the
// account's resolved base currency. It never assumes rubles: with no resolved
// currency it tells the model to use MoySklad's own currency labels.
func moneyRule(code, name string) string {
	switch {
	case code != "" && name != "":
		return fmt.Sprintf("Суммы — в валюте учёта (%s, %s), тысячи отделяй "+
			"пробелом и добавляй код: 12 345 %s.", name, code, code)
	case code != "":
		return fmt.Sprintf("Суммы — в валюте учёта, тысячи отделяй пробелом и "+
			"добавляй код: 12 345 %s.", code)
	default:
		return "Суммы — в валюте учёта аккаунта МойСклад (не предполагай рубли; " +
			"если валюта неизвестна, вызови инструмент get_account_currency). " +
			"Тысячи отделяй пробелом."
	}
}
