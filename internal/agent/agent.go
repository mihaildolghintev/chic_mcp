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
	RatePerHour  int           // per-chat requests per hour, 0 = default 30, <0 = unlimited
	Timeout      time.Duration // wall-clock cap per request (default 3m)
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

// Reset starts a fresh dialog session for chatID: the next answer won't see
// anything said before this point. Old messages stay stored.
func (a *Agent) Reset(ctx context.Context, chatID int64) error {
	return a.store.StartSession(ctx, chatID)
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

// Handle answers one user message. text is the message text (or the photo
// caption), imageDataURI is a base64 data URI when the message carries a
// photo. The returned string is always safe to send to the user.
func (a *Agent) Handle(ctx context.Context, chatID int64, text, imageDataURI string) (string, error) {
	if !a.allow(chatID) {
		return msgRateLimited, nil
	}
	if imageDataURI != "" && !a.llm.HasVision() {
		return msgNoVision, nil
	}
	ctx, cancel := context.WithTimeout(ctx, a.opts.Timeout)
	defer cancel()

	log := slog.Default().With("chat_id", chatID)

	// History is context, not correctness — a read failure degrades to a
	// memoryless answer instead of an error.
	history, err := a.store.RecentMessages(ctx, chatID, a.opts.HistoryDepth)
	if err != nil {
		log.Warn("history read failed, answering without it", "err", err)
	}

	userMsg := llm.User(text)
	stored := text
	if imageDataURI != "" {
		userMsg = llm.UserImage(text, imageDataURI)
		stored = strings.TrimSpace("[фото] " + text)
	}

	msgs := make([]llm.Message, 0, len(history)+2)
	msgs = append(msgs, llm.System(systemPrompt(time.Now())))
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
			a.remember(ctx, log, chatID, stored, answer)
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
			result := a.callTool(ctx, tc)
			log.Info("tool called", "tool", tc.Function.Name, "took", time.Since(started).Round(time.Millisecond))
			msgs = append(msgs, llm.ToolResult(tc.ID, result))
		}
	}

	log.Warn("agent hit round limit", "rounds", a.opts.MaxRounds, "tokens", spent)
	return msgTooManyloops, nil
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
func (a *Agent) remember(ctx context.Context, log *slog.Logger, chatID int64, userText, answer string) {
	if userText != "" {
		if err := a.store.AppendMessage(ctx, chatID, "user", userText); err != nil {
			log.Warn("store user message failed", "err", err)
		}
	}
	if answer != "" {
		if err := a.store.AppendMessage(ctx, chatID, "assistant", answer); err != nil {
			log.Warn("store assistant message failed", "err", err)
		}
	}
}

// allow enforces the per-chat hourly rate limit.
func (a *Agent) allow(chatID int64) bool {
	if a.opts.RatePerHour < 0 {
		return true
	}
	a.mu.Lock()
	lim, ok := a.limiters[chatID]
	if !ok {
		// Full-burst limiter: RatePerHour requests immediately, then a steady
		// refill — matches "N запросов в час" the way a human expects it.
		lim = rate.NewLimiter(rate.Every(time.Hour/time.Duration(a.opts.RatePerHour)), a.opts.RatePerHour)
		a.limiters[chatID] = lim
	}
	a.mu.Unlock()
	return lim.Allow()
}

// systemPrompt is rebuilt per request so "сегодня" is always today.
func systemPrompt(now time.Time) string {
	return fmt.Sprintf(`Ты — ассистент по данным МойСклад магазина Chic. Сегодня %s.

У тебя есть инструменты только для ЧТЕНИЯ данных МойСклад: товары, остатки,
продажи, прибыль, обороты, деньги, контрагенты, документы, аналитика
(ABC-анализ, сравнение периодов, мёртвый сток, дебиторка).

Правила:
- Отвечай на русском, кратко и по делу. Суммы — в рублях, тысячи отделяй
  пробелом: 12 345 ₽.
- Данные бери только из инструментов, ничего не выдумывай.
- Если вопрос про период ("за неделю", "в марте") — вычисли даты от сегодняшней.
- Если вопрос неоднозначный, задай короткий уточняющий вопрос вместо догадок.

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
		now.Format("2006-01-02 (Monday)"))
}
