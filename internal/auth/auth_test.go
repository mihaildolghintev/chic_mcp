package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestStaticToken_Verify(t *testing.T) {
	v := NewStaticToken("s3cret")
	if !v.Verify("s3cret") {
		t.Error("valid token rejected")
	}
	if v.Verify("wrong") {
		t.Error("invalid token accepted")
	}
	if v.Verify("") {
		t.Error("empty token accepted")
	}
	if NewStaticToken("").Verify("") {
		t.Error("empty configured secret must reject everything")
	}
}

func TestMiddleware_AcceptsValidBearer(t *testing.T) {
	mw := Middleware(Config{Verifiers: []Verifier{NewStaticToken("s3cret")}})
	srv := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestMiddleware_CaseInsensitiveScheme(t *testing.T) {
	mw := Middleware(Config{Verifiers: []Verifier{NewStaticToken("s3cret")}})
	srv := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "bearer s3cret")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for lowercase scheme", rec.Code)
	}
}

func TestMiddleware_RejectsMissingAndBad(t *testing.T) {
	mw := Middleware(Config{
		Verifiers:           []Verifier{NewStaticToken("s3cret")},
		ResourceMetadataURL: "https://mcp.chic.md/.well-known/oauth-protected-resource",
	})
	srv := mw(okHandler())

	cases := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"wrong token", "Bearer nope"},
		{"not bearer", "Basic Zm9vOmJhcg=="},
		{"empty token", "Bearer "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			// The WWW-Authenticate header drives Claude's OAuth discovery.
			want := `Bearer resource_metadata="https://mcp.chic.md/.well-known/oauth-protected-resource"`
			if got := rec.Header().Get("WWW-Authenticate"); got != want {
				t.Errorf("WWW-Authenticate = %q, want %q", got, want)
			}
		})
	}
}

func TestMiddleware_MultipleVerifiers(t *testing.T) {
	// Simulates the static-token + OAuth-token chain: either accepts.
	oauthLike := verifierFunc(func(tok string) bool { return tok == "oauth-issued" })
	mw := Middleware(Config{Verifiers: []Verifier{NewStaticToken("static"), oauthLike}})
	srv := mw(okHandler())

	for _, tok := range []string{"static", "oauth-issued"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("token %q: status = %d, want 200", tok, rec.Code)
		}
	}
}

type verifierFunc func(string) bool

func (f verifierFunc) Verify(t string) bool { return f(t) }
