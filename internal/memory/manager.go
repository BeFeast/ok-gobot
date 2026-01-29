package memory

import (
	"context"
	"fmt"
)

// MemoryManager coordinates embeddings and storage
type MemoryManager struct {
	client *EmbeddingClient
	store  *MemoryStore
}

// NewMemoryManager creates a new memory manager
func NewMemoryManager(client *EmbeddingClient, store *MemoryStore) *MemoryManager {
	return &MemoryManager{
		client: client,
		store:  store,
	}
}

// Remember stores a new memory with its embedding
func (m *MemoryManager) Remember(ctx context.Context, content, category string) error {
	// Generate embedding
	embedding, err := m.client.GetEmbedding(ctx, content)
	if err != nil {
		return fmt.Errorf("failed to generate embedding: %w", err)
	}

	// Store memory
	if err := m.store.Save(ctx, content, category, embedding); err != nil {
		return fmt.Errorf("failed to store memory: %w", err)
	}

	return nil
}

// Recall searches for relevant memories based on a query
func (m *MemoryManager) Recall(ctx context.Context, query string, topK int) ([]MemoryResult, error) {
	// Generate query embedding
	queryEmbedding, err := m.client.GetEmbedding(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to generate query embedding: %w", err)
	}

	// Search for similar memories
	results, err := m.store.Search(ctx, queryEmbedding, topK)
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
