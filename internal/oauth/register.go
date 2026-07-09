package oauth

import (
	"encoding/json"
	"net/http"
)

// registrationRequest is the subset of RFC 7591 we consume. Being single-user,
// we accept any well-formed registration and issue a public client_id.
type registrationRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

// handleRegister implements Dynamic Client Registration. It returns a client_id
// with no secret (public client using PKCE), echoing the registered metadata.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "POST required")
		return
	}
	var req registrationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "malformed JSON body")
		return
	}
	if len(req.RedirectURIs) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "at least one redirect_uri is required")
		return
	}

	id, err := randToken()
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not generate client id")
		return
	}

	s.store.addClient(client{
		ID:           id,
		RedirectURIs: req.RedirectURIs,
		Name:         req.ClientName,
	})

	s.log().Info("oauth client registered", "client_id", id, "name", req.ClientName)

	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  id,
		"redirect_uris":              req.RedirectURIs,
		"client_name":                req.ClientName,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}
