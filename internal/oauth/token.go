package oauth

import (
	"net/http"
)

// handleToken implements the token endpoint for the authorization_code and
// refresh_token grants. Public client, so no client authentication — the code
// is bound to the client via PKCE.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "POST required")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form")
		return
	}
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		s.grantAuthorizationCode(w, r)
	case "refresh_token":
		s.grantRefreshToken(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant_type")
	}
}

func (s *Server) grantAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	code := r.PostFormValue("code")
	ac, ok := s.store.takeCode(code)
	if !ok {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or expired authorization code")
		return
	}
	// The redirect_uri and client_id must match those from /authorize.
	if r.PostFormValue("redirect_uri") != ac.RedirectURI {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}
	if cid := r.PostFormValue("client_id"); cid != "" && cid != ac.ClientID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client_id mismatch")
		return
	}
	if !verifyPKCE(ac.CodeChallenge, r.PostFormValue("code_verifier")) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	s.issueTokens(w, ac.ClientID, ac.Resource, ac.Scope)
}

func (s *Server) grantRefreshToken(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.store.getRefresh(r.PostFormValue("refresh_token"))
	if !ok {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown refresh token")
		return
	}
	s.issueTokens(w, rt.ClientID, rt.Resource, rt.Scope)
}

// issueTokens mints an access token (+ refresh token) and writes the RFC 6749
// token response.
func (s *Server) issueTokens(w http.ResponseWriter, clientID, resource, scope string) {
	at, err := randToken()
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "token generation failed")
		return
	}
	rtok, err := randToken()
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "token generation failed")
		return
	}

	s.store.putAccess(at, accessToken{
		Resource:  resource,
		Scope:     scope,
		ExpiresAt: s.cfg.Now().Add(s.cfg.AccessTokenTTL),
	})
	s.store.putRefresh(rtok, refreshToken{
		ClientID: clientID,
		Resource: resource,
		Scope:    scope,
	})

	resp := map[string]any{
		"access_token":  at,
		"token_type":    "Bearer",
		"expires_in":    int(s.cfg.AccessTokenTTL.Seconds()),
		"refresh_token": rtok,
	}
	if scope != "" {
		resp["scope"] = scope
	}
	writeJSON(w, http.StatusOK, resp)
}
