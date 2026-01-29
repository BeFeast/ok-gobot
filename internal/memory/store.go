package memory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"time"
)

// MemoryResult represents a memory search result
type MemoryResult struct {
	ID         int64     `json:"id"`
	Content    string    `json:"content"`
	Category   string    `json:"category"`
	Similarity float32   `json:"similarity"`
	CreatedAt  time.Time `json:"created_at"`
}

// MemoryStore handles storage and retrieval of memories with embeddings
type MemoryStore struct {
	db *sql.DB
}

// NewMemoryStore creates a new memory store
func NewMemoryStore(db *sql.DB) (*MemoryStore, error) {
	store := &MemoryStore{db: db}
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate memory tables: %w", err)
	}
	return store, nil
}

// migrate creates the memories table
func (s *MemoryStore) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS memories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT NOT NULL,
			embedding BLOB NOT NULL,
			category TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_memories_category ON memories(category);`,
		`CREATE INDEX IF NOT EXISTS idx_memories_created_at ON memories(created_at);`,
	}

	for _, migration := range migrations {
		if _, err := s.db.Exec(migration); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	return nil
}

// Save stores a memory with its embedding
func (s *MemoryStore) Save(ctx context.Context, content, category string, embedding []float32) error {
	// Encode embedding to binary
	embeddingBytes, err := encodeEmbedding(embedding)
	if err != nil {
		return fmt.Errorf("failed to encode embedding: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		"INSERT INTO memories (content, embedding, category) VALUES (?, ?, ?)",
		content, embeddingBytes, category,
	)
	return err
}

// Search finds the most similar memories using cosine similarity
func (s *MemoryStore) Search(ctx context.Context, queryEmbedding []float32, topK int) ([]MemoryResult, error) {
	if topK <= 0 {
		topK = 5
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, content, embedding, category, created_at
		FROM memories
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query memories: %w", err)
	}
	defer rows.Close()

	var results []MemoryResult
	for rows.Next() {
		var id int64
		var content, category string
		var embeddingBytes []byte
		var createdAt time.Time

		if err := rows.Scan(&id, &content, &embeddingBytes, &category, &createdAt); err != nil {
			continue
		}

		// Decode embedding
		embedding, err := decodeEmbedding(embeddingBytes)
		if err != nil {
			continue
		}

		// Calculate cosine similarity
		similarity := cosineSimilarity(queryEmbedding, embedding)

		results = append(results, MemoryResult{
			ID:         id,
			Content:    content,
			Category:   category,
			Similarity: similarity,
			CreatedAt:  createdAt,
		})
	}

	// Sort by similarity (descending)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})

	// Return top K
	if len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}

// Delete removes a memory by ID
func (s *MemoryStore) Delete(id int64) error {
	result, err := s.db.Exec("DELETE FROM memories WHERE id = ?", id)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return fmt.Errorf("memory not found")
	}

	return nil
}

// List retrieves the most recent memories
func (s *MemoryStore) List(limit int) ([]MemoryResult, error) {
	if limit <= 0 {
		limit = 10
	}

	rows, err := s.db.Query(`
		SELECT id, content, category, created_at
		FROM memories
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query memories: %w", err)
	}
	defer rows.Close()

	var results []MemoryResult
	for rows.Next() {
		var id int64
		var content, category string
		var createdAt time.Time

		if err := rows.Scan(&id, &content, &category, &createdAt); err != nil {
			continue
		}

		results = append(results, MemoryResult{
			ID:        id,
			Content:   content,
			Category:  category,
			CreatedAt: createdAt,
		})
	}

	return results, nil
}

// cosineSimilarity calculates the cosine similarity between two vectors
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return float32(dotProduct / (math.Sqrt(normA) * math.Sqrt(normB)))
}

// encodeEmbedding converts a float32 slice to binary format
func encodeEmbedding(embedding []float32) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, embedding); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeEmbedding converts binary data back to a float32 slice
func decodeEmbedding(data []byte) ([]float32, error) {
	buf := bytes.NewReader(data)
	embedding := make([]float32, len(data)/4)
	if err := binary.Read(buf, binary.LittleEndian, &embedding); err != nil {
		return nil, err
	}
	return embedding, nil
}
