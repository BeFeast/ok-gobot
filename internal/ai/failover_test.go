package ai

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeStreamingClient struct {
	chunks      []StreamChunk
	streamCalls atomic.Int32
}

type fakeCompleteClient struct {
	err               error
	completeCalls     atomic.Int32
	completeToolCalls atomic.Int32
}

func (c *fakeCompleteClient) Complete(context.Context, []Message) (string, error) {
	c.completeCalls.Add(1)
	return "", c.err
}

func (c *fakeCompleteClient) CompleteWithTools(context.Context, []ChatMessage, []ToolDefinition) (*ChatCompletionResponse, error) {
	c.completeToolCalls.Add(1)
	return nil, c.err
}

func (c *fakeStreamingClient) Complete(context.Context, []Message) (string, error) {
	return "", errors.New("unexpected non-streaming call")
}

func (c *fakeStreamingClient) CompleteWithTools(context.Context, []ChatMessage, []ToolDefinition) (*ChatCompletionResponse, error) {
	return nil, errors.New("unexpected non-streaming call")
}

func (c *fakeStreamingClient) CompleteStream(context.Context, []Message) <-chan StreamChunk {
	c.streamCalls.Add(1)
	return fakeStream(c.chunks)
}

func (c *fakeStreamingClient) CompleteStreamWithTools(context.Context, []ChatMessage, []ToolDefinition) <-chan StreamChunk {
	c.streamCalls.Add(1)
	return fakeStream(c.chunks)
}

func fakeStream(chunks []StreamChunk) <-chan StreamChunk {
	ch := make(chan StreamChunk, len(chunks))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return ch
}

func fakeFailoverClient(primary, fallback Client) *FailoverClient {
	return &FailoverClient{
		entries: []failoverEntry{
			{model: "primary", client: primary},
			{model: "fallback", client: fallback},
		},
		cooldowns: make(map[string]time.Time),
	}
}

func drainFailoverStream(ch <-chan StreamChunk) (content, finishReason string, err error) {
	var text strings.Builder
	for chunk := range ch {
		text.WriteString(chunk.Content)
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
		if chunk.Error != nil {
			return text.String(), finishReason, chunk.Error
		}
		if chunk.Done {
			return text.String(), finishReason, nil
		}
	}
	return text.String(), finishReason, nil
}

func TestFailoverClientStreamingFallsBackBeforeContent(t *testing.T) {
	starts := []struct {
		name string
		run  func(*FailoverClient) <-chan StreamChunk
	}{
		{
			name: "complete stream",
			run: func(fc *FailoverClient) <-chan StreamChunk {
				return fc.CompleteStream(context.Background(), []Message{{Role: RoleUser, Content: "hello"}})
			},
		},
		{
			name: "complete stream with tools",
			run: func(fc *FailoverClient) <-chan StreamChunk {
				return fc.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "hello"}}, nil)
			},
		},
	}

	for _, start := range starts {
		t.Run(start.name, func(t *testing.T) {
			primary := &fakeStreamingClient{chunks: []StreamChunk{{Error: &BackendHTTPError{Provider: "ChatGPT", StatusCode: 503}}}}
			fallback := &fakeStreamingClient{chunks: []StreamChunk{{Content: "fallback answer", Done: true, FinishReason: "stop"}}}
			fc := fakeFailoverClient(primary, fallback)

			content, finishReason, err := drainFailoverStream(start.run(fc))
			if err != nil {
				t.Fatalf("stream error = %v", err)
			}
			if content != "fallback answer" || finishReason != "stop" {
				t.Fatalf("content=%q finishReason=%q", content, finishReason)
			}
			if primary.streamCalls.Load() != 1 || fallback.streamCalls.Load() != 1 {
				t.Fatalf("stream calls primary=%d fallback=%d", primary.streamCalls.Load(), fallback.streamCalls.Load())
			}
			if decision := fc.LastFallbackDecision(); decision.Action != FallbackActionFallback || decision.ToModel != "fallback" {
				t.Fatalf("fallback decision = %+v", decision)
			}
		})
	}
}

