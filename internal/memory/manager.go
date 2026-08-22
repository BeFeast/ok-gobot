package memory

import (
	"context"
	"fmt"
)

// EmbeddingQueryClient produces one embedding for a search query.
type EmbeddingQueryClient interface {
	GetEmbedding(ctx context.Context, text string) ([]float32, error)
}

// MetadataExtractor extracts structured metadata from raw memory content.
// Kept for API compatibility, but Remember() is deprecated in memory v2.
type MetadataExtractor interface {
	Extract(ctx context.Context, content string) (ChunkMetadata, error)
}

// MemoryManager coordinates embeddings and indexed markdown memory chunk search.
type MemoryManager struct {
	client    EmbeddingQueryClient
	store     *MemoryStore
	backend   Backend
	extractor MetadataExtractor
}

// MemoryManagerOption customizes manager initialization.
type MemoryManagerOption func(*MemoryManager)

// WithMetadataExtractor keeps compatibility with existing configuration wiring.
func WithMetadataExtractor(extractor MetadataExtractor) MemoryManagerOption {
	return func(m *MemoryManager) {
		m.extractor = extractor
	}
}

// WithBackend overrides the built-in SQLite backend used by the manager.
func WithBackend(backend Backend) MemoryManagerOption {
	return func(m *MemoryManager) {
		m.backend = backend
	}
}

// NewMemoryManager creates a new memory manager.
func NewMemoryManager(client EmbeddingQueryClient, store *MemoryStore, opts ...MemoryManagerOption) *MemoryManager {
	manager := &MemoryManager{
		client: client,
		store:  store,
	}
	manager.backend = NewBuiltinBackend(client, store)
	for _, opt := range opts {
		if opt != nil {
			opt(manager)
		}
	}
	return manager
}

// Store returns the manager's underlying memory store, or nil if unset.
// Exposed so callers (e.g. tool registries) can share the same connection
// without re-opening the database.
func (m *MemoryManager) Store() *MemoryStore {
	if m == nil {
		return nil
	}
	return m.store
}

// LexicalIndexAvailable reports whether the manager's lexical index can serve
// keyword queries. For the built-in SQLite backend this is false when the
// binary was compiled without the "sqlite_fts5" build tag. Backends that carry
// their own index (QMD) answer for themselves.
func (m *MemoryManager) LexicalIndexAvailable() bool {
	if m == nil {
		return false
	}
	if m.backend == nil {
		return m.store.LexicalIndexAvailable()
	}
	if backend, ok := m.backend.(interface{ LexicalIndexAvailable() bool }); ok {
		return backend.LexicalIndexAvailable()
	}
	// A backend that maintains its own index (QMD) does not depend on the
	// SQLite FTS5 module being compiled in.
	return true
}

// CountChunks reports how many indexed chunks live under sourcePrefix, or in
// the whole index when sourcePrefix is empty.
func (m *MemoryManager) CountChunks(ctx context.Context, sourcePrefix string) (int, error) {
	if m == nil || m.store == nil {
		return 0, fmt.Errorf("memory manager is not configured")
	}
	return m.store.CountChunks(ctx, sourcePrefix)
}

// Embedder returns the manager's embedding query client, or nil if unset.
func (m *MemoryManager) Embedder() EmbeddingQueryClient {
	if m == nil {
		return nil
	}
	return m.client
}

// Search searches indexed markdown chunks with hybrid lexical and semantic ranking.
func (m *MemoryManager) Search(ctx context.Context, query string, topK int) ([]MemoryResult, error) {
	if m == nil || m.backend == nil {
		return nil, fmt.Errorf("memory manager is not configured")
	}
	return m.backend.Search(ctx, query, topK, false)
}

// SearchScoped searches indexed markdown chunks using a scoped recall policy.
func (m *MemoryManager) SearchScoped(ctx context.Context, query string, topK int, policy *RecallPolicy) ([]MemoryResult, error) {
	if policy == nil {
		return m.Search(ctx, query, topK)
	}
	if m == nil || m.backend == nil {
		return nil, fmt.Errorf("memory manager is not configured")
	}
	if scoped, ok := m.backend.(interface {
		SearchScoped(context.Context, string, int, bool, *RecallPolicy) ([]MemoryResult, error)
	}); ok {
		return scoped.SearchScoped(ctx, query, topK, false, policy)
	}
	results, err := m.backend.Search(ctx, query, topK*3, false)
	if err != nil {
		return nil, err
	}
	filtered, _ := policy.FilterResults(results)
	if len(filtered) > topK && topK > 0 {
		filtered = filtered[:topK]
	}
	return filtered, nil
}

// SearchExpanded searches for matching chunks, then expands each result to
// include all chunks from the same branch (source_file + header_path).
// This gives full section context without replaying the entire history.
func (m *MemoryManager) SearchExpanded(ctx context.Context, query string, topK int) ([]MemoryResult, error) {
	if m == nil || m.backend == nil {
		return nil, fmt.Errorf("memory manager is not configured")
	}
	return m.backend.Search(ctx, query, topK, true)
}

// SearchExpandedScoped searches with policy filtering, then expands allowed
// branches only.
func (m *MemoryManager) SearchExpandedScoped(ctx context.Context, query string, topK int, policy *RecallPolicy) ([]MemoryResult, error) {
	if policy == nil {
		return m.SearchExpanded(ctx, query, topK)
	}
	if m == nil || m.backend == nil {
		return nil, fmt.Errorf("memory manager is not configured")
	}
	if scoped, ok := m.backend.(interface {
		SearchScoped(context.Context, string, int, bool, *RecallPolicy) ([]MemoryResult, error)
	}); ok {
		return scoped.SearchScoped(ctx, query, topK, true, policy)
	}
	results, err := m.backend.Search(ctx, query, topK*3, true)
	if err != nil {
		return nil, err
	}
	filtered, _ := policy.FilterResults(results)
	if len(filtered) > topK && topK > 0 {
		filtered = filtered[:topK]
	}
	return filtered, nil
}

// Recall is kept as a compatibility alias for existing callers.
func (m *MemoryManager) Recall(ctx context.Context, query string, topK int) ([]MemoryResult, error) {
	return m.Search(ctx, query, topK)
}

// Remember is deprecated in v2 where markdown files are canonical.
func (m *MemoryManager) Remember(ctx context.Context, content, category string) error {
	return fmt.Errorf("memory remember is deprecated in v2; write to markdown files and reindex")
}

// ForgetByID is deprecated in v2 where markdown files are canonical.
func (m *MemoryManager) ForgetByID(id int64) error {
	return fmt.Errorf("memory forget is deprecated in v2; edit markdown files and reindex")
}

// ListRecent is deprecated in v2 where markdown files are canonical.
func (m *MemoryManager) ListRecent(limit int) ([]MemoryResult, error) {
	return nil, fmt.Errorf("memory list is deprecated in v2; use memory_search")
}
