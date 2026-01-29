package ai

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const cooldownDuration = 60 * time.Second

// FailoverClient wraps an AI client with failover capabilities
type FailoverClient struct {
	client    *OpenAICompatibleClient
	cooldowns map[string]time.Time
	mu        sync.RWMutex
}

// NewFailoverClient creates a new failover client
func NewFailoverClient(client *OpenAICompatibleClient) *FailoverClient {
	return &FailoverClient{
		client:    client,
		cooldowns: make(map[string]time.Time),
	}
}

// isRetryableError checks if an error warrants trying a fallback model
func isRetryableError(statusCode int, body string) bool {
	// Rate limiting and server errors
	if statusCode == 429 || statusCode == 500 || statusCode == 502 || statusCode == 503 || statusCode == 504 {
		return true
	}

	// Context length exceeded
	if strings.Contains(body, "context_length_exceeded") {
		return true
	}

	return false
}

// isCooledDown checks if a model is in cooldown
func (fc *FailoverClient) isCooledDown(model string) bool {
	fc.mu.RLock()
	defer fc.mu.RUnlock()

	cooldownUntil, exists := fc.cooldowns[model]
	if !exists {
		return false
	}

	return time.Now().Before(cooldownUntil)
}

// setCooldown puts a model into cooldown
func (fc *FailoverClient) setCooldown(model string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	fc.cooldowns[model] = time.Now().Add(cooldownDuration)
}

// CompleteWithFailover tries the primary model, then fallbacks in order
func (fc *FailoverClient) CompleteWithFailover(
	ctx context.Context,
	messages []Message,
	model string,
	fallbackModels []string,
) (response string, actualModel string, err error) {
	// Build the model chain: primary + fallbacks
	models := append([]string{model}, fallbackModels...)

	var lastError error

	for _, tryModel := range models {
		// Skip models in cooldown
		if fc.isCooledDown(tryModel) {
			continue
		}

		// Temporarily override the client's model
		originalModel := fc.client.config.Model
		fc.client.config.Model = tryModel

		// Try the request
		resp, err := fc.client.Complete(ctx, messages)

		// Restore original model
		fc.client.config.Model = originalModel

		if err == nil {
			// Success
			return resp, tryModel, nil
		}

		// Check if error is retryable
		lastError = err
		errorMsg := err.Error()

		// Extract status code from error message
		var statusCode int
		if _, scanErr := fmt.Sscanf(errorMsg, "API error (status %d):", &statusCode); scanErr == nil {
			if isRetryableError(statusCode, errorMsg) {
				// Put model in cooldown and try next
				fc.setCooldown(tryModel)
				continue
			}
		}

		// Non-retryable error, return immediately
		return "", tryModel, err
	}

	// All models failed or are in cooldown
	if lastError != nil {
		return "", "", fmt.Errorf("all models failed: %w", lastError)
	}

	return "", "", fmt.Errorf("all models are in cooldown")
}

// CompleteStreamWithFailover tries the primary model, then fallbacks in order for streaming
func (fc *FailoverClient) CompleteStreamWithFailover(
	ctx context.Context,
	messages []Message,
	model string,
	fallbackModels []string,
	callback func(StreamChunk),
) (actualModel string, err error) {
	// Build the model chain: primary + fallbacks
	models := append([]string{model}, fallbackModels...)

	var lastError error

	for _, tryModel := range models {
		// Skip models in cooldown
		if fc.isCooledDown(tryModel) {
			continue
		}

		// Temporarily override the client's model
		originalModel := fc.client.config.Model
		fc.client.config.Model = tryModel

		// Try the streaming request
		chunkChan := fc.client.CompleteStream(ctx, messages)

		// Restore original model
		fc.client.config.Model = originalModel

		// Process chunks
		var streamError error
		for chunk := range chunkChan {
			if chunk.Error != nil {
				streamError = chunk.Error
				break
			}
			callback(chunk)
			if chunk.Done {
				// Success
				return tryModel, nil
			}
		}

		if streamError == nil {
			// Stream completed successfully
			return tryModel, nil
		}

		// Check if error is retryable
		lastError = streamError
		errorMsg := streamError.Error()

		// Extract status code from error message
		var statusCode int
		if _, scanErr := fmt.Sscanf(errorMsg, "API error (status %d):", &statusCode); scanErr == nil {
			if isRetryableError(statusCode, errorMsg) {
				// Put model in cooldown and try next
				fc.setCooldown(tryModel)
				continue
			}
		}

		// Non-retryable error, return immediately
		return tryModel, streamError
	}

	// All models failed or are in cooldown
	if lastError != nil {
		return "", fmt.Errorf("all models failed: %w", lastError)
	}

	return "", fmt.Errorf("all models are in cooldown")
}
