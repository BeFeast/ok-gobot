package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const defaultEmbeddingsBaseURL = "https://api.openai.com/v1"

// Provider request caps, measured live against Cloudflare Workers AI
// (@cf/baai/bge-m3) on 2026-09-03: a request whose inputs total more than
// 60 000 tokens is rejected with HTTP 400 code 3030 ("Max context reached"),
// and a single input above 8 192 tokens with "Sequence too long". Both budgets
// below stay well under the caps because the token estimate is only an
// approximation; the same numbers are safe for OpenAI-compatible endpoints.
const (
	// embeddingRequestTokenBudget bounds the estimated tokens of one request.
	embeddingRequestTokenBudget = 45_000
	// embeddingInputTokenCap bounds the estimated tokens of one input; longer
	// text is embedded head-only so the chunk still gets a vector instead of
	// failing the whole file.
	embeddingInputTokenCap = 6_000
	// Runes per token at the conservative end of what was observed: Russian
	// tokenises at ~2.5–3.5 chars/token, English at ~4.
	cyrillicRunesPerToken = 2.5
	latinRunesPerToken    = 3.5

	// A 45k-token batch of long Russian chunks took ~2 s live, but the old
	// 30 s client timeout left no headroom for provider slowdowns.
	embeddingHTTPTimeout = 60 * time.Second
	embeddingMaxAttempts = 3
)

// embeddingRetryBackoff is used for 429/5xx responses without a Retry-After.
var embeddingRetryBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// EmbeddingAPIError is a non-200 response from the embedding endpoint.
type EmbeddingAPIError struct {
	StatusCode int
	Body       string
	// RetryAfter is the provider's Retry-After hint; HasRetryAfter reports
	// whether the header was present at all (a zero delay is a valid hint).
	RetryAfter    time.Duration
	HasRetryAfter bool
}

func (e *EmbeddingAPIError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Body)
}

// InputTooLarge reports whether the provider rejected the request because the
// inputs exceeded its context window, which a caller can fix by sending less.
func (e *EmbeddingAPIError) InputTooLarge() bool {
	if e == nil || (e.StatusCode != http.StatusBadRequest && e.StatusCode != http.StatusRequestEntityTooLarge) {
		return false
	}
	body := strings.ToLower(e.Body)
	for _, marker := range []string{"context", "too long", "input too big", "too large", "maximum"} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

// Retryable reports whether the response is transient (rate limit / server
// error) and worth retrying with backoff.
func (e *EmbeddingAPIError) Retryable() bool {
	return e != nil && (e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500)
}

// IsEmbeddingInputTooLarge reports whether err (possibly wrapped) is a
// provider rejection caused by oversized input.
func IsEmbeddingInputTooLarge(err error) bool {
	var apiErr *EmbeddingAPIError
	return errors.As(err, &apiErr) && apiErr.InputTooLarge()
}

// embeddingRunesPerToken returns the rune count of text and the chars/token
// ratio to apply to it (Cyrillic anywhere → the denser Cyrillic ratio).
func embeddingRunesPerToken(text string) (int, float64) {
	runes := 0
	cyrillic := false
	for _, r := range text {
		runes++
		if !cyrillic && unicode.Is(unicode.Cyrillic, r) {
			cyrillic = true
		}
	}
	if cyrillic {
		return runes, cyrillicRunesPerToken
	}
	return runes, latinRunesPerToken
}

// EstimateEmbeddingTokens approximates the provider token count of text.
// It deliberately overestimates so the request budget is never exceeded.
func EstimateEmbeddingTokens(text string) int {
	runes, perToken := embeddingRunesPerToken(text)
	if runes == 0 {
		return 0
	}
	return int(math.Ceil(float64(runes) / perToken))
}

// TruncateEmbeddingInput caps text at embeddingInputTokenCap estimated tokens.
// The cut lands on a rune boundary so the provider never receives a broken
// UTF-8 sequence. The second result reports whether anything was removed.
func TruncateEmbeddingInput(text string) (string, bool) {
	runes, perToken := embeddingRunesPerToken(text)
	allowedRunes := int(float64(embeddingInputTokenCap) * perToken)
	if runes <= allowedRunes {
		return text, false
	}
	seen := 0
	for byteIdx := range text {
		if seen == allowedRunes {
			return text[:byteIdx], true
		}
		seen++
	}
	return text, false
}

// planEmbeddingBatches splits texts into contiguous [start, end) ranges that
// respect both a per-request element cap and a per-request token budget. A
// single text above the budget is placed alone in its own batch; it is the
// caller's job to truncate such inputs beforehand.
func planEmbeddingBatches(texts []string, maxCount, tokenBudget int) [][2]int {
	if len(texts) == 0 {
		return nil
	}
	if maxCount <= 0 {
		maxCount = defaultEmbeddingBatchSize
	}
	if tokenBudget <= 0 {
		tokenBudget = embeddingRequestTokenBudget
	}

	batches := make([][2]int, 0, len(texts)/maxCount+1)
	start := 0
	tokens := 0
	for idx, text := range texts {
		cost := EstimateEmbeddingTokens(text)
		if idx > start && (idx-start >= maxCount || tokens+cost > tokenBudget) {
			batches = append(batches, [2]int{start, idx})
			start = idx
			tokens = 0
		}
		tokens += cost
	}
	return append(batches, [2]int{start, len(texts)})
}

// ResolveEmbeddingAPIKey picks the key sent to the embedding endpoint.
// memory.embeddings_api_key always wins. Falling back to ai.api_key is only
// allowed for the default OpenAI endpoint: that key is the chat-provider pool
// key and sending it to a third-party embeddings host (Cloudflare, a local
// server) would leak it to a provider it was never meant for.
func ResolveEmbeddingAPIKey(baseURL, memoryAPIKey, aiAPIKey string) (string, error) {
	if key := strings.TrimSpace(memoryAPIKey); key != "" {
		return key, nil
	}
	aiAPIKey = strings.TrimSpace(aiAPIKey)
	if aiAPIKey == "" {
		return "", nil
	}
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if normalized == "" || strings.EqualFold(normalized, defaultEmbeddingsBaseURL) {
		return aiAPIKey, nil
	}
	return "", fmt.Errorf("memory.embeddings_api_key (OKGOBOT_MEMORY_EMBEDDINGS_API_KEY) is empty while memory.embeddings_base_url=%s; refusing to send ai.api_key to a non-OpenAI embeddings endpoint, set the memory key explicitly", normalized)
}

// EmbeddingClient handles communication with embedding API
type EmbeddingClient struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client

	// backoff holds the transient-error delays; tests shorten them.
	backoff []time.Duration
}

