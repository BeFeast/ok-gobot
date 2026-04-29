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

// Search searches indexed markdown chunks using hybrid lexical/vector ranking.
func (m *MemoryManager) Search(ctx context.Context, query string, topK int) ([]MemoryResult, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("memory manager is not fully configured")
	}

	var queryEmbedding []float32
	if m.client != nil {
		embedding, err := m.client.GetEmbedding(ctx, query)
		if err == nil {
			queryEmbedding = embedding
		}
	}

	results, err := m.store.SearchChunksHybrid(ctx, query, queryEmbedding, topK)
	if err != nil {
		return nil, fmt.Errorf("failed to search memory chunks: %w", err)
	}

	return results, nil
}

// SearchExpanded searches for matching chunks, then expands each result to
// include all chunks from the same branch (source_file + header_path).
// This gives full section context without replaying the entire history.
func (m *MemoryManager) SearchExpanded(ctx context.Context, query string, topK int) ([]MemoryResult, error) {
	if m == nil || m.store == nil {
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
	bestByBranch := make(map[branch]MemoryResult)

	for _, h := range hits {
		b := branch{h.SourceFile, h.HeaderPath}
		if best, ok := bestByBranch[b]; !ok || h.Score > best.Score {
			bestByBranch[b] = h
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

		best := bestByBranch[b]
		expanded = append(expanded, MemoryResult{
			ID:           best.ID,
			Source:       b.file,
			SourceFile:   b.file,
			HeaderPath:   b.header,
			StartLine:    best.StartLine,
			EndLine:      best.EndLine,
			ChunkOrdinal: best.ChunkOrdinal,
			Content:      strings.Join(texts, "\n\n"),
			ContentHash:  best.ContentHash,
			Similarity:   best.Similarity,
			Score:        best.Score,
			LexicalScore: best.LexicalScore,
			VectorScore:  best.VectorScore,
			HybridScore:  best.HybridScore,
			UpdatedAt:    best.UpdatedAt,
			IndexedAt:    best.IndexedAt,
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
