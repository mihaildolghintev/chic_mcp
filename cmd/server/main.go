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
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"mcp.chic.md/internal/auth"
	"mcp.chic.md/internal/cache"
	"mcp.chic.md/internal/mcpserver"
	"mcp.chic.md/internal/moysklad"
	"mcp.chic.md/internal/oauth"
)

func main() {
	transport := flag.String("transport", envOr("MCP_TRANSPORT", "stdio"), "transport: stdio or http")
	flag.Parse()

	token := os.Getenv("MOYSKLAD_TOKEN")
	if token == "" {
		log.Fatal("MOYSKLAD_TOKEN is required")
	}

	api, closeCache := buildAPI(token)
	defer closeCache()

	switch *transport {
	case "stdio":
		runStdio(api)
	case "http":
		runHTTP(api)
	default:
		log.Fatalf("unknown transport %q (want stdio or http)", *transport)
	}
}

// buildAPI constructs the MoySklad API, wrapping it in a TTL cache when CACHE_DB
// is set. The returned close func tears the cache down (no-op when disabled).
func buildAPI(token string) (mcpserver.MoyskladAPI, func()) {
	client := moysklad.NewClient(token)
	var api mcpserver.MoyskladAPI = client

	dbPath := os.Getenv("CACHE_DB")
	if dbPath == "" {
		log.Print("response cache disabled (set CACHE_DB to enable)")
		return api, func() {}
	}
	store, err := cache.OpenStore(dbPath)
	if err != nil {
		log.Fatalf("cache: %v", err)
	}
	stopJanitor := store.StartJanitor(10 * time.Minute)
	log.Printf("response cache enabled at %s", dbPath)
	return cache.New(client, store, cache.DefaultTTLs()), func() {
		stopJanitor()
		_ = store.Close()
	}
}

// runStdio serves the MCP server over stdin/stdout. Note: the protocol owns
// stdout, so all logging must stay on stderr (the log package's default).
func runStdio(api mcpserver.MoyskladAPI) {
	log.Print("serving MoySklad MCP over stdio")
	if err := server.ServeStdio(mcpserver.New(api)); err != nil {
		log.Fatalf("stdio server error: %v", err)
	}
}

// runHTTP serves the Streamable HTTP transport with Bearer/OAuth auth.
func runHTTP(api mcpserver.MoyskladAPI) {
	bearer := os.Getenv("MCP_BEARER_TOKEN")
	if bearer == "" {
		log.Fatal("MCP_BEARER_TOKEN is required for http transport (static Bearer token for simple clients)")
	}
	addr := envOr("LISTEN_ADDR", ":8080")
	publicBaseURL := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")

	mcpHandler := mcpserver.NewStreamableHTTP(api)
	mux := http.NewServeMux()

	authCfg := auth.Config{Verifiers: []auth.Verifier{auth.NewStaticToken(bearer)}}

	if publicBaseURL != "" && os.Getenv("OAUTH_PASSWORD") != "" {
		oauthSrv := oauth.New(oauth.Config{
			Issuer:   publicBaseURL,
			Password: os.Getenv("OAUTH_PASSWORD"),
		})
		oauthSrv.RegisterRoutes(mux)
		authCfg.Verifiers = append(authCfg.Verifiers, oauthSrv)
		authCfg.ResourceMetadataURL = publicBaseURL + "/.well-known/oauth-protected-resource"
		log.Print("OAuth 2.1 enabled for Claude (discovery at /.well-known/oauth-protected-resource)")
	} else {
		log.Print("OAuth disabled (set PUBLIC_BASE_URL and OAUTH_PASSWORD to enable Claude); static Bearer only")
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
		log.Printf("MoySklad MCP server listening on %s (endpoint /mcp)", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