// NewEmbeddingClient creates a new embedding client
func NewEmbeddingClient(baseURL, apiKey, model string) *EmbeddingClient {
	if model == "" {
		model = "text-embedding-3-small"
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultEmbeddingsBaseURL
	}
	return &EmbeddingClient{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  apiKey,
		model:   model,
		httpClient: &http.Client{
			Timeout: embeddingHTTPTimeout,
		},
		backoff: embeddingRetryBackoff,
	}
}

// EmbeddingProviderConfigured reports whether configuration points at a usable
// embedding provider. The default OpenAI endpoint requires an API key, while
// custom OpenAI-compatible local endpoints may intentionally use no key.
func EmbeddingProviderConfigured(baseURL, apiKey string) bool {
	if strings.TrimSpace(apiKey) != "" {
		return true
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return baseURL != "" && !strings.EqualFold(baseURL, defaultEmbeddingsBaseURL)
}

// embeddingRequest represents the API request body
type embeddingRequest struct {
	Input interface{} `json:"input"`
	Model string      `json:"model"`
}

// embeddingResponse represents the API response
type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// GetEmbedding returns the embedding vector for the given text
func (c *EmbeddingClient) GetEmbedding(ctx context.Context, text string) ([]float32, error) {
	embeddings, err := c.GetEmbeddings(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 || len(embeddings[0]) == 0 {
		return nil, fmt.Errorf("no embedding returned from API")
	}
	return embeddings[0], nil
}

// GetEmbeddings returns embedding vectors for the given texts in one request.
// Every input is capped at embeddingInputTokenCap (head-only) so no caller can
// trip the per-input provider limit; staying under the per-request budget is
// the caller's job (see planEmbeddingBatches). Rate-limit and server errors
// are retried with backoff; any other non-200 response is returned as an
// *EmbeddingAPIError.
func (c *EmbeddingClient) GetEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
	// Nil-receiver guard: a typed-nil *EmbeddingClient stored in an interface
	// passes interface == nil checks upstream; fail explicitly instead of
	// dereferencing nil (this crashed production startup indexing).
	if c == nil {
		return nil, fmt.Errorf("embedding client not configured")
	}
	if len(texts) == 0 {
		return nil, nil
	}

	inputs := make([]string, len(texts))
	for i, text := range texts {
		inputs[i], _ = TruncateEmbeddingInput(text)
	}

	jsonData, err := json.Marshal(embeddingRequest{Input: inputs, Model: c.model})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	var body []byte
	for attempt := 0; ; attempt++ {
		body, err = c.post(ctx, jsonData)
		if err == nil {
			break
		}
		var apiErr *EmbeddingAPIError
		if !errors.As(err, &apiErr) || !apiErr.Retryable() || attempt+1 >= embeddingMaxAttempts {
			return nil, err
		}
		if waitErr := sleepContext(ctx, c.retryDelay(apiErr, attempt)); waitErr != nil {
			return nil, waitErr
		}
	}

	var result embeddingResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if result.Error != nil {
		return nil, fmt.Errorf("API error: %s", result.Error.Message)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned from API")
	}

	if len(result.Data) != len(texts) {
		return nil, fmt.Errorf("embedding count mismatch: got %d, want %d", len(result.Data), len(texts))
	}

	embeddings := make([][]float32, len(texts))
	allIndexed := true
	for _, item := range result.Data {
		if item.Index < 0 || item.Index >= len(texts) {
			allIndexed = false
			break
		}
		embeddings[item.Index] = item.Embedding
	}

	if !allIndexed {
		for i := range result.Data {
			embeddings[i] = result.Data[i].Embedding
		}
	}

	for i := range embeddings {
		if len(embeddings[i]) == 0 {
			return nil, fmt.Errorf("missing embedding at index %d", i)
		}
	}

	return embeddings, nil
}

// post performs one embeddings request and returns the body on HTTP 200.
func (c *EmbeddingClient) post(ctx context.Context, jsonData []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &EmbeddingAPIError{StatusCode: resp.StatusCode, Body: string(body)}
		apiErr.RetryAfter, apiErr.HasRetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, apiErr
	}
	return body, nil
}

func (c *EmbeddingClient) retryDelay(apiErr *EmbeddingAPIError, attempt int) time.Duration {
	if apiErr.HasRetryAfter {
		return apiErr.RetryAfter
	}
	backoff := c.backoff
	if len(backoff) == 0 {
		backoff = embeddingRetryBackoff
	}
	if attempt >= len(backoff) {
		attempt = len(backoff) - 1
	}
	return backoff[attempt]
}

// parseRetryAfter understands both header forms: delay-seconds and HTTP-date.
func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds < 0 {
			seconds = 0
		}
		return time.Duration(seconds * float64(time.Second)), true
	}
	if at, err := http.ParseTime(value); err == nil {
		delay := time.Until(at)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
