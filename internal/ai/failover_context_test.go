package ai

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// A run that hits its own deadline must not put the model into cooldown for
// everyone else. On 2026-09-02 a 15-minute host_task subagent expired, the
// request error was classified as "unavailable", all three models went into
// cooldown, and the parent DM that finished the same second got "all models
// are in cooldown" instead of its answer.
func TestCompleteWithToolsDoesNotCoolDownOnCallerContextEnd(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	primary := &fakeCompleteClient{err: fmt.Errorf("request failed: Post \"http://gateway/v1/chat/completions\": %w", context.DeadlineExceeded)}
	fallback := &fakeCompleteClient{}
	fc := fakeFailoverClient(primary, fallback)

	_, err := fc.CompleteWithTools(ctx, nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error should carry the caller's deadline: %v", err)
	}
	if fallback.completeToolCalls.Load() != 0 {
		t.Fatalf("fallback model was tried after the caller's context ended")
	}
	for _, entry := range fc.entries {
		if fc.isCooledDown(entry.model) {
			t.Fatalf("model %s was cooled down for the caller's own context ending", entry.model)
		}
	}
}

func TestCompleteDoesNotCoolDownOnCallerContextEnd(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	primary := &fakeCompleteClient{err: fmt.Errorf("request failed: %w", context.Canceled)}
	fallback := &fakeCompleteClient{}
	fc := fakeFailoverClient(primary, fallback)

	if _, err := fc.Complete(ctx, nil); err == nil {
		t.Fatal("expected an error")
	}
	if fallback.completeCalls.Load() != 0 {
		t.Fatalf("fallback model was tried after the caller's context ended")
	}
	for _, entry := range fc.entries {
		if fc.isCooledDown(entry.model) {
			t.Fatalf("model %s was cooled down for the caller's own context ending", entry.model)
		}
	}
}
