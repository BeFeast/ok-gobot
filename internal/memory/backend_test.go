package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFallbackBackendFallsBackAndSuppressesRetries(t *testing.T) {
	t.Parallel()

	primary := &stubBackend{name: "qmd", err: fmt.Errorf("qmd unavailable")}
	fallback := &stubBackend{name: "builtin", results: []MemoryResult{{Source: "MEMORY.md", Content: "fallback"}}}
	backend := NewFallbackBackend(primary, fallback, time.Minute)
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	backend.now = func() time.Time { return now }

	results, err := backend.Search(context.Background(), "query", 5, false)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 || results[0].Content != "fallback" {
		t.Fatalf("unexpected fallback results: %+v", results)
	}
	if primary.calls != 1 {
		t.Fatalf("primary calls=%d, want 1", primary.calls)
	}

	_, err = backend.Search(context.Background(), "query", 5, false)
	if err != nil {
		t.Fatalf("second Search returned error: %v", err)
	}
	if primary.calls != 1 {
		t.Fatalf("primary retried during cooldown; calls=%d", primary.calls)
	}

	now = now.Add(time.Minute + time.Second)
	_, err = backend.Search(context.Background(), "query", 5, false)
	if err != nil {
		t.Fatalf("third Search returned error: %v", err)
	}
	if primary.calls != 2 {
		t.Fatalf("primary calls after cooldown=%d, want 2", primary.calls)
	}
}

func TestFallbackBackendClearsReasonAfterCooldown(t *testing.T) {
	t.Parallel()

	primary := &stubBackend{name: "qmd", err: fmt.Errorf("server unavailable")}
	fallback := &stubBackend{name: "builtin", results: []MemoryResult{{Source: "MEMORY.md", Content: "fallback"}}}
	backend := NewFallbackBackend(primary, fallback, time.Minute)
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	backend.now = func() time.Time { return now }

	if _, err := backend.Search(context.Background(), "query", 5, false); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if got := backend.LastError(); !strings.Contains(got, "server unavailable") {
		t.Fatalf("LastError=%q, want server unavailable", got)
	}

	now = now.Add(time.Minute + time.Second)
	primary.err = nil
	if _, err := backend.Search(context.Background(), "query", 5, false); err != nil {
		t.Fatalf("Search after cooldown returned error: %v", err)
	}
	if got := backend.LastError(); got != "" {
		t.Fatalf("LastError after cooldown success=%q, want empty", got)
	}
}

func TestFallbackBackendFallbackReasonFollowsCooldown(t *testing.T) {
	t.Parallel()

	primary := &stubBackend{name: "qmd", err: fmt.Errorf("server unavailable")}
	fallback := &stubBackend{name: "builtin", results: []MemoryResult{{Source: "MEMORY.md", Content: "fallback"}}}
	backend := NewFallbackBackend(primary, fallback, time.Minute)
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	backend.now = func() time.Time { return now }

	if _, err := backend.Search(context.Background(), "query", 5, false); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if got := backend.FallbackReason(); !strings.Contains(got, "server unavailable") {
		t.Fatalf("FallbackReason=%q, want server unavailable", got)
	}

	now = now.Add(time.Minute + time.Second)
	if got := backend.FallbackReason(); got != "" {
		t.Fatalf("FallbackReason after cooldown=%q, want empty", got)
	}
}

func TestMemoryManagerUsesConfiguredBackend(t *testing.T) {
	t.Parallel()

	backend := &stubBackend{name: "qmd", results: []MemoryResult{{Source: "MEMORY.md", Content: "qmd result"}}}
	manager := NewMemoryManager(nil, nil, WithBackend(backend))

	results, err := manager.Search(context.Background(), "query", 5)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if backend.calls != 1 {
		t.Fatalf("backend calls=%d, want 1", backend.calls)
	}
	if len(results) != 1 || results[0].Content != "qmd result" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestFallbackBackendFallsBackOnEmptyPrimaryResults(t *testing.T) {
	t.Parallel()

	primary := &stubBackend{name: "qmd"}
	fallback := &stubBackend{name: "builtin", results: []MemoryResult{{Source: "MEMORY.md", Content: "fallback"}}}
	backend := NewFallbackBackend(primary, fallback, time.Minute)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	backend.now = func() time.Time { return now }

	results, err := backend.Search(context.Background(), "query", 5, false)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 || results[0].Content != "fallback" {
		t.Fatalf("empty primary result did not reach fallback: %+v", results)
	}
	if primary.calls != 1 || fallback.calls != 1 {
		t.Fatalf("calls primary=%d fallback=%d, want 1/1", primary.calls, fallback.calls)
	}

	// An empty primary result is a miss, not a failure: it must not trip the
	// cooldown that suppresses the primary backend after real errors.
	if reason := backend.FallbackReason(); reason != "" {
		t.Fatalf("empty primary result tripped cooldown: %q", reason)
	}
	if _, err := backend.Search(context.Background(), "query", 5, false); err != nil {
		t.Fatalf("second Search returned error: %v", err)
	}
	if primary.calls != 2 {
		t.Fatalf("primary was suppressed after an empty result; calls=%d, want 2", primary.calls)
	}
}

func TestFallbackBackendKeepsEmptyWhenFallbackAlsoMisses(t *testing.T) {
	t.Parallel()

	primary := &stubBackend{name: "qmd"}
	fallback := &stubBackend{name: "builtin"}
	backend := NewFallbackBackend(primary, fallback, time.Minute)

	results, err := backend.Search(context.Background(), "query", 5, false)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %+v", results)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback calls=%d, want 1", fallback.calls)
	}
}

func TestFallbackBackendIgnoresFallbackErrorOnEmptyPrimary(t *testing.T) {
	t.Parallel()

	primary := &stubBackend{name: "qmd"}
	fallback := &stubBackend{name: "builtin", err: fmt.Errorf("builtin unavailable")}
	backend := NewFallbackBackend(primary, fallback, time.Minute)

	results, err := backend.Search(context.Background(), "query", 5, false)
	if err != nil {
		t.Fatalf("a healthy primary with no hits must not surface a fallback error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %+v", results)
	}
}

type stubBackend struct {
	name    string
	results []MemoryResult
	err     error
	calls   int
}

func (s *stubBackend) Name() string { return s.name }

func (s *stubBackend) Search(_ context.Context, _ string, _ int, _ bool) ([]MemoryResult, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.results, nil
}
