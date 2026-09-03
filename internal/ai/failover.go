package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
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
// It implements both Client and StreamingClient.
type FailoverClient struct {
	entries      []failoverEntry
	cooldowns    map[string]time.Time
	lastDecision FallbackDecision
	mu           sync.RWMutex
}

var _ StreamingClient = (*FailoverClient)(nil)

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

// isRetryableFromErr classifies an error and decides whether a fallback model
// should be tried.
func isRetryableFromErr(err error) bool {
	if err == nil {
		return false
	}
	decision := DecideFallback(ClassifyBackendError(err), true, "", []string{"primary", "fallback"})
	switch decision.Action {
	case FallbackActionFallback:
		return true
	case FallbackActionStop, FallbackActionApproval:
		return false
	default:
		return false
	}
}

// LastFallbackDecision returns the most recent failover decision for status/logging.
func (fc *FailoverClient) LastFallbackDecision() FallbackDecision {
	if fc == nil {
		return FallbackDecision{}
	}
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.lastDecision
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

func (fc *FailoverClient) setDecision(decision FallbackDecision) {
	fc.mu.Lock()
	fc.lastDecision = decision
	fc.mu.Unlock()
}

func (fc *FailoverClient) failureDecision(kind BackendFailureKind, current string) FallbackDecision {
	models := fc.models()
	decision := DecideFallback(kind, len(models) > 1, current, models)
	if decision.Action == FallbackActionFallback {
		decision.ToModel = nextAvailableModel(fc, current)
		if decision.ToModel == "" {
			decision.Action = FallbackActionStop
			decision.Reason = "fallback order exhausted"
		}
	}
	if fallbackableFailure(kind) {
		fc.setCooldown(current)
	}
	return decision
}

func (fc *FailoverClient) partialStopDecision(kind BackendFailureKind, current, reason string) FallbackDecision {
	decision := fc.failureDecision(kind, current)
	decision.Action = FallbackActionStop
	decision.ToModel = ""
	decision.Reason = reason
	return decision
}

type streamOpenFunc func(context.Context, StreamingClient) <-chan StreamChunk

// CompleteStream implements StreamingClient. A retryable failure may switch
// models only before any content is emitted to the caller.
func (fc *FailoverClient) CompleteStream(ctx context.Context, messages []Message) <-chan StreamChunk {
	return fc.completeStream(ctx, "CompleteStream", func(attemptCtx context.Context, client StreamingClient) <-chan StreamChunk {
		return client.CompleteStream(attemptCtx, messages)
	})
}

// CompleteStreamWithTools implements StreamingClient. A retryable failure may
// switch models only before any content is emitted to the caller.
func (fc *FailoverClient) CompleteStreamWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) <-chan StreamChunk {
	return fc.completeStream(ctx, "CompleteStreamWithTools", func(attemptCtx context.Context, client StreamingClient) <-chan StreamChunk {
		return client.CompleteStreamWithTools(attemptCtx, messages, tools)
	})
}