func TestFailoverClientStreamingKeepsPartialWithoutSwitching(t *testing.T) {
	primary := &fakeStreamingClient{chunks: []StreamChunk{
		{Content: "partial answer"},
		{Error: &BackendHTTPError{Provider: "ChatGPT", StatusCode: 503}},
	}}
	fallback := &fakeStreamingClient{chunks: []StreamChunk{{Content: "must not leak", Done: true}}}
	fc := fakeFailoverClient(primary, fallback)

	content, finishReason, err := drainFailoverStream(fc.CompleteStreamWithTools(context.Background(), nil, nil))
	if err != nil {
		t.Fatalf("stream error = %v", err)
	}
	if content != "partial answer" || finishReason != "incomplete" {
		t.Fatalf("content=%q finishReason=%q", content, finishReason)
	}
	if fallback.streamCalls.Load() != 0 {
		t.Fatalf("fallback stream calls = %d, want 0", fallback.streamCalls.Load())
	}
	if decision := fc.LastFallbackDecision(); decision.Action != FallbackActionStop || !strings.Contains(decision.Reason, "partial response") {
		t.Fatalf("fallback decision = %+v", decision)
	}
}

func TestTrackedFailoverRecordsPrimaryFailureThenFallbackSuccess(t *testing.T) {
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider:        ProviderConfig{Name: "chatgpt", Model: "primary"},
		FallbackModels:  []string{"fallback"},
		FallbackEnabled: true,
	})
	primary := &fakeStreamingClient{chunks: []StreamChunk{{Error: &BackendHTTPError{Provider: "ChatGPT", StatusCode: 503}}}}
	fallback := &fakeStreamingClient{chunks: []StreamChunk{{Content: "fallback answer", Done: true, FinishReason: "stop"}}}
	fc := fakeFailoverClient(primary, fallback)
	tracked, ok := TrackClient(fc, BackendIdentity{
		Provider: "chatgpt",
		Backend:  "chatgpt",
		Model:    "primary",
		Tier:     "agent",
		Effort:   "high",
	}, checker.RecordRuntimeOutcome).(*FailoverClient)
	if !ok {
		t.Fatalf("tracked failover type = %T, want *FailoverClient", tracked)
	}

	content, finishReason, err := drainFailoverStream(tracked.CompleteStreamWithTools(context.Background(), nil, nil))
	if err != nil || content != "fallback answer" || finishReason != "stop" {
		t.Fatalf("content=%q finish=%q err=%v", content, finishReason, err)
	}
	health := checker.Snapshot()
	if health.Status != BackendHealthHealthy || health.Identity.Model != "fallback" {
		t.Fatalf("final health = %+v, want healthy fallback", health)
	}
	if health.Fallback.Action != FallbackActionFallback || health.Fallback.FromModel != "primary" || health.Fallback.ToModel != "fallback" {
		t.Fatalf("runtime fallback decision = %+v", health.Fallback)
	}
}

func TestTrackedFailoverExhaustionDoesNotAdvertiseAnotherFallback(t *testing.T) {
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider:        ProviderConfig{Name: "chatgpt", Model: "primary"},
		FallbackModels:  []string{"fallback"},
		FallbackEnabled: true,
	})
	primary := &fakeStreamingClient{chunks: []StreamChunk{{Error: &BackendHTTPError{Provider: "ChatGPT", StatusCode: 503}}}}
	fallback := &fakeStreamingClient{chunks: []StreamChunk{{Error: &BackendHTTPError{Provider: "ChatGPT", StatusCode: 503}}}}
	tracked := TrackClient(fakeFailoverClient(primary, fallback), BackendIdentity{
		Provider: "chatgpt",
		Backend:  "chatgpt",
		Model:    "primary",
	}, checker.RecordRuntimeOutcome).(*FailoverClient)

	_, _, err := drainFailoverStream(tracked.CompleteStreamWithTools(context.Background(), nil, nil))
	if err == nil {
		t.Fatal("exhausted failover stream succeeded")
	}
	health := checker.Snapshot()
	if health.Identity.Model != "fallback" || health.Status != BackendHealthUnavailable {
		t.Fatalf("exhausted fallback health = %+v", health)
	}
	if health.Fallback.Action != FallbackActionStop || health.Fallback.ToModel != "" || !strings.Contains(health.Fallback.Reason, "exhausted") {
		t.Fatalf("exhausted fallback decision = %+v", health.Fallback)
	}
}

