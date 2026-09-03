package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPlanEmbeddingBatches(t *testing.T) {
	t.Parallel()

	latin := func(n int) string { return strings.Repeat("a", n) }
	repeat := func(text string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = text
		}
		return out
	}

	tests := []struct {
		name     string
		texts    []string
		maxCount int
		budget   int
		want     [][2]int
	}{
		{name: "empty", texts: nil, maxCount: 32, budget: 45_000, want: nil},
		{
			name:     "count cap only",
			texts:    repeat("hi", 70),
			maxCount: 32,
			budget:   45_000,
			want:     [][2]int{{0, 32}, {32, 64}, {64, 70}},
		},
		{
			// 350 Latin runes ≈ 100 tokens: budget 250 fits two per request.
			name:     "token budget splits before the count cap",
			texts:    repeat(latin(350), 5),
			maxCount: 32,
			budget:   250,
			want:     [][2]int{{0, 2}, {2, 4}, {4, 5}},
		},
		{
			// One input above the budget must travel alone, not drag the
			// neighbours into a rejected request.
			name:     "oversize input alone in its batch",
			texts:    []string{latin(35), latin(3500), latin(35), latin(35)},
			maxCount: 32,
			budget:   100,
			want:     [][2]int{{0, 1}, {1, 2}, {2, 4}},
		},
		{
			name:     "oversize input first",
			texts:    []string{latin(3500), latin(35)},
			maxCount: 32,
			budget:   100,
			want:     [][2]int{{0, 1}, {1, 2}},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := planEmbeddingBatches(tc.texts, tc.maxCount, tc.budget)
			if len(got) != len(tc.want) {
				t.Fatalf("batches = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("batches = %v, want %v", got, tc.want)
				}
			}
			for _, span := range got {
				if span[1]-span[0] > tc.maxCount {
					t.Fatalf("span %v exceeds count cap %d", span, tc.maxCount)
				}
				tokens := 0
				for _, text := range tc.texts[span[0]:span[1]] {
					tokens += EstimateEmbeddingTokens(text)
				}
				if tokens > tc.budget && span[1]-span[0] > 1 {
					t.Fatalf("span %v holds %d tokens over budget %d", span, tokens, tc.budget)
				}
			}
		})
	}
}

func TestEstimateEmbeddingTokensIsConservativeForCyrillic(t *testing.T) {
	t.Parallel()
	if got := EstimateEmbeddingTokens(""); got != 0 {
		t.Fatalf("empty text = %d tokens, want 0", got)
	}
	// 7 Latin runes / 3.5 → 2; the same rune count in Cyrillic / 2.5 → 3.
	if got := EstimateEmbeddingTokens("abcdefg"); got != 2 {
		t.Fatalf("latin = %d tokens, want 2", got)
	}
	if got := EstimateEmbeddingTokens("абвгдеж"); got != 3 {
		t.Fatalf("cyrillic = %d tokens, want 3", got)
	}
	if got := EstimateEmbeddingTokens("abcdef ж"); got != 4 {
		t.Fatalf("mixed text must use the Cyrillic ratio: got %d, want 4", got)
	}
}

func TestTruncateEmbeddingInputCutsOnRuneBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		text      string
		wantRunes int
		wantCut   bool
	}{
		{name: "short text untouched", text: "привет, world", wantRunes: 13, wantCut: false},
		{name: "latin at the cap untouched", text: strings.Repeat("a", 21_000), wantRunes: 21_000, wantCut: false},
		{name: "latin over the cap", text: strings.Repeat("a", 30_000), wantRunes: 21_000, wantCut: true},
		// Multi-byte runes: a byte-based cut would land mid-sequence.
		{name: "cyrillic over the cap", text: strings.Repeat("ж", 20_000), wantRunes: 15_000, wantCut: true},
		{name: "four-byte runes over the cap", text: strings.Repeat("😀", 25_000), wantRunes: 21_000, wantCut: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, cut := TruncateEmbeddingInput(tc.text)
			if cut != tc.wantCut {
				t.Fatalf("truncated = %v, want %v", cut, tc.wantCut)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncated text is not valid UTF-8")
			}
			if n := utf8.RuneCountInString(got); n != tc.wantRunes {
				t.Fatalf("rune count = %d, want %d", n, tc.wantRunes)
			}
			if !strings.HasPrefix(tc.text, got) {
				t.Fatalf("truncated text is not a prefix of the input")
			}
			if tokens := EstimateEmbeddingTokens(got); tokens > embeddingInputTokenCap {
				t.Fatalf("truncated text still estimates %d tokens > cap %d", tokens, embeddingInputTokenCap)
			}
		})
	}
}

