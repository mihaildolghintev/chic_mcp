// Package auth guards the MCP endpoint. Two client classes share one gate:
// simple clients (Antigravity, Cursor) send a static Bearer token, while Claude
// obtains a token through the OAuth 2.1 flow. Both arrive as `Authorization:
// Bearer <token>` and are checked against the same verifier chain, so adding
// OAuth later is just another Verifier.
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Verifier decides whether a bearer token is valid. Implementations must be
// safe for concurrent use.
type Verifier interface {
	// Verify reports whether token is valid. It should be constant-time with
	// respect to the secret to avoid leaking it through timing.
	Verify(token string) bool
}

// StaticToken accepts exactly one preconfigured token (the static Bearer for
// simple clients). Comparison is constant-time.
type StaticToken struct {
	token []byte
}

// NewStaticToken builds a verifier for a single shared secret.
func NewStaticToken(token string) *StaticToken {
	return &StaticToken{token: []byte(token)}
}

func (s *StaticToken) Verify(token string) bool {
	if len(s.token) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), s.token) == 1
}

// Config controls the auth middleware.
type Config struct {
	// Verifiers is the chain tried in order; the first to accept wins.
	Verifiers []Verifier
	// ResourceMetadataURL, when set, is advertised in the WWW-Authenticate
	// header on 401 so OAuth clients (Claude) can start discovery. It points at
	// /.well-known/oauth-protected-resource.
	ResourceMetadataURL string
}

// Middleware returns an http.Handler wrapper that requires a bearer token
// accepted by at least one verifier. On failure it returns 401 with a
// WWW-Authenticate header, which is what kicks off Claude's OAuth discovery.
func Middleware(cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if ok && anyAccepts(cfg.Verifiers, token) {
				next.ServeHTTP(w, r)
				return
			}
			writeUnauthorized(w, cfg.ResourceMetadataURL)
		})
	}
}

func anyAccepts(vs []Verifier, token string) bool {
	for _, v := range vs {
		if v.Verify(token) {
			return true
		}
	}
	return false
}

// bearerToken extracts the token from an Authorization: Bearer <token> header.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const prefix = "Bearer "
	// Scheme is case-insensitive per RFC 6750.
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

func writeUnauthorized(w http.ResponseWriter, resourceMetadataURL string) {
	challenge := "Bearer"
	if resourceMetadataURL != "" {
		challenge += ` resource_metadata="` + resourceMetadataURL + `"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"invalid_token","error_description":"missing or invalid bearer token"}`))
}
