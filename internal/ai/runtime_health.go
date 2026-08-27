package ai

import (
	"context"
	"errors"
	"io"
	"time"
)

// clientUnwrapper exposes capability-bearing clients hidden by decorators.
type clientUnwrapper interface {
	UnwrapClient() Client
}

// TrackClient decorates real provider attempts with health reporting. A
// FailoverClient is handled entry-by-entry so a failed primary followed by a
// healthy fallback records both concrete model outcomes.
func TrackClient(client Client, identity BackendIdentity, reporter BackendOutcomeReporter) Client {
	if client == nil || reporter == nil {
		return client
	}
	if failover, ok := client.(*FailoverClient); ok {
		order := failover.models()
		for i := range failover.entries {
			entryIdentity := identity
			entryIdentity.Model = failover.entries[i].model
			entryIdentity.FallbackOrder = append([]string(nil), order...)
			failover.entries[i].client = trackSingleClient(failover.entries[i].client, entryIdentity, reporter)
		}
		return failover
	}
	return trackSingleClient(client, identity, reporter)
}

func trackSingleClient(client Client, identity BackendIdentity, reporter BackendOutcomeReporter) Client {
	base := &runtimeHealthClient{inner: client, identity: identity, reporter: reporter}
	if streaming, ok := client.(StreamingClient); ok {
		return &runtimeHealthStreamingClient{runtimeHealthClient: base, streaming: streaming}
	}
	return base
}

type runtimeHealthClient struct {
	inner    Client
	identity BackendIdentity
	reporter BackendOutcomeReporter
}

func (c *runtimeHealthClient) Complete(ctx context.Context, messages []Message) (string, error) {
	started := time.Now()
	result, err := c.inner.Complete(ctx, messages)
	c.report(ctx, err, "", time.Since(started))
	return result, err
}

func (c *runtimeHealthClient) CompleteWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) (*ChatCompletionResponse, error) {
	started := time.Now()
	result, err := c.inner.CompleteWithTools(ctx, messages, tools)
	c.report(ctx, err, "", time.Since(started))
	return result, err
}

func (c *runtimeHealthClient) SupportsVision() bool {
	return SupportsVision(c.inner)
}

func (c *runtimeHealthClient) UnwrapClient() Client {
	return c.inner
}

func (c *runtimeHealthClient) report(ctx context.Context, err error, finishReason string, latency time.Duration) {
	if c == nil || c.reporter == nil {
		return
	}
	canceled := false
	if ctx != nil {
		ctxErr := ctx.Err()
		canceled = errors.Is(ctxErr, context.Canceled)
		if err == nil && errors.Is(ctxErr, context.DeadlineExceeded) {
			err = context.DeadlineExceeded
		}
	}
	c.reporter(BackendRuntimeOutcome{
		Identity:     c.identity,
		Err:          err,
		FinishReason: finishReason,
		Canceled:     canceled,
		Latency:      latency,
	})
}

type runtimeHealthStreamingClient struct {
	*runtimeHealthClient
	streaming StreamingClient
}

func (c *runtimeHealthStreamingClient) CompleteStream(ctx context.Context, messages []Message) <-chan StreamChunk {
	started := time.Now()
	return c.trackStream(ctx, started, c.streaming.CompleteStream(ctx, messages))
}

func (c *runtimeHealthStreamingClient) CompleteStreamWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) <-chan StreamChunk {
	started := time.Now()
	return c.trackStream(ctx, started, c.streaming.CompleteStreamWithTools(ctx, messages, tools))
}

func (c *runtimeHealthStreamingClient) trackStream(ctx context.Context, started time.Time, input <-chan StreamChunk) <-chan StreamChunk {
	output := make(chan StreamChunk, 100)
	go func() {
		defer close(output)
		if input == nil {
			c.report(ctx, io.ErrUnexpectedEOF, "", time.Since(started))
			return
		}

		reported := false
		emittedContent := false
		for chunk := range input {
			if chunk.Content != "" {
				emittedContent = true
			}
			if !reported && chunk.Error != nil {
				finishReason := chunk.FinishReason
				if emittedContent && finishReason == "" {
					finishReason = "incomplete"
				}
				c.report(ctx, chunk.Error, finishReason, time.Since(started))
				reported = true
			} else if !reported && chunk.Done {
				if chunk.FinishReason == "incomplete" {
					c.report(ctx, ErrBackendStreamIncomplete, chunk.FinishReason, time.Since(started))
				} else {
					c.report(ctx, nil, chunk.FinishReason, time.Since(started))
				}
				reported = true
			}
			output <- chunk
		}
		if !reported {
			finishReason := ""
			if emittedContent {
				finishReason = "incomplete"
			}
			c.report(ctx, io.ErrUnexpectedEOF, finishReason, time.Since(started))
		}
	}()
	return output
}
