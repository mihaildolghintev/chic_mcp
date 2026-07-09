package oauth

import (
	"crypto/subtle"
	"html/template"
	"net/http"
	"net/url"
)

// authParams are the OAuth authorization-request parameters we thread through
// the login form so a successful POST can mint a code for the original request.
type authParams struct {
	ClientID            string
	RedirectURI         string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	Resource            string
}

func (p authParams) hidden() map[string]string {
	return map[string]string{
		"client_id":             p.ClientID,
		"redirect_uri":          p.RedirectURI,
		"state":                 p.State,
		"code_challenge":        p.CodeChallenge,
		"code_challenge_method": p.CodeChallengeMethod,
		"scope":                 p.Scope,
		"resource":              p.Resource,
	}
}

func parseAuthParams(v url.Values) authParams {
	return authParams{
		ClientID:            v.Get("client_id"),
		RedirectURI:         v.Get("redirect_uri"),
		State:               v.Get("state"),
		CodeChallenge:       v.Get("code_challenge"),
		CodeChallengeMethod: v.Get("code_challenge_method"),
		Scope:               v.Get("scope"),
		Resource:            v.Get("resource"),
	}
}

// handleAuthorize renders the login form (GET) and issues an auth code on a
// correct password (POST).
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.authorizeGET(w, r)
	case http.MethodPost:
		s.authorizePOST(w, r)
	default:
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "GET or POST required")
	}
}

func (s *Server) authorizeGET(w http.ResponseWriter, r *http.Request) {
	p := parseAuthParams(r.URL.Query())
	if errCode, errDesc, ok := s.validateAuthRequest(p, r.URL.Query().Get("response_type")); !ok {
		writeOAuthError(w, http.StatusBadRequest, errCode, errDesc)
		return
	}
	renderLogin(w, p, false)
}

func (s *Server) authorizePOST(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form")
		return
	}
	p := parseAuthParams(r.PostForm)
	if errCode, errDesc, ok := s.validateAuthRequest(p, "code"); !ok {
		writeOAuthError(w, http.StatusBadRequest, errCode, errDesc)
		return
	}

	// Constant-time password check.
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("password")), []byte(s.cfg.Password)) != 1 {
		s.log().Warn("oauth login failed", "client_id", p.ClientID, "remote", r.RemoteAddr)
		renderLogin(w, p, true)
		return
	}

	code, err := randToken()
	if err != nil {
		s.redirectError(w, r, p, "server_error", "could not generate code")
		return
	}
	s.store.putCode(code, authCode{
		ClientID:      p.ClientID,
		RedirectURI:   p.RedirectURI,
		CodeChallenge: p.CodeChallenge,
		Resource:      p.Resource,
		Scope:         p.Scope,
		ExpiresAt:     s.cfg.Now().Add(s.cfg.CodeTTL),
	})

	// Redirect back to the client with the code (and state, if any).
	u, _ := url.Parse(p.RedirectURI)
	q := u.Query()
	q.Set("code", code)
	if p.State != "" {
		q.Set("state", p.State)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// validateAuthRequest checks the request against the registered client and the
// PKCE requirement. It returns an (errorCode, description, ok) triple.
func (s *Server) validateAuthRequest(p authParams, responseType string) (string, string, bool) {
	if responseType != "code" {
		return "unsupported_response_type", "only response_type=code is supported", false
	}
	c, ok := s.store.getClient(p.ClientID)
	if !ok {
		return "invalid_client", "unknown client_id", false
	}
	if !redirectAllowed(c.RedirectURIs, p.RedirectURI) {
		return "invalid_request", "redirect_uri does not match any registered URI", false
	}
	if p.CodeChallenge == "" || p.CodeChallengeMethod != "S256" {
		return "invalid_request", "PKCE with code_challenge_method=S256 is required", false
	}
	return "", "", true
}

func redirectAllowed(registered []string, uri string) bool {
	for _, r := range registered {
		if r == uri {
			return true
		}
	}
	return false
}

// redirectError sends an OAuth error back to the client redirect_uri when it is
// valid; otherwise it renders the error directly.
func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, p authParams, code, desc string) {
	c, ok := s.store.getClient(p.ClientID)
	if !ok || !redirectAllowed(c.RedirectURIs, p.RedirectURI) {
		writeOAuthError(w, http.StatusBadRequest, code, desc)
		return
	}
	u, _ := url.Parse(p.RedirectURI)
	q := u.Query()
	q.Set("error", code)
	q.Set("error_description", desc)
	if p.State != "" {
		q.Set("state", p.State)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>MoySklad MCP — Sign in</title>
<style>
  body { font-family: system-ui, sans-serif; background: #0f1115; color: #e6e6e6;
         display: grid; place-items: center; min-height: 100vh; margin: 0; }
  form { background: #1a1d24; padding: 2rem; border-radius: 12px; width: 320px;
         box-shadow: 0 8px 30px rgba(0,0,0,.4); }
  h1 { font-size: 1.1rem; margin: 0 0 1rem; }
  input[type=password] { width: 100%; padding: .6rem; border-radius: 8px;
         border: 1px solid #333; background: #0f1115; color: #e6e6e6; box-sizing: border-box; }
  button { margin-top: 1rem; width: 100%; padding: .6rem; border: 0; border-radius: 8px;
           background: #4c8bf5; color: #fff; font-weight: 600; cursor: pointer; }
  .err { color: #ff6b6b; font-size: .85rem; margin-top: .5rem; }
</style>
</head>
<body>
<form method="POST" action="/authorize">
  <h1>Authorize access to MoySklad MCP</h1>
  {{range $k, $v := .Hidden}}<input type="hidden" name="{{$k}}" value="{{$v}}">
  {{end}}
  <input type="password" name="password" placeholder="Password" autofocus required>
  {{if .Error}}<div class="err">Incorrect password.</div>{{end}}
  <button type="submit">Sign in</button>
</form>
</body>
</html>`))

func renderLogin(w http.ResponseWriter, p authParams, showErr bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = loginTmpl.Execute(w, struct {
		Hidden map[string]string
		Error  bool
	}{Hidden: p.hidden(), Error: showErr})
}