func TestTrackedFailoverRecordsIncompletePartialAsUnavailable(t *testing.T) {
	checker := NewBackendPreflight(BackendPreflightConfig{
		Provider:        ProviderConfig{Name: "chatgpt", Model: "primary"},
		FallbackModels:  []string{"fallback"},
		FallbackEnabled: true,
	})
	primary := &fakeStreamingClient{chunks: []StreamChunk{
		{Content: "partial answer"},
		{Done: true, FinishReason: "incomplete"},
	}}
	fallback := &fakeStreamingClient{chunks: []StreamChunk{{Content: "must not run", Done: true}}}
	tracked := TrackClient(fakeFailoverClient(primary, fallback), BackendIdentity{
		Provider: "chatgpt",
		Backend:  "chatgpt",
		Model:    "primary",
	}, checker.RecordRuntimeOutcome).(*FailoverClient)

	content, finishReason, err := drainFailoverStream(tracked.CompleteStreamWithTools(context.Background(), nil, nil))
	if err != nil || content != "partial answer" || finishReason != "incomplete" {
		t.Fatalf("content=%q finish=%q err=%v", content, finishReason, err)
	}
	if fallback.streamCalls.Load() != 0 {
		t.Fatalf("fallback calls = %d, want no splice after partial content", fallback.streamCalls.Load())
	}
	health := checker.Snapshot()
	if health.Status != BackendHealthUnavailable || health.FailureKind != BackendFailureUnavailable {
		t.Fatalf("incomplete health = %+v", health)
	}
	if health.Fallback.Action != FallbackActionStop || !strings.Contains(health.Fallback.Reason, "partial response") {
		t.Fatalf("incomplete fallback decision = %+v", health.Fallback)
	}
}

func TestTrackedStreamExpiredDeadlineOverridesSuccessfulTerminalChunk(t *testing.T) {
	checker := NewBackendPreflight(BackendPreflightConfig{Provider: ProviderConfig{Name: "chatgpt", Model: "primary"}})
	client := TrackClient(&fakeStreamingClient{chunks: []StreamChunk{{Content: "buffered", Done: true, FinishReason: "stop"}}}, BackendIdentity{
		Provider: "chatgpt",
		Backend:  "chatgpt",
		Model:    "primary",
	}, checker.RecordRuntimeOutcome).(StreamingClient)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	content, finishReason, err := drainFailoverStream(client.CompleteStream(ctx, nil))
	if err != nil || content != "buffered" || finishReason != "stop" {
		t.Fatalf("content=%q finish=%q err=%v", content, finishReason, err)
	}
	if health := checker.Snapshot(); health.Status != BackendHealthUnavailable {
		t.Fatalf("expired-deadline health = %+v, want unavailable", health)
	} else if health.Fallback.Action != FallbackActionStop || health.Fallback.ToModel != "" {
		t.Fatalf("expired-deadline fallback decision = %+v, want stop", health.Fallback)
	}
}

func TestFailoverClientStreamingDoesNotFallbackOnContextErrors(t *testing.T) {
	tests := []struct {
		name      string
		streamErr error
		partial   bool
	}{
		{name: "canceled before content", streamErr: context.Canceled},
		{name: "deadline before content", streamErr: context.DeadlineExceeded},
		{name: "canceled after content", streamErr: context.Canceled, partial: true},
		{name: "deadline after content", streamErr: context.DeadlineExceeded, partial: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chunks := []StreamChunk{{Error: test.streamErr}}
			if test.partial {
				chunks = append([]StreamChunk{{Content: "partial"}}, chunks...)
			}
			primary := &fakeStreamingClient{chunks: chunks}
			fallback := &fakeStreamingClient{chunks: []StreamChunk{{Content: "must not run", Done: true}}}
			fc := fakeFailoverClient(primary, fallback)

			content, _, err := drainFailoverStream(fc.CompleteStreamWithTools(context.Background(), nil, nil))
			if !errors.Is(err, test.streamErr) {
				t.Fatalf("stream error = %v, want %v", err, test.streamErr)
			}
			if test.partial && content != "partial" {
				t.Fatalf("content = %q, want partial content retained with context error", content)
			}
			if fallback.streamCalls.Load() != 0 {
				t.Fatalf("fallback stream calls = %d, want 0", fallback.streamCalls.Load())
			}
		})
	}
}

func TestFailoverClientStreamingCanceledContextWinsOverBufferedSuccess(t *testing.T) {
	tests := []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{
			name: "canceled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			wantErr: context.Canceled,
		},
		{
			name: "expired deadline",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			wantErr: context.DeadlineExceeded,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.context()
			defer cancel()
			primary := &fakeStreamingClient{chunks: []StreamChunk{{Content: "buffered primary", Done: true, FinishReason: "stop"}}}
			fallback := &fakeStreamingClient{chunks: []StreamChunk{{Content: "buffered fallback", Done: true, FinishReason: "stop"}}}
			fc := fakeFailoverClient(primary, fallback)

			content, _, err := drainFailoverStream(fc.CompleteStreamWithTools(ctx, nil, nil))
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("stream error = %v, want %v", err, test.wantErr)
			}
			if content != "" {
				t.Fatalf("content = %q, want canceled context to suppress buffered success", content)
			}
			if primary.streamCalls.Load() != 0 || fallback.streamCalls.Load() != 0 {
				t.Fatalf("stream calls primary=%d fallback=%d, want no attempts", primary.streamCalls.Load(), fallback.streamCalls.Load())
			}
		})
	}
}

