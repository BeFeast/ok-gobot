package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

const cooldownDuration = 60 * time.Second

// failoverEntry holds a model name and its pre-created client.
type failoverEntry struct {
	model  string
	client Client
}

// FailoverClient wraps multiple clients and tries them in order on retryable errors.
// It implements the Client interface.
type FailoverClient struct {
	entries   []failoverEntry
	cooldowns map[string]time.Time
	mu        sync.RWMutex
}

// SupportsVision reports true when every configured fallback client supports
// multimodal input. This avoids routing image content to a non-vision fallback.
func (fc *FailoverClient) SupportsVision() bool {
	if len(fc.entries) == 0 {
		return false
	}
	for _, entry := range fc.entries {
		if !SupportsVision(entry.client) {
			return false
		}
	}
	return true
}

// NewClientWithFailover creates a FailoverClient from a primary ProviderConfig and fallback
// model names. Fallback models share the same provider/API key/base URL as the primary.
func NewClientWithFailover(primary ProviderConfig, fallbackModels []string) (*FailoverClient, error) {
	entries := make([]failoverEntry, 0, 1+len(fallbackModels))

	primaryClient, err := NewClient(primary)
	if err != nil {
		return nil, fmt.Errorf("failed to create primary client: %w", err)
	}
	entries = append(entries, failoverEntry{model: primary.Model, client: primaryClient})

	for _, model := range fallbackModels {
		cfg := primary
		cfg.Model = model
		fbClient, err := NewClient(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create failover client for model %s: %w", model, err)
		}
		entries = append(entries, failoverEntry{model: model, client: fbClient})
	}

	return &FailoverClient{
		entries:   entries,
		cooldowns: make(map[string]time.Time),
	}, nil
}

// NewFailoverClient creates a FailoverClient wrapping a single existing Client.
// Useful for testing; in production use NewClientWithFailover.
func NewFailoverClient(model string, client Client) *FailoverClient {
	return &FailoverClient{
		entries:   []failoverEntry{{model: model, client: client}},
		cooldowns: make(map[string]time.Time),
	}
}

// isRetryableError checks if a status code / body warrants trying a fallback model.
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

// isRetryableFromErr extracts the status code from an error message and decides
// whether the error is retryable. Also treats network-level failures as retryable.
func isRetryableFromErr(err error) bool {
	msg := err.Error()
	var statusCode int
	if _, scanErr := fmt.Sscanf(msg, "API error (status %d):", &statusCode); scanErr == nil {
		return isRetryableError(statusCode, msg)
	}

	// Network-level errors: timeouts, connection resets, EOF, TLS failures.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "TLS handshake") ||
		strings.Contains(msg, "no such host") {
		return true
	}

	return false
}

// isCooledDown reports whether a model is currently in cooldown.
func (fc *FailoverClient) isCooledDown(model string) bool {
	fc.mu.RLock()
	defer fc.mu.RUnlock()

	cooldownUntil, exists := fc.cooldowns[model]
	if !exists {
		return false
	}
	return time.Now().Before(cooldownUntil)
}

// setCooldown puts a model into cooldown for cooldownDuration.
func (fc *FailoverClient) setCooldown(model string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	fc.cooldowns[model] = time.Now().Add(cooldownDuration)
}

// Complete implements Client. It tries each model in order, skipping ones in
// cooldown and retrying with the next on 429/5xx errors.
func (fc *FailoverClient) Complete(ctx context.Context, messages []Message) (string, error) {
	primaryModel := ""
	if len(fc.entries) > 0 {
		primaryModel = fc.entries[0].model
	}

	var lastErr error
	for _, entry := range fc.entries {
		if fc.isCooledDown(entry.model) {
			continue
		}

		resp, err := entry.client.Complete(ctx, messages)
		if err == nil {
			if entry.model != primaryModel {
				log.Printf("[failover] Complete: succeeded with fallback model %s", entry.model)
			}
			return resp, nil
		}

		lastErr = err
		if isRetryableFromErr(err) {
			log.Printf("[failover] Complete: model %s failed with retryable error (%v), trying next", entry.model, err)
			fc.setCooldown(entry.model)
			continue
		}

		// Non-retryable error — surface immediately.
		return "", err
	}

	if lastErr != nil {
		return "", fmt.Errorf("all models failed or are in cooldown: %w", lastErr)
	}
	return "", fmt.Errorf("all models are in cooldown")
}

// CompleteWithTools implements Client. It tries each model in order, skipping ones
// in cooldown and retrying with the next on 429/5xx errors.
func (fc *FailoverClient) CompleteWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) (*ChatCompletionResponse, error) {
	primaryModel := ""
	if len(fc.entries) > 0 {
		primaryModel = fc.entries[0].model
	}

	var lastErr error
	for _, entry := range fc.entries {
		if fc.isCooledDown(entry.model) {
			continue
		}

		resp, err := entry.client.CompleteWithTools(ctx, messages, tools)
		if err == nil {
			if entry.model != primaryModel {
				log.Printf("[failover] CompleteWithTools: succeeded with fallback model %s", entry.model)
			}
			return resp, nil
		}

		lastErr = err
		if isRetryableFromErr(err) {
			log.Printf("[failover] CompleteWithTools: model %s failed with retryable error (%v), trying next", entry.model, err)
			fc.setCooldown(entry.model)
			continue
		}

		// Non-retryable error — surface immediately.
		return nil, err
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all models failed or are in cooldown: %w", lastErr)
	}
	return nil, fmt.Errorf("all models are in cooldown")
}
