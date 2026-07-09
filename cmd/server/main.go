// Command server runs the MoySklad MCP server. It supports two transports:
//
//	stdio — for local use: a client (Claude Desktop, Cursor) launches this
//	        binary and talks over stdin/stdout. No ports, tokens or TLS.
//	http  — Streamable HTTP for remote hosting, guarded by a static Bearer
//	        token and (optionally) single-user OAuth 2.1 for Claude.
//
// Select with -transport (or MCP_TRANSPORT); default is stdio.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"mcp.chic.md/internal/auth"
	"mcp.chic.md/internal/buildinfo"
	"mcp.chic.md/internal/cache"
	"mcp.chic.md/internal/mcpserver"
	"mcp.chic.md/internal/moysklad"
	"mcp.chic.md/internal/oauth"
)

func main() {
	slog.SetDefault(newLogger())

	transport := flag.String("transport", envOr("MCP_TRANSPORT", "stdio"), "transport: stdio or http")
	healthCheck := flag.Bool("health-check", false, "probe /healthz on the local server and exit 0/1 (for container HEALTHCHECK)")
	flag.Parse()

	if *healthCheck {
		os.Exit(runHealthCheck())
	}

	slog.Info("moysklad mcp starting", "build", buildinfo.Get(), "transport", *transport)

	token := os.Getenv("MOYSKLAD_TOKEN")
	if token == "" {
		slog.Error("MOYSKLAD_TOKEN is required")
		os.Exit(1)
	}

	api, closeCache := buildAPI(token)
	defer closeCache()

	switch *transport {
	case "stdio":
		runStdio(api)
	case "http":
		runHTTP(api)
	default:
		slog.Error("unknown transport", "transport", *transport, "want", "stdio or http")
		os.Exit(1)
	}
}

// newLogger builds the process logger. It always writes to stderr — in stdio
// transport the MCP protocol owns stdout, so logs must never touch it. Set
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

func runStdio(api mcpserver.MoyskladAPI) {
	slog.Info("serving MoySklad MCP over stdio")
	if err := server.ServeStdio(mcpserver.New(api)); err != nil {
		slog.Error("stdio server error", "err", err)
		os.Exit(1)
	}
}

func runHTTP(api mcpserver.MoyskladAPI) {
	bearer := os.Getenv("MCP_BEARER_TOKEN")
	if bearer == "" {
		slog.Error("MCP_BEARER_TOKEN is required for http transport (static Bearer token for simple clients)")
		os.Exit(1)
	}
	addr := envOr("LISTEN_ADDR", ":8080")
	publicBaseURL := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")

	mcpHandler := mcpserver.NewStreamableHTTP(api)
	mux := http.NewServeMux()

	// Liveness probe, deliberately unauthenticated so Caddy/monitors/Docker can
	// reach it without a token. It reports process health only (no upstream call
	// to MoySklad, which would burn the account rate limit on every probe).
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(map[string]any{
			"status": "ok",
			"build":  buildinfo.Get(),
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})

	authCfg := auth.Config{Verifiers: []auth.Verifier{auth.NewStaticToken(bearer)}}

	if publicBaseURL != "" && os.Getenv("OAUTH_PASSWORD") != "" {
		oauthSrv := oauth.New(oauth.Config{
			Issuer:   publicBaseURL,
			Password: os.Getenv("OAUTH_PASSWORD"),
		})
		oauthSrv.RegisterRoutes(mux)
		authCfg.Verifiers = append(authCfg.Verifiers, oauthSrv)
		authCfg.ResourceMetadataURL = publicBaseURL + "/.well-known/oauth-protected-resource"
		slog.Info("OAuth 2.1 enabled for Claude (discovery at /.well-known/oauth-protected-resource)")
	} else {
		slog.Info("OAuth disabled (set PUBLIC_BASE_URL and OAUTH_PASSWORD to enable Claude); static Bearer only")
	}

	mux.Handle("/mcp", auth.Middleware(authCfg)(mcpHandler))

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// Bound the header read to blunt Slowloris-style slow-header attacks.
		// ReadTimeout/WriteTimeout are intentionally left unset so long-lived
		// Streamable-HTTP (SSE) responses are not cut off mid-stream.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		slog.Info("MoySklad MCP server listening", "addr", addr, "endpoint", "/mcp")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
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
