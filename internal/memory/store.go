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

const memoryChunksTable = "memory_chunks"

// MemoryResult represents an indexed markdown memory search result.
type MemoryResult struct {
	ID         int64     `json:"id"`
	Source     string    `json:"source"`
	HeaderPath string    `json:"header_path"`
	StartLine  int       `json:"start_line"`
	EndLine    int       `json:"end_line"`
	Content    string    `json:"content"`
	Similarity float32   `json:"similarity"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// MemoryStore handles storage and retrieval of memory chunks with embeddings.
// Markdown files are the source of truth; SQLite is index-only.
type MemoryStore struct {
	db *sql.DB
}

// NewMemoryStore creates a new memory store.
func NewMemoryStore(db *sql.DB) (*MemoryStore, error) {
	store := &MemoryStore{db: db}
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate memory tables: %w", err)
	}
	return store, nil
}

// migrate creates/updates memory index tables.
// The legacy memories table is retained only for rollback compatibility and kept empty.
func (s *MemoryStore) migrate() error {
	legacyMigrations := []string{
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
	for _, migration := range legacyMigrations {
		if _, err := s.db.Exec(migration); err != nil {
			return fmt.Errorf("legacy migration failed: %w", err)
		}
	}

	hasChunksTable, err := s.tableExists(memoryChunksTable)
	if err != nil {
		return fmt.Errorf("failed to inspect %s table: %w", memoryChunksTable, err)
	}

	if hasChunksTable {
		hasSourceColumn, err := s.columnExists(memoryChunksTable, "source")
		if err != nil {
			return fmt.Errorf("failed to inspect %s schema: %w", memoryChunksTable, err)
		}
		if !hasSourceColumn {
			if _, err := s.db.Exec(`ALTER TABLE memory_chunks RENAME TO memory_chunks_legacy_v1`); err != nil {
				return fmt.Errorf("failed to rename legacy memory_chunks table: %w", err)
			}
			hasChunksTable = false
		}
	}

	if !hasChunksTable {
		if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS memory_chunks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source TEXT NOT NULL,
			header_path TEXT NOT NULL DEFAULT '',
			start_line INTEGER NOT NULL DEFAULT 1,
			end_line INTEGER NOT NULL DEFAULT 1,
			content TEXT NOT NULL,
			embedding BLOB NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`); err != nil {
			return fmt.Errorf("failed to create memory_chunks table: %w", err)
		}
	}

	chunkIndexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_source ON memory_chunks(source);`,
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_header_path ON memory_chunks(header_path);`,
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_updated_at ON memory_chunks(updated_at);`,
	}
	for _, stmt := range chunkIndexes {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("failed to create memory_chunks index: %w", err)
		}
	}

	if _, err := s.db.Exec(`DROP TABLE IF EXISTS memory_chunks_legacy_v1`); err != nil {
		return fmt.Errorf("failed to drop legacy memory_chunks table: %w", err)
	}

	if _, err := s.db.Exec(`DELETE FROM memories`); err != nil {
		return fmt.Errorf("failed to clear legacy memories table: %w", err)
	}

	return nil
}

// IndexChunk stores a markdown chunk and its embedding in the index table.
func (s *MemoryStore) IndexChunk(ctx context.Context, source, headerPath string, startLine, endLine int, content string, embedding []float32) error {
	embeddingBytes, err := encodeEmbedding(embedding)
	if err != nil {
		return fmt.Errorf("failed to encode embedding: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO memory_chunks (source, header_path, start_line, end_line, content, embedding, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		source, headerPath, startLine, endLine, content, embeddingBytes,
	)
	return err
}

// SearchChunks finds the most similar indexed chunks using cosine similarity.
func (s *MemoryStore) SearchChunks(ctx context.Context, queryEmbedding []float32, topK int) ([]MemoryResult, error) {
	if topK <= 0 {
		topK = 5
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source, header_path, start_line, end_line, content, embedding, updated_at
		FROM memory_chunks
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query memory chunks: %w", err)
	}
	defer rows.Close()

	var results []MemoryResult
	for rows.Next() {
		var id int64
		var source, headerPath, content string
		var startLine, endLine int
		var embeddingBytes []byte
		var updatedAt time.Time

		if err := rows.Scan(&id, &source, &headerPath, &startLine, &endLine, &content, &embeddingBytes, &updatedAt); err != nil {
			continue
		}

		embedding, err := decodeEmbedding(embeddingBytes)
		if err != nil {
			continue
		}

		similarity := cosineSimilarity(queryEmbedding, embedding)

		results = append(results, MemoryResult{
			ID:         id,
			Source:     source,
			HeaderPath: headerPath,
			StartLine:  startLine,
			EndLine:    endLine,
			Content:    content,
			Similarity: similarity,
			UpdatedAt:  updatedAt,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})

	if len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}

// Search is kept as a compatibility alias for callers that still use the old name.
func (s *MemoryStore) Search(ctx context.Context, queryEmbedding []float32, topK int) ([]MemoryResult, error) {
	return s.SearchChunks(ctx, queryEmbedding, topK)
}

// Save is deprecated in v2 where markdown is canonical.
func (s *MemoryStore) Save(ctx context.Context, content, category string, embedding []float32) error {
	return fmt.Errorf("memory save is deprecated in v2; index markdown chunks instead")
}

// Delete is deprecated in v2 where markdown is canonical.
func (s *MemoryStore) Delete(id int64) error {
	return fmt.Errorf("memory delete is deprecated in v2; update markdown source files instead")
}

// List is deprecated in v2 where markdown is canonical.
func (s *MemoryStore) List(limit int) ([]MemoryResult, error) {
	return nil, fmt.Errorf("memory list is deprecated in v2; use memory_search over indexed chunks")
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

// cosineSimilarity calculates the cosine similarity between two vectors.
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

// encodeEmbedding converts a float32 slice to binary format.
func encodeEmbedding(embedding []float32) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, embedding); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeEmbedding converts binary data back to a float32 slice.
func decodeEmbedding(data []byte) ([]float32, error) {
	buf := bytes.NewReader(data)
	embedding := make([]float32, len(data)/4)
	if err := binary.Read(buf, binary.LittleEndian, &embedding); err != nil {
		return nil, err
	}
	return embedding, nil
}
