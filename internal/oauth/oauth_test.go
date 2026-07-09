package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testRedirect = "https://claude.ai/api/mcp/auth_callback"

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := New(Config{Issuer: "https://mcp.chic.md", Password: "hunter2"})
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts
}

func decodeJSON(t *testing.T, r *http.Response, out any) {
	t.Helper()
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}

func pkcePair() (verifier, challenge string) {
	verifier = "abc123ABC456-def789_verifier-value-long-enough-01234567890"
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

func TestDiscoveryMetadata(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	decodeJSON(t, resp, &prm)
	if prm.Resource != "https://mcp.chic.md" {
		t.Errorf("resource = %q", prm.Resource)
	}
	if len(prm.AuthorizationServers) != 1 || prm.AuthorizationServers[0] != "https://mcp.chic.md" {
		t.Errorf("authorization_servers = %v", prm.AuthorizationServers)
	}

	resp2, err := http.Get(ts.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	var asm struct {
		Issuer                        string   `json:"issuer"`
		AuthorizationEndpoint         string   `json:"authorization_endpoint"`
		TokenEndpoint                 string   `json:"token_endpoint"`
		RegistrationEndpoint          string   `json:"registration_endpoint"`
		CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	}
	decodeJSON(t, resp2, &asm)
	if asm.TokenEndpoint != "https://mcp.chic.md/token" {
		t.Errorf("token_endpoint = %q", asm.TokenEndpoint)
	}
	if len(asm.CodeChallengeMethodsSupported) != 1 || asm.CodeChallengeMethodsSupported[0] != "S256" {
		t.Errorf("code_challenge_methods = %v", asm.CodeChallengeMethodsSupported)
	}
}

// registerClient runs DCR and returns the issued client_id.
func registerClient(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	body := `{"redirect_uris":["` + testRedirect + `"],"client_name":"Claude"}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d, want 201", resp.StatusCode)
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	decodeJSON(t, resp, &out)
	if out.ClientID == "" {
		t.Fatal("register returned empty client_id")
	}
	return out.ClientID
}

// noRedirectClient returns an HTTP client that does not follow redirects, so we
// can inspect the Location header from /authorize.
func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func TestFullAuthorizationCodeFlow(t *testing.T) {
	s, ts := newTestServer(t)
	clientID := registerClient(t, ts)
	verifier, challenge := pkcePair()

	// 1. GET /authorize renders the login form.
	authURL := ts.URL + "/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {testRedirect},
		"state":                 {"xyz"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {"mcp"},
		"resource":              {"https://mcp.chic.md"},
	}.Encode()
	getResp, err := http.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("authorize GET status = %d, want 200", getResp.StatusCode)
	}

	// 2. POST /authorize with the correct password -> redirect with code.
	form := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {testRedirect},
		"state":                 {"xyz"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {"mcp"},
		"resource":              {"https://mcp.chic.md"},
		"password":              {"hunter2"},
	}
	postResp, err := noRedirectClient().PostForm(ts.URL+"/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusFound {
		t.Fatalf("authorize POST status = %d, want 302", postResp.StatusCode)
	}
	loc, err := url.Parse(postResp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Query().Get("state") != "xyz" {
		t.Errorf("state = %q, want xyz", loc.Query().Get("state"))
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatal("no code in redirect")
	}

	// 3. POST /token exchanges the code (with PKCE verifier) for tokens.
	tokResp, err := http.PostForm(ts.URL+"/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {testRedirect},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	decodeJSON(t, tokResp, &tok)
	if tok.AccessToken == "" || tok.TokenType != "Bearer" {
		t.Fatalf("bad token response: %+v", tok)
	}

	// 4. The access token must validate against the server (auth.Verifier).
	if !s.Verify(tok.AccessToken) {
		t.Error("issued access token failed Verify")
	}
	if s.Verify("not-a-real-token") {
		t.Error("random token passed Verify")
	}

	// 5. The code is single-use.
	reuse, err := http.PostForm(ts.URL+"/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {testRedirect},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	})
	if err != nil {
		t.Fatal(err)
	}
	reuse.Body.Close()
	if reuse.StatusCode == http.StatusOK {
		t.Error("authorization code was accepted twice")
	}

	// 6. Refresh token yields a fresh, valid access token.
	refResp, err := http.PostForm(ts.URL+"/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	var refreshed struct {
		AccessToken string `json:"access_token"`
	}
	decodeJSON(t, refResp, &refreshed)
	if refreshed.AccessToken == "" || !s.Verify(refreshed.AccessToken) {
		t.Errorf("refresh did not yield a valid access token: %+v", refreshed)
	}
}

func TestAuthorize_WrongPasswordReRendersForm(t *testing.T) {
	_, ts := newTestServer(t)
	clientID := registerClient(t, ts)
	_, challenge := pkcePair()

	resp, err := noRedirectClient().PostForm(ts.URL+"/authorize", url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {testRedirect},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"password":              {"wrong"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Wrong password must NOT redirect (no code leaked).
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (form re-render)", resp.StatusCode)
	}
	if resp.Header.Get("Location") != "" {
		t.Error("wrong password produced a redirect")
	}
}

func TestToken_PKCEMismatchRejected(t *testing.T) {
	_, ts := newTestServer(t)
	clientID := registerClient(t, ts)
	_, challenge := pkcePair()

	resp, err := noRedirectClient().PostForm(ts.URL+"/authorize", url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {testRedirect},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"password":              {"hunter2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	code := mustLocationParam(t, resp, "code")

	bad, err := http.PostForm(ts.URL+"/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {testRedirect},
		"client_id":     {clientID},
		"code_verifier": {"the-wrong-verifier"},
	})
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode == http.StatusOK {
		t.Error("token endpoint accepted a bad PKCE verifier")
	}
}

func TestAuthorize_UnknownClientRejected(t *testing.T) {
	_, ts := newTestServer(t)
	_, challenge := pkcePair()
	resp, err := http.Get(ts.URL + "/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {"nope"},
		"redirect_uri":          {testRedirect},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown client", resp.StatusCode)
	}
}

func TestVerify_ExpiredToken(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := New(Config{Issuer: "https://mcp.chic.md", Password: "x", AccessTokenTTL: time.Hour, Now: func() time.Time { return now }})
	s.store.putAccess("tok", accessToken{ExpiresAt: now.Add(time.Hour)})
	if !s.Verify("tok") {
		t.Fatal("token should be valid before expiry")
	}
	now = now.Add(2 * time.Hour)
	if s.Verify("tok") {
		t.Error("token should be invalid after expiry")
	}
}

func mustLocationParam(t *testing.T, resp *http.Response, key string) string {
	t.Helper()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	v := loc.Query().Get(key)
	if v == "" {
		t.Fatalf("missing %q in redirect Location %q", key, resp.Header.Get("Location"))
	}
	return v
}