func (fc *FailoverClient) completeStream(ctx context.Context, operation string, open streamOpenFunc) <-chan StreamChunk {
	out := make(chan StreamChunk, 100)
	go func() {
		defer close(out)
		if ctx == nil {
			ctx = context.Background()
		}
		if err := ctx.Err(); err != nil {
			out <- StreamChunk{Error: err, Done: true}
			return
		}

		primaryModel := ""
		if len(fc.entries) > 0 {
			primaryModel = fc.entries[0].model
		}

	entryLoop:
		for _, entry := range fc.entries {
			if err := ctx.Err(); err != nil {
				out <- StreamChunk{Error: err, Done: true}
				return
			}
			if fc.isCooledDown(entry.model) {
				continue
			}

			streamClient, ok := entry.client.(StreamingClient)
			if !ok {
				out <- StreamChunk{
					Error: fmt.Errorf("client for model %s does not support streaming", entry.model),
					Done:  true,
				}
				return
			}

			attemptCtx, cancel := context.WithCancel(ctx)
			chunks := open(attemptCtx, streamClient)
			if chunks == nil {
				cancel()
				if err := ctx.Err(); err != nil {
					out <- StreamChunk{Error: err, Done: true}
					return
				}
				err := fmt.Errorf("%s stream for model %s is nil: %w", operation, entry.model, io.ErrUnexpectedEOF)
				kind := ClassifyBackendError(err)
				decision := fc.failureDecision(kind, entry.model)
				fc.setDecision(decision)
				if decision.Action == FallbackActionFallback {
					if ctxErr := ctx.Err(); ctxErr != nil {
						out <- StreamChunk{Error: ctxErr, Done: true}
						return
					}
					log.Printf("[failover] %s: model %s returned a nil stream (%s), switching to %s: %v", operation, entry.model, kind, decision.ToModel, err)
					continue
				}
				out <- StreamChunk{Error: err, Done: true}
				return
			}

			emittedContent := false
			for {
				select {
				case <-ctx.Done():
					cancel()
					drainStream(chunks)
					out <- StreamChunk{Error: ctx.Err(), Done: true}
					return

				case chunk, ok := <-chunks:
					if err := ctx.Err(); err != nil {
						cancel()
						drainStream(chunks)
						out <- StreamChunk{Error: err, Done: true}
						return
					}
					if !ok {
						cancel()
						if emittedContent {
							decision := fc.partialStopDecision(BackendFailureUnavailable, entry.model, "partial response already emitted; fallback suppressed")
							fc.setDecision(decision)
							log.Printf("[failover] %s: model %s closed after partial content; preserving the partial response", operation, entry.model)
							out <- StreamChunk{Done: true, FinishReason: "incomplete"}
							return
						}

						err := fmt.Errorf("%s stream for model %s closed before emitting content: %w", operation, entry.model, io.ErrUnexpectedEOF)
						kind := ClassifyBackendError(err)
						decision := fc.failureDecision(kind, entry.model)
						fc.setDecision(decision)
						if decision.Action == FallbackActionFallback {
							if ctxErr := ctx.Err(); ctxErr != nil {
								out <- StreamChunk{Error: ctxErr, Done: true}
								return
							}
							log.Printf("[failover] %s: model %s closed before content (%s), switching to %s: %v", operation, entry.model, kind, decision.ToModel, err)
							continue entryLoop
						}
						out <- StreamChunk{Error: err, Done: true}
						return
					}

					if chunk.Error != nil {
						err := chunk.Error
						if ctx.Err() != nil {
							err = ctx.Err()
						}
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							cancel()
							drainStream(chunks)
							out <- StreamChunk{Error: err, Done: true}
							return
						}

						kind := ClassifyBackendError(err)
						if emittedContent {
							decision := fc.partialStopDecision(kind, entry.model, "partial response already emitted; fallback suppressed")
							fc.setDecision(decision)
							cancel()
							drainStream(chunks)
							log.Printf("[failover] %s: model %s failed after partial content (%s); preserving partial response without switching: %v", operation, entry.model, kind, err)
							out <- StreamChunk{Done: true, FinishReason: "incomplete"}
							return
						}

						decision := fc.failureDecision(kind, entry.model)
						fc.setDecision(decision)
						if decision.Action == FallbackActionFallback {
							cancel()
							drainStream(chunks)
							if ctxErr := ctx.Err(); ctxErr != nil {
								out <- StreamChunk{Error: ctxErr, Done: true}
								return
							}
							log.Printf("[failover] %s: model %s failed before content (%s), switching to %s: %v", operation, entry.model, kind, decision.ToModel, err)
							continue entryLoop
						}

						cancel()
						drainStream(chunks)
						log.Printf("[failover] %s: model %s failed before content (%s), decision=%s reason=%s", operation, entry.model, kind, decision.Action, decision.Reason)
						out <- StreamChunk{Error: err, Done: true}
						return
					}

					// Empty non-terminal chunks do not carry user-visible state and
					// must not leak from an attempt that may still fail over.
					// An image-only chunk is not empty: a natively
					// image-capable backend streams the picture in a delta
					// with no text beside it, and dropping it here is
					// indistinguishable from the model saying nothing.
					if chunk.Content == "" && len(chunk.Images) == 0 && !chunk.Done {
						continue
					}
					if chunk.Content != "" || len(chunk.Images) > 0 {
						emittedContent = true
					}
					if err := ctx.Err(); err != nil {
						cancel()
						drainStream(chunks)
						out <- StreamChunk{Error: err, Done: true}
						return
					}
					if chunk.Done && chunk.FinishReason == "incomplete" {
						reason := "stream ended incomplete; fallback suppressed"
						if emittedContent {
							reason = "partial response already emitted; fallback suppressed"
						}
						decision := fc.partialStopDecision(BackendFailureUnavailable, entry.model, reason)
						fc.setDecision(decision)
					}
					out <- chunk
					if chunk.Done {
						cancel()
						if entry.model != primaryModel && chunk.FinishReason != "incomplete" {
							log.Printf("[failover] %s: succeeded with fallback model %s", operation, entry.model)
						}
						return
					}
				}
			}
		}

		if err := ctx.Err(); err != nil {
			out <- StreamChunk{Error: err, Done: true}
			return
		}
		message := "all models are in cooldown"
		if len(fc.entries) == 0 {
			message = "failover client has no models"
		}
		out <- StreamChunk{Error: errors.New(message), Done: true}
	}()
	return out
}