func TestFailoverClientStreamingCanceledContextWinsOverAllCooldown(t *testing.T) {
	primary := &fakeStreamingClient{}
	fallback := &fakeStreamingClient{}
	fc := fakeFailoverClient(primary, fallback)
	fc.cooldowns["primary"] = time.Now().Add(time.Minute)
	fc.cooldowns["fallback"] = time.Now().Add(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := drainFailoverStream(fc.CompleteStream(ctx, nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stream error = %v, want context.Canceled rather than cooldown error", err)
	}
}

func TestFailoverClientStreamingCoolsDownLastRetryableModel(t *testing.T) {
	primary := &fakeStreamingClient{chunks: []StreamChunk{{Error: &BackendHTTPError{Provider: "ChatGPT", StatusCode: 503}}}}
	fallback := &fakeStreamingClient{chunks: []StreamChunk{{Error: &BackendHTTPError{Provider: "ChatGPT", StatusCode: 503}}}}
	fc := fakeFailoverClient(primary, fallback)

	_, _, err := drainFailoverStream(fc.CompleteStreamWithTools(context.Background(), nil, nil))
	if err == nil {
		t.Fatal("stream succeeded, want exhausted retryable failure")
	}
	if !fc.isCooledDown("primary") || !fc.isCooledDown("fallback") {
		t.Fatalf("cooldowns primary=%t fallback=%t, want both retryable failures cooled", fc.isCooledDown("primary"), fc.isCooledDown("fallback"))
	}
	decision := fc.LastFallbackDecision()
	if decision.Action != FallbackActionStop || decision.FromModel != "fallback" || decision.ToModel != "" || decision.Reason != "fallback order exhausted" {
		t.Fatalf("last fallback decision = %+v", decision)
	}
}

func TestFailoverClientNonStreamingCoolsDownLastRetryableModel(t *testing.T) {
	tests := []struct {
		name string
		run  func(*FailoverClient) error
	}{
		{
			name: "complete",
			run: func(fc *FailoverClient) error {
				_, err := fc.Complete(context.Background(), nil)
				return err
			},
		},
		{
			name: "complete with tools",
			run: func(fc *FailoverClient) error {
				_, err := fc.CompleteWithTools(context.Background(), nil, nil)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := &fakeCompleteClient{err: &BackendHTTPError{Provider: "ChatGPT", StatusCode: 503}}
			fallback := &fakeCompleteClient{err: &BackendHTTPError{Provider: "ChatGPT", StatusCode: 503}}
			fc := fakeFailoverClient(primary, fallback)

			if err := test.run(fc); err == nil {
				t.Fatal("call succeeded, want exhausted retryable failure")
			}
			if !fc.isCooledDown("primary") || !fc.isCooledDown("fallback") {
				t.Fatalf("cooldowns primary=%t fallback=%t, want both retryable failures cooled", fc.isCooledDown("primary"), fc.isCooledDown("fallback"))
			}
			decision := fc.LastFallbackDecision()
			if decision.Action != FallbackActionStop || decision.FromModel != "fallback" || decision.ToModel != "" || decision.Reason != "fallback order exhausted" {
				t.Fatalf("last fallback decision = %+v", decision)
			}
		})
	}
}

func TestFailoverClientStreamingIncompleteStopsAndCoolsDownWithoutSplice(t *testing.T) {
	tests := []struct {
		name   string
		chunks []StreamChunk
	}{
		{
			name:   "clean close after partial content",
			chunks: []StreamChunk{{Content: "partial answer"}},
		},
		{
			name:   "explicit incomplete terminal",
			chunks: []StreamChunk{{Content: "partial answer"}, {Done: true, FinishReason: "incomplete"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := &fakeStreamingClient{chunks: test.chunks}
			fallback := &fakeStreamingClient{chunks: []StreamChunk{{Content: "must not splice", Done: true, FinishReason: "stop"}}}
			fc := fakeFailoverClient(primary, fallback)

			content, finishReason, err := drainFailoverStream(fc.CompleteStreamWithTools(context.Background(), nil, nil))
			if err != nil || content != "partial answer" || finishReason != "incomplete" {
				t.Fatalf("content=%q finish=%q err=%v", content, finishReason, err)
			}
			if fallback.streamCalls.Load() != 0 {
				t.Fatalf("fallback calls = %d, want no response splice", fallback.streamCalls.Load())
			}
			if !fc.isCooledDown("primary") {
				t.Fatal("primary model was not cooled down after incomplete stream")
			}
			decision := fc.LastFallbackDecision()
			if decision.Action != FallbackActionStop || decision.FromModel != "primary" || decision.ToModel != "" {
				t.Fatalf("incomplete fallback decision = %+v", decision)
			}
		})
	}
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{
			name:       "rate limit error",
			statusCode: 429,
			body:       "rate limit exceeded",
			want:       true,
		},
		{
			name:       "server error 500",
			statusCode: 500,
			body:       "internal server error",
			want:       true,
		},
		{
			name:       "bad gateway 502",
			statusCode: 502,
			body:       "bad gateway",
			want:       true,
		},
		{
			name:       "service unavailable 503",
			statusCode: 503,
			body:       "service unavailable",
			want:       true,
		},
		{
			name:       "gateway timeout 504",
			statusCode: 504,
			body:       "gateway timeout",
			want:       true,
		},
		{
			name:       "context length exceeded",
			statusCode: 400,
			body:       "context_length_exceeded: maximum context length is 4096 tokens",
			want:       true,
		},
		{
			name:       "unauthorized error",
			statusCode: 401,
			body:       "unauthorized",
			want:       false,
		},
		{
			name:       "bad request",
			statusCode: 400,
			body:       "invalid request",
			want:       false,
		},
		{
			name:       "not found",
			statusCode: 404,
			body:       "model not found",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(tt.statusCode, tt.body)
			if got != tt.want {
				t.Errorf("isRetryableError(%d, %q) = %v, want %v", tt.statusCode, tt.body, got, tt.want)
			}
		})
	}
}

func newTestFailoverClient(model string) *FailoverClient {
	client := &OpenAICompatibleClient{
		config: ProviderConfig{
			Name:   "test",
			Model:  model,
			APIKey: "test-key",
		},
	}
	return NewFailoverClient(model, client)
}

func TestFailoverClientCooldown(t *testing.T) {
	fc := newTestFailoverClient("test-model")

	// Initially, no cooldown
	if fc.isCooledDown("model-1") {
		t.Error("model-1 should not be in cooldown initially")
	}

	// Set cooldown
	fc.setCooldown("model-1")

	// Should be in cooldown
	if !fc.isCooledDown("model-1") {
		t.Error("model-1 should be in cooldown after setCooldown")
	}

	// Other models should not be affected
	if fc.isCooledDown("model-2") {
		t.Error("model-2 should not be in cooldown")
	}

	// Manually expire the cooldown for testing
	fc.mu.Lock()
	fc.cooldowns["model-1"] = time.Now().Add(-1 * time.Second)
	fc.mu.Unlock()

	// Should no longer be in cooldown
	if fc.isCooledDown("model-1") {
		t.Error("model-1 should not be in cooldown after expiration")
	}
}

func TestFailoverClientThreadSafety(t *testing.T) {
	fc := newTestFailoverClient("test-model")

	// Concurrent cooldown operations
	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func(id int) {
			model := "model-" + string(rune('0'+id))
			fc.setCooldown(model)
			fc.isCooledDown(model)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify cooldowns were set
	count := 0
	fc.mu.RLock()
	count = len(fc.cooldowns)
	fc.mu.RUnlock()

	if count != 10 {
		t.Errorf("Expected 10 cooldowns, got %d", count)
	}
}

func TestFailoverOrder(t *testing.T) {
	fc := newTestFailoverClient("primary-model")

	// Set cooldown on primary and first fallback
	fc.setCooldown("primary-model")
	fc.setCooldown("fallback-1")

	// Build expected model chain
	primaryModel := "primary-model"
	fallbacks := []string{"fallback-1", "fallback-2", "fallback-3"}

	// Verify which models would be tried (excluding cooled down ones)
	models := append([]string{primaryModel}, fallbacks...)

	availableModels := []string{}
	for _, model := range models {
		if !fc.isCooledDown(model) {
			availableModels = append(availableModels, model)
		}
	}

	// Should skip primary-model and fallback-1, try fallback-2 next
	if len(availableModels) != 2 {
		t.Errorf("Expected 2 available models, got %d", len(availableModels))
	}

	if availableModels[0] != "fallback-2" {
		t.Errorf("Expected first available model to be fallback-2, got %s", availableModels[0])
	}

	if availableModels[1] != "fallback-3" {
		t.Errorf("Expected second available model to be fallback-3, got %s", availableModels[1])
	}
}