// embeddingTestServer answers /embeddings with the scripted status codes in
// order (the last one repeats) and records every request's inputs.
type embeddingTestServer struct {
	mu       sync.Mutex
	statuses []int
	headers  []http.Header
	inputs   [][]string
	body     string
}

func (s *embeddingTestServer) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/embeddings" {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	call := len(s.inputs)
	s.inputs = append(s.inputs, req.Input)
	status := http.StatusOK
	if len(s.statuses) > 0 {
		idx := call
		if idx >= len(s.statuses) {
			idx = len(s.statuses) - 1
		}
		status = s.statuses[idx]
	}
	var header http.Header
	if call < len(s.headers) {
		header = s.headers[call]
	}
	body := s.body
	s.mu.Unlock()

	for key, values := range header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
		return
	}
	type item struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	}
	data := make([]item, len(req.Input))
	for i := range req.Input {
		data[i] = item{Embedding: []float32{float32(len(req.Input[i])), 1}, Index: i}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (s *embeddingTestServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inputs)
}

func newEmbeddingTestClient(t *testing.T, srv *embeddingTestServer) *EmbeddingClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(srv.handler))
	t.Cleanup(server.Close)
	client := NewEmbeddingClient(server.URL, "test-key", "test-model")
	client.backoff = []time.Duration{0, 0, 0}
	return client
}

func TestEmbeddingClientRetriesRateLimitHonouringRetryAfter(t *testing.T) {
	t.Parallel()

	srv := &embeddingTestServer{
		statuses: []int{http.StatusTooManyRequests, http.StatusOK},
		headers:  []http.Header{{"Retry-After": []string{"0"}}},
		body:     `{"errors":[{"code":2003,"message":"rate limited"}]}`,
	}
	client := newEmbeddingTestClient(t, srv)
	// A generous backoff would be used without Retry-After; the header must win.
	client.backoff = []time.Duration{time.Hour, time.Hour, time.Hour}

	done := make(chan struct{})
	var (
		vectors [][]float32
		err     error
	)
	go func() {
		defer close(done)
		vectors, err = client.GetEmbeddings(context.Background(), []string{"a", "b"})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("GetEmbeddings ignored Retry-After: 0 and slept on the backoff table")
	}
	if err != nil {
		t.Fatalf("GetEmbeddings error = %v", err)
	}
	if len(vectors) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vectors))
	}
	if calls := srv.calls(); calls != 2 {
		t.Fatalf("server calls = %d, want 2 (one 429 + one retry)", calls)
	}
}

func TestEmbeddingClientRetriesServerErrorsThenGivesUp(t *testing.T) {
	t.Parallel()

	t.Run("recovers within three attempts", func(t *testing.T) {
		t.Parallel()
		srv := &embeddingTestServer{statuses: []int{502, 503, http.StatusOK}, body: "upstream unavailable"}
		client := newEmbeddingTestClient(t, srv)
		if _, err := client.GetEmbeddings(context.Background(), []string{"a"}); err != nil {
			t.Fatalf("GetEmbeddings error = %v", err)
		}
		if calls := srv.calls(); calls != 3 {
			t.Fatalf("server calls = %d, want 3", calls)
		}
	})

	t.Run("gives up after three attempts", func(t *testing.T) {
		t.Parallel()
		srv := &embeddingTestServer{statuses: []int{http.StatusTooManyRequests}, body: "slow down"}
		client := newEmbeddingTestClient(t, srv)
		_, err := client.GetEmbeddings(context.Background(), []string{"a"})
		var apiErr *EmbeddingAPIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("expected a 429 EmbeddingAPIError, got %v", err)
		}
		if calls := srv.calls(); calls != embeddingMaxAttempts {
			t.Fatalf("server calls = %d, want %d", calls, embeddingMaxAttempts)
		}
	})
}

