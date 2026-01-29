package ai

import (
	"testing"
	"time"
)

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

func TestFailoverClientCooldown(t *testing.T) {
	client := &OpenAICompatibleClient{
		config: ProviderConfig{
			Name:   "test",
			Model:  "test-model",
			APIKey: "test-key",
		},
	}

	fc := NewFailoverClient(client)

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
	client := &OpenAICompatibleClient{
		config: ProviderConfig{
			Name:   "test",
			Model:  "test-model",
			APIKey: "test-key",
		},
	}

	fc := NewFailoverClient(client)

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
	client := &OpenAICompatibleClient{
		config: ProviderConfig{
			Name:   "test",
			Model:  "primary-model",
			APIKey: "test-key",
		},
	}

	fc := NewFailoverClient(client)

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
