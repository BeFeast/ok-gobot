package memory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const memoryChunksTable = "memory_chunks"

// MemoryResult represents a memory search result
type MemoryResult struct {
	ID         int64         `json:"id"`
	Content    string        `json:"content"`
	Category   string        `json:"category"`
	Similarity float32       `json:"similarity"`
	Metadata   ChunkMetadata `json:"metadata"`
	CreatedAt  time.Time     `json:"created_at"`
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

// migrate ensures the memory_chunks table exists with metadata support.
func (s *MemoryStore) migrate() error {
	hasChunksTable, err := s.tableExists(memoryChunksTable)
	if err != nil {
		return fmt.Errorf("failed to inspect %s table: %w", memoryChunksTable, err)
	}
	if !hasChunksTable {
		hasLegacyTable, err := s.tableExists("memories")
		if err != nil {
			return fmt.Errorf("failed to inspect legacy memories table: %w", err)
		}
		if hasLegacyTable {
			if _, err := s.db.Exec(`ALTER TABLE memories RENAME TO memory_chunks`); err != nil {
				return fmt.Errorf("failed to rename legacy memories table: %w", err)
			}
		}
	}

	migrations := []string{
		`CREATE TABLE IF NOT EXISTS memory_chunks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT NOT NULL,
			embedding BLOB NOT NULL,
			category TEXT,
			metadata TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_category ON memory_chunks(category);`,
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_created_at ON memory_chunks(created_at);`,
	}

	for _, migration := range migrations {
		if _, err := s.db.Exec(migration); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	hasMetadataColumn, err := s.columnExists(memoryChunksTable, "metadata")
	if err != nil {
		return fmt.Errorf("failed to inspect metadata column: %w", err)
	}
	if !hasMetadataColumn {
		if _, err := s.db.Exec(`ALTER TABLE memory_chunks ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`); err != nil {
			return fmt.Errorf("failed to add metadata column: %w", err)
		}
	}

	return nil
}

// Save stores a memory with its embedding
func (s *MemoryStore) Save(ctx context.Context, content, category string, embedding []float32) error {
	return s.SaveWithMetadata(ctx, content, category, embedding, ChunkMetadata{})
}

// SaveWithMetadata stores a memory with embedding and structured metadata.
func (s *MemoryStore) SaveWithMetadata(ctx context.Context, content, category string, embedding []float32, metadata ChunkMetadata) error {
	// Encode embedding to binary
	embeddingBytes, err := encodeEmbedding(embedding)
	if err != nil {
		return fmt.Errorf("failed to encode embedding: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		"INSERT INTO memory_chunks (content, embedding, category, metadata) VALUES (?, ?, ?, ?)",
		content, embeddingBytes, category, metadata.toJSON(),
	)
	return err
}

// Search finds the most similar memories using cosine similarity
func (s *MemoryStore) Search(ctx context.Context, queryEmbedding []float32, topK int) ([]MemoryResult, error) {
	return s.SearchWithFilter(ctx, queryEmbedding, topK, MemorySearchFilter{})
}

// SearchWithFilter finds the most similar memories and applies metadata filters.
func (s *MemoryStore) SearchWithFilter(ctx context.Context, queryEmbedding []float32, topK int, filter MemorySearchFilter) ([]MemoryResult, error) {
	if topK <= 0 {
		topK = 5
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, content, embedding, category, metadata, created_at
		FROM memory_chunks
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
		var metadataRaw string
		var createdAt time.Time

		if err := rows.Scan(&id, &content, &embeddingBytes, &category, &metadataRaw, &createdAt); err != nil {
			continue
		}

		metadata := parseChunkMetadata(metadataRaw)
		if !matchesSearchFilter(metadata, filter) {
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
			Metadata:   metadata,
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
	result, err := s.db.Exec("DELETE FROM memory_chunks WHERE id = ?", id)
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
		SELECT id, content, category, metadata, created_at
		FROM memory_chunks
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
		var metadataRaw string
		var createdAt time.Time

		if err := rows.Scan(&id, &content, &category, &metadataRaw, &createdAt); err != nil {
			continue
		}

		results = append(results, MemoryResult{
			ID:        id,
			Content:   content,
			Category:  category,
			Metadata:  parseChunkMetadata(metadataRaw),
			CreatedAt: createdAt,
		})
	}

	return results, nil
}

func matchesSearchFilter(metadata ChunkMetadata, filter MemorySearchFilter) bool {
	if strings.TrimSpace(filter.Person) != "" && !containsFold(metadata.People, filter.Person) {
		return false
	}
	return true
}

func (s *MemoryStore) tableExists(name string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name,
	).Scan(&count)
	return count > 0, err
}

func (s *MemoryStore) columnExists(tableName, columnName string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			defaultV   sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultV, &primaryKey); err != nil {
			return false, err
		}
		if name == columnName {
			return true, nil
		}
	}
	return false, rows.Err()
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
