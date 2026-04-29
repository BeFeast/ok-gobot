package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const defaultBackendCooldown = time.Minute

// Backend searches memory by natural-language query.
type Backend interface {
	Name() string
	Search(ctx context.Context, query string, topK int, expand bool) ([]MemoryResult, error)
}

// BuiltinBackend is the built-in SQLite/embedding memory backend.
type BuiltinBackend struct {
	client EmbeddingQueryClient
	store  *MemoryStore
}

// NewBuiltinBackend creates the built-in SQLite/embedding backend.
func NewBuiltinBackend(client EmbeddingQueryClient, store *MemoryStore) *BuiltinBackend {
	return &BuiltinBackend{client: client, store: store}
}

func (b *BuiltinBackend) Name() string {
	return "builtin"
}

func (b *BuiltinBackend) Search(ctx context.Context, query string, topK int, expand bool) ([]MemoryResult, error) {
	if b == nil || b.store == nil {
		return nil, fmt.Errorf("memory manager is not configured")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("memory search query is empty")
	}

	var queryEmbedding []float32
	if b.client != nil {
		if embedding, err := b.client.GetEmbedding(ctx, query); err == nil {
			queryEmbedding = embedding
		}
	}

	results, err := b.store.SearchHybrid(ctx, query, queryEmbedding, topK)
	if err != nil {
		return nil, fmt.Errorf("failed to search memory chunks: %w", err)
	}
	if !expand || len(results) == 0 {
		return results, nil
	}

	type branch struct{ file, header string }
	var branches []branch
	seen := make(map[branch]bool)
	bestScores := make(map[branch]float32)

	for _, h := range results {
		b := branch{h.SourceFile, h.HeaderPath}
		if h.Similarity > bestScores[b] {
			bestScores[b] = h.Similarity
		}
		if !seen[b] {
			seen[b] = true
			branches = append(branches, b)
		}
	}

	expanded := make([]MemoryResult, 0, len(branches))
	for _, br := range branches {
		chunks, err := b.store.GetBranchChunks(ctx, br.file, br.header)
		if err != nil || len(chunks) == 0 {
			continue
		}

		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Content
		}

		expanded = append(expanded, MemoryResult{
			Source:     br.file,
			SourceFile: br.file,
			HeaderPath: br.header,
			Content:    strings.Join(texts, "\n\n"),
			Similarity: bestScores[br],
		})
	}

	return expanded, nil
}

// FallbackBackend uses a primary backend, then temporarily suppresses it after
// failures to avoid retry storms while preserving built-in memory availability.
type FallbackBackend struct {
	primary  Backend
	fallback Backend
	cooldown time.Duration
	now      func() time.Time

	mu            sync.Mutex
	disabledUntil time.Time
	lastErr       error
}

// NewFallbackBackend wraps a primary backend with a fallback backend.
func NewFallbackBackend(primary, fallback Backend, cooldown time.Duration) *FallbackBackend {
	if cooldown <= 0 {
		cooldown = defaultBackendCooldown
	}
	return &FallbackBackend{
		primary:  primary,
		fallback: fallback,
		cooldown: cooldown,
		now:      time.Now,
	}
}

func (b *FallbackBackend) Name() string {
	if b == nil || b.primary == nil {
		return "fallback"
	}
	if b.fallback == nil {
		return b.primary.Name()
	}
	return b.primary.Name() + "+" + b.fallback.Name()
}

func (b *FallbackBackend) Search(ctx context.Context, query string, topK int, expand bool) ([]MemoryResult, error) {
	if b == nil || b.primary == nil {
		return nil, fmt.Errorf("memory backend is not configured")
	}

	if disabled, lastErr := b.isDisabled(); disabled {
		if b.fallback != nil {
			return b.fallback.Search(ctx, query, topK, expand)
		}
		return nil, fmt.Errorf("%s memory backend is temporarily unavailable: %w", b.primary.Name(), lastErr)
	}

	results, err := b.primary.Search(ctx, query, topK, expand)
	if err == nil {
		b.recordSuccess()
		return results, nil
	}

	b.recordFailure(err)
	if b.fallback == nil {
		return nil, fmt.Errorf("%s memory backend unavailable: %w", b.primary.Name(), err)
	}

	fallbackResults, fallbackErr := b.fallback.Search(ctx, query, topK, expand)
	if fallbackErr != nil {
		return nil, fmt.Errorf("%s memory backend unavailable (%v); %s fallback failed: %w", b.primary.Name(), err, b.fallback.Name(), fallbackErr)
	}
	return fallbackResults, nil
}

// LastError returns the last primary backend error recorded by the fallback.
func (b *FallbackBackend) LastError() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lastErr == nil {
		return ""
	}
	return b.lastErr.Error()
}

func (b *FallbackBackend) isDisabled() (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lastErr == nil || b.now().After(b.disabledUntil) {
		return false, nil
	}
	return true, b.lastErr
}

func (b *FallbackBackend) recordSuccess() {
	b.mu.Lock()
	b.lastErr = nil
	b.disabledUntil = time.Time{}
	b.mu.Unlock()
}

func (b *FallbackBackend) recordFailure(err error) {
	b.mu.Lock()
	b.lastErr = err
	b.disabledUntil = b.now().Add(b.cooldown)
	b.mu.Unlock()
}
