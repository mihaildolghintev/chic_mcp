// Package oauth implements a minimal single-user OAuth 2.1 authorization server,
// just enough for Claude's remote-MCP flow: RFC 9728 protected-resource
// metadata, RFC 8414 authorization-server metadata, RFC 7591 dynamic client
// registration, a PKCE (S256) authorization-code grant behind a password login,
// and refresh tokens. Issued access tokens are validated by Verify, which
// satisfies the auth.Verifier interface used by the MCP endpoint middleware.
package oauth

import (
	"encoding/json"
	"net/http"
	"time"
)

// Config configures the OAuth server.
type Config struct {
	// Issuer is the public base URL, e.g. https://mcp.chic.md (no trailing /).
	Issuer string
	// Password is the single-user login secret shown on the /authorize form.
	Password string
	// AccessTokenTTL defaults to 1h if zero.
	AccessTokenTTL time.Duration
	// CodeTTL defaults to 5m if zero.
	CodeTTL time.Duration
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

// Server is the OAuth authorization server.
type Server struct {
	cfg   Config
	store *store
}

// New builds a Server, applying defaults.
func New(cfg Config) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.AccessTokenTTL == 0 {
		cfg.AccessTokenTTL = time.Hour
	}
	if cfg.CodeTTL == 0 {
		cfg.CodeTTL = 5 * time.Minute
	}
	return &Server{cfg: cfg, store: newStore(cfg.Now)}
}

// Verify implements auth.Verifier: true iff token is a live access token.
func (s *Server) Verify(token string) bool {
	return s.store.validAccess(token)
}

// RegisterRoutes mounts all OAuth endpoints (metadata, registration,
// authorize, token) on mux. These must NOT sit behind the bearer middleware.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/.well-known/oauth-protected-resource", s.handleProtectedResource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.handleAuthServerMetadata)
	// Some clients probe the OIDC discovery path; serve the same AS metadata.
	mux.HandleFunc("/.well-known/openid-configuration", s.handleAuthServerMetadata)
	mux.HandleFunc("/register", s.handleRegister)
	mux.HandleFunc("/authorize", s.handleAuthorize)
	mux.HandleFunc("/token", s.handleToken)
}

// handleProtectedResource serves RFC 9728 metadata pointing at this issuer as
// the authorization server for the MCP resource.
func (s *Server) handleProtectedResource(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":              s.cfg.Issuer,
		"authorization_servers": []string{s.cfg.Issuer},
	})
}

// handleAuthServerMetadata serves RFC 8414 authorization-server metadata.
func (s *Server) handleAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.cfg.Issuer,
		"authorization_endpoint":                s.cfg.Issuer + "/authorize",
		"token_endpoint":                        s.cfg.Issuer + "/token",
		"registration_endpoint":                 s.cfg.Issuer + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp"},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeOAuthError emits an RFC 6749 error response.
func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{
		"error":             code,
		"error_description": desc,
	})
}
