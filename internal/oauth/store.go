package oauth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// client is a registered OAuth client (via Dynamic Client Registration).
type client struct {
	ID           string
	RedirectURIs []string
	Name         string
}

// authCode is a short-lived, single-use authorization code bound to the request
// that created it (PKCE challenge, redirect_uri, resource).
type authCode struct {
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Resource      string
	Scope         string
	ExpiresAt     time.Time
}

// accessToken is an issued bearer token with an expiry.
type accessToken struct {
	Resource  string
	Scope     string
	ExpiresAt time.Time
}

// refreshToken mints new access tokens without re-login.
type refreshToken struct {
	ClientID string
	Resource string
	Scope    string
}

// store holds all OAuth state in memory. Single-user, so the volume is tiny;
// the trade-off is that a restart invalidates issued tokens and Claude must
// re-authorize. A SQLite-backed store can replace this behind the same methods.
type store struct {
	mu      sync.Mutex
	clients map[string]client
	codes   map[string]authCode
	access  map[string]accessToken
	refresh map[string]refreshToken
	now     func() time.Time
}

func newStore(now func() time.Time) *store {
	return &store{
		clients: make(map[string]client),
		codes:   make(map[string]authCode),
		access:  make(map[string]accessToken),
		refresh: make(map[string]refreshToken),
		now:     now,
	}
}

func (s *store) addClient(c client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[c.ID] = c
}

func (s *store) getClient(id string) (client, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[id]
	return c, ok
}

func (s *store) putCode(code string, ac authCode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code] = ac
}

// takeCode atomically returns and deletes a code (single-use), reporting
// whether it existed and was unexpired.
func (s *store) takeCode(code string) (authCode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ac, ok := s.codes[code]
	if !ok {
		return authCode{}, false
	}
	delete(s.codes, code)
	if s.now().After(ac.ExpiresAt) {
		return authCode{}, false
	}
	return ac, true
}

func (s *store) putAccess(token string, at accessToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access[token] = at
}

func (s *store) putRefresh(token string, rt refreshToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh[token] = rt
}

func (s *store) getRefresh(token string) (refreshToken, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.refresh[token]
	return rt, ok
}

// validAccess reports whether an access token is known and unexpired.
func (s *store) validAccess(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.access[token]
	if !ok {
		return false
	}
	return !s.now().After(at.ExpiresAt)
}

// randToken returns a cryptographically-random URL-safe token.
func randToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
