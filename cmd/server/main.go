// Command server runs the chic monolith. It has two modes:
//
//	bot   — the production default: a Telegram bot served over a webhook
//	        (route /tg/<secret> behind kamal-proxy TLS) plus /healthz.
//	stdio — the MoySklad MCP server alone over stdin/stdout, for inspecting
//	        the exact tool surface the bot dogfoods (mcp-inspector, tests).
//
// Select with -transport (or MCP_TRANSPORT); default is bot.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/mark3labs/mcp-go/server"

	"mcp.chic.md/internal/agent"
	"mcp.chic.md/internal/buildinfo"
	"mcp.chic.md/internal/cache"
	"mcp.chic.md/internal/llm"
	"mcp.chic.md/internal/mcpserver"
	"mcp.chic.md/internal/moysklad"
	"mcp.chic.md/internal/store"
	"mcp.chic.md/internal/telegram"
)

func main() {
	slog.SetDefault(newLogger())

	transport := flag.String("transport", envOr("MCP_TRANSPORT", "bot"), "mode: bot or stdio")
	healthCheck := flag.Bool("health-check", false, "probe /healthz on the local server and exit 0/1 (for container HEALTHCHECK)")
	flag.Parse()

	if *healthCheck {
		os.Exit(runHealthCheck())
	}

	slog.Info("chic starting", "build", buildinfo.Get(), "mode", *transport)

	switch *transport {
	case "bot":
		runBot()
	case "stdio":
		runStdio()
	default:
		slog.Error("unknown mode", "mode", *transport, "want", "bot or stdio")
		os.Exit(1)
	}
}

// newLogger builds the process logger. It always writes to stderr — in stdio
// mode the MCP protocol owns stdout, so logs must never touch it. Set
// LOG_FORMAT=json for machine-readable output (default is human-readable text)
// and LOG_LEVEL=debug|info|warn|error to set the minimum level (default info).
func newLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(os.Getenv("LOG_LEVEL"))}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if strings.EqualFold(os.Getenv("LOG_FORMAT"), "json") {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// requireEnv reads an env var or exits — all bot-mode config is fail-fast so a
// misconfigured deploy dies loudly instead of half-working.
func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required environment variable is not set", "var", key)
		os.Exit(1)
	}
	return v
}

// buildAPI wraps the client in a TTL cache when CACHE_DB is set; the returned
// close func is a no-op when caching is disabled.
func buildAPI(token string) (mcpserver.MoyskladAPI, func()) {
	client := moysklad.NewClient(token)
	var api mcpserver.MoyskladAPI = client

	dbPath := os.Getenv("CACHE_DB")
	if dbPath == "" {
		slog.Info("response cache disabled (set CACHE_DB to enable)")
		return api, func() {}
	}
	store, err := cache.OpenStore(dbPath)
	if err != nil {
		slog.Error("open cache", "err", err)
		os.Exit(1)
	}
	stopJanitor := store.StartJanitor(10 * time.Minute)
	slog.Info("response cache enabled", "path", dbPath)
	return cache.New(client, store, cache.DefaultTTLs()), func() {
		stopJanitor()
		_ = store.Close()
	}
}

// resolveCurrency looks up the account's base currency for the agent's system
// prompt. It uses a short timeout and never fails the boot — an empty result
// just yields a currency-neutral prompt.
func resolveCurrency(ctx context.Context, api mcpserver.MoyskladAPI) (code, name string) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cur, err := api.AccountCurrency(ctx)
	if err != nil {
		slog.Warn("could not resolve account currency; amounts will be labeled generically", "err", err)
		return "", ""
	}
	slog.Info("account currency resolved", "isoCode", cur.ISOCode, "name", cur.Name)
	return cur.ISOCode, cur.Name
}

// mcpTokenMinLen is the minimum accepted length for MCP_BEARER_TOKEN — short
// enough to allow any real random token, long enough to reject a typo/weak
// value that would expose the whole read surface.
const mcpTokenMinLen = 24