func TestEmbeddingClientClassifiesNonRetryableErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       int
		body         string
		wantTooLarge bool
	}{
		{
			name:         "cloudflare max context",
			status:       http.StatusBadRequest,
			body:         `{"errors":[{"code":3030,"message":"AiError: Max context reached 171168 tokens but model supports only 60000"}]}`,
			wantTooLarge: true,
		},
		{
			name:         "cloudflare sequence too long",
			status:       http.StatusBadRequest,
			body:         `{"errors":[{"code":3030,"message":"Sequence too long: 9000 > 8192"}]}`,
			wantTooLarge: true,
		},
		{
			name:         "payload too large",
			status:       http.StatusRequestEntityTooLarge,
			body:         `{"errors":[{"code":5021,"message":"Input too big"}]}`,
			wantTooLarge: true,
		},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":"invalid api key"}`, wantTooLarge: false},
		{name: "unrelated bad request", status: http.StatusBadRequest, body: `{"error":"unknown model"}`, wantTooLarge: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := &embeddingTestServer{statuses: []int{tc.status}, body: tc.body}
			client := newEmbeddingTestClient(t, srv)
			_, err := client.GetEmbeddings(context.Background(), []string{"a"})
			if err == nil {
				t.Fatalf("expected an error for status %d", tc.status)
			}
			if got := IsEmbeddingInputTooLarge(err); got != tc.wantTooLarge {
				t.Fatalf("IsEmbeddingInputTooLarge = %v, want %v (err=%v)", got, tc.wantTooLarge, err)
			}
			if calls := srv.calls(); calls != 1 {
				t.Fatalf("server calls = %d, want 1 (no retry for %d)", calls, tc.status)
			}
		})
	}
}

func TestEmbeddingClientTruncatesOversizeInputBeforeSending(t *testing.T) {
	t.Parallel()

	srv := &embeddingTestServer{}
	client := newEmbeddingTestClient(t, srv)
	vectors, err := client.GetEmbeddings(context.Background(), []string{strings.Repeat("a", 30_000), "short"})
	if err != nil {
		t.Fatalf("GetEmbeddings error = %v", err)
	}
	if len(vectors) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vectors))
	}
	srv.mu.Lock()
	sent := srv.inputs[0]
	srv.mu.Unlock()
	if n := utf8.RuneCountInString(sent[0]); n != 21_000 {
		t.Fatalf("server received %d runes for the oversize input, want 21000", n)
	}
	if sent[1] != "short" {
		t.Fatalf("short input was altered: %q", sent[1])
	}
}

func TestResolveEmbeddingAPIKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		baseURL   string
		memoryKey string
		aiKey     string
		want      string
		wantErr   bool
	}{
		{name: "memory key wins", baseURL: "https://api.cloudflare.com/client/v4/accounts/x/ai/v1", memoryKey: "cf-token", aiKey: "pool", want: "cf-token"},
		{name: "default endpoint may reuse ai key", baseURL: "", memoryKey: "", aiKey: "pool", want: "pool"},
		{name: "explicit openai endpoint may reuse ai key", baseURL: "https://api.openai.com/v1/", memoryKey: "", aiKey: "pool", want: "pool"},
		{name: "third-party endpoint refuses ai key", baseURL: "https://api.cloudflare.com/client/v4/accounts/x/ai/v1", memoryKey: "", aiKey: "pool", wantErr: true},
		{name: "local endpoint refuses ai key", baseURL: "http://localhost:11434/v1", memoryKey: "", aiKey: "pool", wantErr: true},
		{name: "no keys at all is not an error", baseURL: "http://localhost:11434/v1", memoryKey: "", aiKey: "", want: ""},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveEmbeddingAPIKey(tc.baseURL, tc.memoryKey, tc.aiKey)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got key %q", got)
				}
				if !strings.Contains(err.Error(), "OKGOBOT_MEMORY_EMBEDDINGS_API_KEY") {
					t.Fatalf("error should name the env var: %v", err)
				}
				if strings.Contains(err.Error(), tc.aiKey) {
					t.Fatalf("error must not echo the ai key: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("key = %q, want %q", got, tc.want)
			}
		})
	}
}
