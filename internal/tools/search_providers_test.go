package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

// searchTestContext allows the SSRF-safe transport to reach a local test server.
func searchTestContext() context.Context {
	return ContextWithNetworkPolicy(context.Background(), &CapabilityPolicy{AllowInternalNetworks: true})
}

func TestExaSearchRequestsContents(t *testing.T) {
	var body map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[]}`)
	}))
	defer server.Close()

	old := exaSearchBaseURL
	exaSearchBaseURL = server.URL
	defer func() { exaSearchBaseURL = old }()

	tool := &SearchTool{APIKey: "k", Engine: "exa"}
	if _, err := tool.Execute(searchTestContext(), "governance"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Without an explicit contents block Exa returns id/title/url only, which
	// renders every result with an empty snippet.
	contents, ok := body["contents"].(map[string]interface{})
	if !ok {
		t.Fatalf("request carried no contents block: %v", body)
	}
	if _, ok := contents["highlights"]; !ok {
		t.Fatalf("contents did not request highlights: %v", contents)
	}
}

func TestExaSearchRendersHighlights(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[
			{"title":"Compare","url":"https://forgejo.org/compare-to-gitea/","highlights":["Forgejo   was\ncreated in October 2022."],"text":"page opening"},
			{"title":"NoHighlight","url":"https://example.com/","text":"fallback body text"}
		]}`)
	}))
	defer server.Close()

	old := exaSearchBaseURL
	exaSearchBaseURL = server.URL
	defer func() { exaSearchBaseURL = old }()

	tool := &SearchTool{APIKey: "k", Engine: "exa"}
	out, err := tool.Execute(searchTestContext(), "governance")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Forgejo was created in October 2022.") {
		t.Fatalf("highlight not rendered (or whitespace not collapsed):\n%s", out)
	}
	if !strings.Contains(out, "fallback body text") {
		t.Fatalf("text fallback not used when highlights are absent:\n%s", out)
	}
}

func TestCollapseSearchSnippetCaps(t *testing.T) {
	for _, in := range []string{strings.Repeat("word ", 200), strings.Repeat("текст ", 200)} {
		got := collapseSearchSnippet(in)
		if n := utf8.RuneCountInString(got); n > 401 {
			t.Fatalf("snippet not capped: %d runes", n)
		}
		if !strings.HasSuffix(got, "…") {
			t.Fatalf("truncation not marked: %q", got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("truncation split a multi-byte rune: %q", got)
		}
	}
}

// Brave's free tier allows one request per second, so two searches in one model
// turn rate-limit each other. The retry must turn that into a normal result.
func TestBraveSearchRetriesAfterRateLimit(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"web":{"results":[{"title":"Result","url":"https://example.com","description":"desc"}]}}`)
	}))
	defer server.Close()

	old := braveSearchBaseURL
	braveSearchBaseURL = server.URL
	defer func() { braveSearchBaseURL = old }()

	tool := &SearchTool{APIKey: "k", Engine: "brave"}
	out, err := tool.Execute(searchTestContext(), "weather")
	if err != nil {
		t.Fatalf("Execute after 429 retry: %v", err)
	}
	if !strings.Contains(out, "Result") {
		t.Fatalf("retry result not rendered:\n%s", out)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2 (one 429 + one retry)", got)
	}
}

func TestBraveSearchGivesUpAfterSecondRateLimit(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	old := braveSearchBaseURL
	braveSearchBaseURL = server.URL
	defer func() { braveSearchBaseURL = old }()

	tool := &SearchTool{APIKey: "k", Engine: "brave"}
	_, err := tool.Execute(searchTestContext(), "weather")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want a 429 failure", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want exactly 2 — the retry must not loop", got)
	}
}
