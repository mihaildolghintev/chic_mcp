package phoenix

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestAnnotator points an Annotator at a stub server, bypassing NewFromEnv's
// environment reads.
func newTestAnnotator(endpoint string, headers map[string]string) *Annotator {
	return &Annotator{
		endpoint: endpoint,
		headers:  headers,
		client:   &http.Client{Timeout: 2 * time.Second},
	}
}

func TestAnnotatePostsWellFormedRequest(t *testing.T) {
	var (
		gotPath  string
		gotQuery string
		gotAuth  string
		body     wire
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("api_key")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := newTestAnnotator(srv.URL+"/v1/span_annotations", map[string]string{"api_key": "secret"})
	err := a.Annotate(context.Background(), Annotation{
		SpanID:     "0123456789abcdef",
		Name:       "user_feedback",
		Label:      "thumbs_down",
		Score:      0,
		Identifier: "100:0123456789abcdef",
		Metadata:   map[string]string{"user_id": "100"},
	})
	if err != nil {
		t.Fatalf("Annotate: %v", err)
	}

	if gotPath != "/v1/span_annotations" {
		t.Errorf("path = %q, want /v1/span_annotations", gotPath)
	}
	if gotQuery != "sync=false" {
		t.Errorf("query = %q, want sync=false", gotQuery)
	}
	if gotAuth != "secret" {
		t.Errorf("api_key header = %q, want secret", gotAuth)
	}
	if len(body.Data) != 1 {
		t.Fatalf("want 1 annotation, got %d", len(body.Data))
	}
	ann := body.Data[0]
	if ann.SpanID != "0123456789abcdef" || ann.Name != "user_feedback" || ann.AnnotatorKind != "HUMAN" {
		t.Errorf("annotation envelope wrong: %+v", ann)
	}
	if ann.Result.Label != "thumbs_down" || ann.Result.Score != 0 {
		t.Errorf("result = %+v, want thumbs_down/0", ann.Result)
	}
	if ann.Identifier != "100:0123456789abcdef" || ann.Metadata["user_id"] != "100" {
		t.Errorf("identifier/metadata wrong: %+v", ann)
	}
}

func TestAnnotateNoOpWhenDisabledOrBlankSpan(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	// Disabled annotator: no endpoint, must not call out.
	disabled := newTestAnnotator("", nil)
	if disabled.Enabled() {
		t.Error("empty-endpoint annotator must report disabled")
	}
	if err := disabled.Annotate(context.Background(), Annotation{SpanID: "abc"}); err != nil {
		t.Errorf("disabled Annotate should be a no-op, got %v", err)
	}

	// Enabled but blank span id: nothing to annotate, must not call out.
	enabled := newTestAnnotator(srv.URL, nil)
	if err := enabled.Annotate(context.Background(), Annotation{SpanID: ""}); err != nil {
		t.Errorf("blank-span Annotate should be a no-op, got %v", err)
	}
	if called {
		t.Error("no HTTP call expected for disabled/blank-span annotate")
	}
}

func TestAnnotateReturnsErrorOnRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte("span not found"))
	}))
	defer srv.Close()

	a := newTestAnnotator(srv.URL+"/v1/span_annotations", nil)
	err := a.Annotate(context.Background(), Annotation{SpanID: "abc", Name: "user_feedback"})
	if err == nil || !strings.Contains(err.Error(), "span not found") {
		t.Fatalf("want rejection error surfacing the body, got %v", err)
	}
}

func TestParseHeaders(t *testing.T) {
	got := parseHeaders("api_key=abc123, authorization=Bearer%20xyz ,,bad")
	if got["api_key"] != "abc123" {
		t.Errorf("api_key = %q, want abc123", got["api_key"])
	}
	if got["authorization"] != "Bearer xyz" {
		t.Errorf("authorization = %q, want percent-decoded 'Bearer xyz'", got["authorization"])
	}
	if _, ok := got["bad"]; ok {
		t.Error("valueless pair must be skipped")
	}
}