// bearerAuth guards next with a constant-time token check. The "Bearer" scheme
// is matched case-insensitively (RFC 6750), the token constant-time. A missing
// or wrong credential yields 401 and never reaches the MCP handler.
func bearerAuth(token string, next http.Handler) http.Handler {
	want := []byte(token)
	const prefix = "bearer "
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		got := []byte(auth[len(prefix):])
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// runBot is the production mode: Telegram webhook + worker pool + /healthz,
// with the MCP-backed LLM agent answering messages.
func runBot() {
	botToken := requireEnv("TELEGRAM_BOT_TOKEN")
	webhookSecret := requireEnv("TELEGRAM_WEBHOOK_SECRET")
	publicBaseURL := strings.TrimRight(requireEnv("PUBLIC_BASE_URL"), "/")
	allowed, err := telegram.ParseAllowedIDs(requireEnv("ALLOWED_USER_IDS"))
	if err != nil {
		slog.Error("ALLOWED_USER_IDS", "err", err)
		os.Exit(1)
	}
	addr := envOr("LISTEN_ADDR", ":8080")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	api, closeCache := buildAPI(requireEnv("MOYSKLAD_TOKEN"))
	defer closeCache()

	llmClient, err := llm.FromEnv()
	if err != nil {
		slog.Error("llm config", "err", err)
		os.Exit(1)
	}
	slog.Info("llm configured", "vision", llmClient.HasVision())

	// app.db is durable state (dialog memory), unlike the regenerable cache.
	appDB, err := store.Open(envOr("APP_DB", "app.db"))
	if err != nil {
		slog.Error("open app db", "err", err)
		os.Exit(1)
	}
	defer func() { _ = appDB.Close() }()

	// Resolve the account's base currency once so the agent labels amounts
	// correctly (lei, rubles, euro, …) instead of assuming rubles. A failure
	// here is non-fatal: the agent falls back to a currency-neutral prompt and
	// can still call get_account_currency at runtime.
	curCode, curName := resolveCurrency(ctx, api)

	ag, err := agent.New(ctx, llmClient, mcpserver.New(api), appDB, agent.Options{
		CurrencyCode: curCode,
		CurrencyName: curName,
	})
	if err != nil {
		slog.Error("agent init", "err", err)
		os.Exit(1)
	}
	defer func() { _ = ag.Close() }()

	// The handler needs the bot for photo downloads, and the bot needs the
	// handler at construction — the closure captures the variable assigned
	// right below, before any update is served.
	var bot *telegram.Bot
	handler := telegram.HandlerFunc(func(ctx context.Context, msg *models.Message) (telegram.Reply, error) {
		// /new works the same as the inline button: forget the dialog.
		if cmd, _, _ := strings.Cut(strings.TrimSpace(msg.Text), "@"); cmd == "/new" {
			if err := ag.Reset(ctx, msg.Chat.ID); err != nil {
				return telegram.Reply{}, fmt.Errorf("reset session: %w", err)
			}
			return telegram.Reply{Text: telegram.MsgSessionReset}, nil
		}
		text, imageURI := msg.Text, ""
		if len(msg.Photo) > 0 {
			uri, err := bot.PhotoDataURI(ctx, msg)
			if errors.Is(err, telegram.ErrPhotoTooLarge) {
				return telegram.Reply{Text: "Фото слишком большое, лимит 20 МБ."}, nil
			}
			if err != nil {
				return telegram.Reply{}, fmt.Errorf("photo download: %w", err)
			}
			text, imageURI = msg.Caption, uri
		}
		if text == "" && imageURI == "" {
			return telegram.Reply{Text: "Я понимаю текст и фотографии."}, nil
		}
		res, err := ag.Handle(ctx, msg.Chat.ID, text, imageURI)
		if err != nil {
			return telegram.Reply{}, err
		}
		return telegram.Reply{Text: res.Text, Options: res.Options, AllowCustom: res.AllowCustom}, nil
	})

	// telegram.New calls getMe under the hood — a bad token fails fast here,
	// before anything is served.
	bot, err = telegram.New(botToken, webhookSecret, allowed, handler, 4, slog.Default())
	if err != nil {
		slog.Error("telegram init failed (bad TELEGRAM_BOT_TOKEN?)", "err", err)
		os.Exit(1)
	}
	bot.OnNewSession(ag.Reset)

	me, err := bot.Me(ctx)
	if err != nil {
		slog.Error("telegram getMe failed", "err", err)
		os.Exit(1)
	}
	slog.Info("telegram bot authenticated", "username", me.Username, "id", me.ID)

	mux := http.NewServeMux()
	// The webhook path embeds the secret as a first barrier; the header check
	// inside the library's handler is the authoritative one.
	mux.Handle("/tg/"+webhookSecret, bot.WebhookHandler())

	// Liveness probe, deliberately unauthenticated so kamal-proxy/monitors/
	// Docker can reach it. Process health only — no upstream calls.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(map[string]any{
			"status": "ok",
			"build":  buildinfo.Get(),
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})

	// Optionally expose the MoySklad MCP surface to remote clients (Claude,
	// Antigravity) at /mcp. Opt-in and never anonymous: it is mounted only when
	// MCP_BEARER_TOKEN is set, and every request must present that token as a
	// Bearer credential. Without the env var the endpoint does not exist.
	if mcpToken := os.Getenv("MCP_BEARER_TOKEN"); mcpToken != "" {
		if len(mcpToken) < mcpTokenMinLen {
			slog.Error("MCP_BEARER_TOKEN too short — refusing to expose the MCP endpoint with a weak token", "min", mcpTokenMinLen)
			os.Exit(1)
		}
		mux.Handle("/mcp", bearerAuth(mcpToken, mcpserver.NewStreamableHTTP(api)))
		slog.Info("public MCP endpoint enabled", "path", "/mcp", "auth", "bearer")
	} else {
		slog.Info("public MCP endpoint disabled (set MCP_BEARER_TOKEN to enable)")
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		slog.Info("bot http server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	// Register the webhook only after the server is up, so Telegram's first
	// delivery attempt doesn't hit a closed port. Idempotent on re-deploys.
	webhookURL := publicBaseURL + "/tg/" + webhookSecret
	if err := bot.RegisterWebhook(ctx, webhookURL); err != nil {
		slog.Error("setWebhook failed", "err", err)
		os.Exit(1)
	}
	slog.Info("telegram webhook registered", "url", publicBaseURL+"/tg/***")

	workersDone := make(chan struct{})
	go func() { bot.StartWebhook(ctx); close(workersDone) }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("shutting down...")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
	cancel() // stop workers after the listener closed (no new updates)
	<-workersDone
}

// runStdio serves the MoySklad MCP server over stdio — the standalone test
// surface for the exact same mcpserver.New the bot's agent will dogfood.
func runStdio() {
	api, closeCache := buildAPI(requireEnv("MOYSKLAD_TOKEN"))
	defer closeCache()

	slog.Info("serving MoySklad MCP over stdio")
	if err := server.ServeStdio(mcpserver.New(api)); err != nil {
		slog.Error("stdio server error", "err", err)
		os.Exit(1)
	}
}

// runHealthCheck probes the local /healthz endpoint and returns an exit code
// (0 healthy, 1 not). It exists so a container HEALTHCHECK can run the binary
// itself — the distroless runtime image ships no shell or curl.
func runHealthCheck() int {
	host := envOr("LISTEN_ADDR", ":8080")
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + host + "/healthz")
	if err != nil {
		slog.Error("health check failed", "err", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		slog.Error("health check unhealthy", "status", resp.StatusCode)
		return 1
	}
	return 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
