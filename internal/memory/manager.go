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

// RecallSearchResult includes both returned memory and policy decisions.
type RecallSearchResult struct {
	Results   []MemoryResult
	Decisions []RecallDecision
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

// SearchScoped searches indexed markdown chunks while enforcing the supplied recall policy.
func (m *MemoryManager) SearchScoped(ctx context.Context, query string, topK int, policy *RecallPolicy) (RecallSearchResult, error) {
	return m.searchScoped(ctx, query, topK, false, policy)
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

// SearchExpandedScoped searches under policy and expands matching branches.
func (m *MemoryManager) SearchExpandedScoped(ctx context.Context, query string, topK int, policy *RecallPolicy) (RecallSearchResult, error) {
	return m.searchScoped(ctx, query, topK, true, policy)
}

func (m *MemoryManager) searchScoped(ctx context.Context, query string, topK int, expand bool, policy *RecallPolicy) (RecallSearchResult, error) {
	if m == nil || m.backend == nil {
		return RecallSearchResult{}, fmt.Errorf("memory manager is not configured")
	}
	if policy == nil {
		results, err := m.backend.Search(ctx, query, topK, expand)
		return RecallSearchResult{Results: results}, err
	}

	limit := normalizeMemoryTopK(topK)
	candidateLimit := limit * 20
	if candidateLimit < 50 {
		candidateLimit = 50
	}
	if candidateLimit > 200 {
		candidateLimit = 200
	}

	results, err := m.backend.Search(ctx, query, candidateLimit, expand)
	if err != nil {
		return RecallSearchResult{}, err
	}
	filtered, decisions := filterRecallResults(results, limit, policy)
	return RecallSearchResult{Results: filtered, Decisions: decisions}, nil
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

func filterRecallResults(results []MemoryResult, topK int, policy *RecallPolicy) ([]MemoryResult, []RecallDecision) {
	limit := normalizeMemoryTopK(topK)
	if policy == nil {
		if len(results) > limit {
			return results[:limit], nil
		}
		return results, nil
	}

	filtered := make([]MemoryResult, 0, min(len(results), limit))
	decisions := make([]RecallDecision, 0)
	seenDecisions := make(map[string]struct{})

	for _, result := range results {
		source := result.SourceFile
		if source == "" {
			source = result.Source
		}
		decision := policy.DecisionForSource(source)
		decisionKey := fmt.Sprintf("%s\x00%t\x00%s", decision.Source, decision.Allowed, decision.Reason)
		if _, ok := seenDecisions[decisionKey]; !ok {
			seenDecisions[decisionKey] = struct{}{}
			decisions = append(decisions, decision)
		}
		if !decision.Allowed {
			continue
		}
		result.Content = RedactMemorySnippet(result.Content)
		filtered = append(filtered, result)
		if len(filtered) >= limit {
			break
		}
	}

	return filtered, decisions
}