func drainStream(ch <-chan StreamChunk) {
	if ch == nil {
		return
	}
	go func() {
		for range ch {
		}
	}()
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
		// See CompleteWithTools: a finished caller context must not cool
		// the model down.
		if ctx.Err() != nil {
			return "", err
		}
		kind := ClassifyBackendError(err)
		decision := fc.failureDecision(kind, entry.model)
		fc.setDecision(decision)
		if decision.Action == FallbackActionFallback {
			log.Printf("[failover] Complete: model %s failed (%s), fallback decision=%s next=%s err=%v", entry.model, kind, decision.Action, decision.ToModel, err)
			continue
		}

		// Non-retryable error — surface immediately.
		log.Printf("[failover] Complete: model %s failed (%s), fallback decision=%s reason=%s", entry.model, kind, decision.Action, decision.Reason)
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
		// The caller's own context ending is not a backend failure. Cooling
		// the model down for it would take it away from every other session
		// for a minute because one run hit its deadline — which is exactly
		// how a 15-minute host_task subagent left the parent DM with "all
		// models are in cooldown" on 2026-09-02.
		if ctx.Err() != nil {
			return nil, err
		}
		kind := ClassifyBackendError(err)
		decision := fc.failureDecision(kind, entry.model)
		fc.setDecision(decision)
		if decision.Action == FallbackActionFallback {
			log.Printf("[failover] CompleteWithTools: model %s failed (%s), fallback decision=%s next=%s err=%v", entry.model, kind, decision.Action, decision.ToModel, err)
			continue
		}

		// Non-retryable error — surface immediately.
		log.Printf("[failover] CompleteWithTools: model %s failed (%s), fallback decision=%s reason=%s", entry.model, kind, decision.Action, decision.Reason)
		return nil, err
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all models failed or are in cooldown: %w", lastErr)
	}
	return nil, fmt.Errorf("all models are in cooldown")
}

func (fc *FailoverClient) models() []string {
	models := make([]string, 0, len(fc.entries))
	for _, entry := range fc.entries {
		models = append(models, entry.model)
	}
	return models
}

func nextAvailableModel(fc *FailoverClient, current string) string {
	seenCurrent := false
	for _, entry := range fc.entries {
		if !seenCurrent {
			seenCurrent = entry.model == current
			continue
		}
		if !fc.isCooledDown(entry.model) {
			return entry.model
		}
	}
	return ""
}
