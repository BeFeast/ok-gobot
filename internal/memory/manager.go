package memory

import (
	"context"
	"fmt"
)

// MetadataExtractor extracts structured metadata from raw memory content.
type MetadataExtractor interface {
	Extract(ctx context.Context, content string) (ChunkMetadata, error)
}

// MemoryManager coordinates embeddings and storage
type MemoryManager struct {
	client    *EmbeddingClient
	store     *MemoryStore
	extractor MetadataExtractor
}

// MemoryManagerOption customizes manager initialization.
type MemoryManagerOption func(*MemoryManager)

// WithMetadataExtractor enables metadata extraction during Remember().
func WithMetadataExtractor(extractor MetadataExtractor) MemoryManagerOption {
	return func(m *MemoryManager) {
		m.extractor = extractor
	}
}

// NewMemoryManager creates a new memory manager
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

// Remember stores a new memory with its embedding
func (m *MemoryManager) Remember(ctx context.Context, content, category string) error {
	// Generate embedding
	embedding, err := m.client.GetEmbedding(ctx, content)
	if err != nil {
		return fmt.Errorf("failed to generate embedding: %w", err)
	}

	metadata := ChunkMetadata{}
	if m.extractor != nil {
		if extracted, err := m.extractor.Extract(ctx, content); err == nil {
			metadata = extracted
		}
	}

	// Store memory
	if err := m.store.SaveWithMetadata(ctx, content, category, embedding, metadata); err != nil {
		return fmt.Errorf("failed to store memory: %w", err)
	}

	return nil
}

// Recall searches for relevant memories based on a query
func (m *MemoryManager) Recall(ctx context.Context, query string, topK int) ([]MemoryResult, error) {
	return m.RecallWithFilter(ctx, query, topK, MemorySearchFilter{})
}

// RecallWithFilter searches for relevant memories with optional metadata filters.
func (m *MemoryManager) RecallWithFilter(ctx context.Context, query string, topK int, filter MemorySearchFilter) ([]MemoryResult, error) {
	// Generate query embedding
	queryEmbedding, err := m.client.GetEmbedding(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to generate query embedding: %w", err)
	}

	// Search for similar memories
	results, err := m.store.SearchWithFilter(ctx, queryEmbedding, topK, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to search memories: %w", err)
	}

	return results, nil
}

// ForgetByID removes a memory by its ID
func (m *MemoryManager) ForgetByID(id int64) error {
	return m.store.Delete(id)
}

// ListRecent returns the most recent memories
func (m *MemoryManager) ListRecent(limit int) ([]MemoryResult, error) {
	return m.store.List(limit)
}
