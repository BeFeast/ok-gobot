package memory

import (
	"context"
	"fmt"
	"strings"
)

// MetadataExtractor extracts structured metadata from raw memory content.
// Kept for API compatibility, but Remember() is deprecated in memory v2.
type MetadataExtractor interface {
	Extract(ctx context.Context, content string) (ChunkMetadata, error)
}

// MemoryManager coordinates embeddings and indexed markdown memory chunk search.
type MemoryManager struct {
	client    *EmbeddingClient
	store     *MemoryStore
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

// NewMemoryManager creates a new memory manager.
func NewMemoryManager(client *EmbeddingClient, store *MemoryStore, opts ...MemoryManagerOption) *MemoryManager {
	manager := &MemoryManager{
		client: client,
		store:  store,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(manager)
		}
	}
	return manager
}

// Search searches indexed markdown chunks by semantic similarity.
func (m *MemoryManager) Search(ctx context.Context, query string, topK int) ([]MemoryResult, error) {
	if m.client == nil || m.store == nil {
		return nil, fmt.Errorf("memory manager is not fully configured")
	}

	queryEmbedding, err := m.client.GetEmbedding(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to generate query embedding: %w", err)
	}

	results, err := m.store.SearchChunks(ctx, queryEmbedding, topK)
	if err != nil {
		return nil, fmt.Errorf("failed to search memory chunks: %w", err)
	}

	return results, nil
}

// SearchExpanded searches for matching chunks, then expands each result to
// include all chunks from the same branch (source_file + header_path).
// This gives full section context without replaying the entire history.
func (m *MemoryManager) SearchExpanded(ctx context.Context, query string, topK int) ([]MemoryResult, error) {
	if m.client == nil || m.store == nil {
		return nil, fmt.Errorf("memory manager is not fully configured")
	}

	hits, err := m.Search(ctx, query, topK)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}

	type branch struct{ file, header string }
	var branches []branch
	seen := make(map[branch]bool)
	bestScores := make(map[branch]float32)

	for _, h := range hits {
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
	for _, b := range branches {
		chunks, err := m.store.GetBranchChunks(ctx, b.file, b.header)
		if err != nil || len(chunks) == 0 {
			continue
		}

		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Content
		}

		expanded = append(expanded, MemoryResult{
			Source:     b.file,
			SourceFile: b.file,
			HeaderPath: b.header,
			Content:    strings.Join(texts, "\n\n"),
			Similarity: bestScores[b],
		})
	}

	return expanded, nil
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
