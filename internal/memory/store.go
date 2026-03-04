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

const (
	memoryChunksTable     = "memory_chunks"
	defaultMigratedSource = "legacy://migrated"
	defaultHeaderPath     = "root"
)

// MemoryResult represents an indexed markdown memory search result.
// Some fields are aliases kept for compatibility with existing callers.
type MemoryResult struct {
	ID int64 `json:"id"`

	Source     string `json:"source"`
	SourceFile string `json:"source_file"`
	HeaderPath string `json:"header_path"`

	StartLine    int `json:"start_line"`
	EndLine      int `json:"end_line"`
	ChunkOrdinal int `json:"chunk_ordinal"`

	Content     string  `json:"content"`
	ContentHash string  `json:"content_hash"`
	Similarity  float32 `json:"similarity"`

	UpdatedAt time.Time `json:"updated_at"`
	IndexedAt time.Time `json:"indexed_at"`
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
func (s *MemoryStore) migrate() error {
	if err := s.ensureLegacyMemoriesTable(); err != nil {
		return err
	}

	hasChunksTable, err := s.tableExists(memoryChunksTable)
	if err != nil {
		return fmt.Errorf("failed to inspect %s table: %w", memoryChunksTable, err)
	}

	if hasChunksTable && !s.hasMemoryChunksV2Shape() {
		legacyBackup := fmt.Sprintf("memory_chunks_legacy_%d", time.Now().UnixNano())
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin memory table recreation: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck

		if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s RENAME TO %s`, memoryChunksTable, legacyBackup)); err != nil {
			return fmt.Errorf("backup legacy memory_chunks table: %w", err)
		}
		if err := createChunksSchemaTx(tx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit memory table recreation: %w", err)
		}

		_ = s.importLegacyRows(legacyBackup)
	} else if !hasChunksTable {
		if err := s.createChunksSchema(); err != nil {
			return err
		}
	}

	if hasLegacyTable, err := s.tableExists("memories"); err == nil && hasLegacyTable {
		_ = s.importLegacyRows("memories")
		if _, err := s.db.Exec(`DELETE FROM memories`); err != nil {
			return fmt.Errorf("clear legacy memories table: %w", err)
		}
	}

	if err := s.ensureChunksIndexes(); err != nil {
		return err
	}
	return nil
}

func (s *MemoryStore) ensureLegacyMemoriesTable() error {
	statements := []string{
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
	for _, stmt := range statements {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("legacy migration failed: %w", err)
		}
	}
	return nil
}

func (s *MemoryStore) createChunksSchema() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin memory schema migration: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := createChunksSchemaTx(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit memory schema migration: %w", err)
	}
	return nil
}

func createChunksSchemaTx(tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS memory_chunks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source_file TEXT NOT NULL,
			header_path TEXT NOT NULL,
			chunk_ordinal INTEGER NOT NULL DEFAULT 0,
			content TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			embedding BLOB NOT NULL,
			indexed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(source_file, header_path, chunk_ordinal)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_source_file ON memory_chunks(source_file);`,
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_indexed_at ON memory_chunks(indexed_at);`,
	}

	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("memory migration failed: %w", err)
		}
	}
	return nil
}

func (s *MemoryStore) ensureChunksIndexes() error {
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_source_file ON memory_chunks(source_file);`,
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_indexed_at ON memory_chunks(indexed_at);`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("create memory index failed: %w", err)
		}
	}
	return nil
}

func (s *MemoryStore) hasMemoryChunksV2Shape() bool {
	required := []string{
		"id",
		"source_file",
		"header_path",
		"chunk_ordinal",
		"content",
		"content_hash",
		"embedding",
		"indexed_at",
	}
	for _, column := range required {
		ok, err := s.columnExists(memoryChunksTable, column)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

func (s *MemoryStore) importLegacyRows(table string) error {
	hasContent, err := s.columnExists(table, "content")
	if err != nil || !hasContent {
		return nil
	}
	hasEmbedding, err := s.columnExists(table, "embedding")
	if err != nil || !hasEmbedding {
		return nil
	}

	hasSourceFile, _ := s.columnExists(table, "source_file")
	hasSource, _ := s.columnExists(table, "source")
	hasHeaderPath, _ := s.columnExists(table, "header_path")
	hasCategory, _ := s.columnExists(table, "category")
	hasChunkOrdinal, _ := s.columnExists(table, "chunk_ordinal")
	hasStartLine, _ := s.columnExists(table, "start_line")

	fields := []string{"content", "embedding"}
	includeSource := hasSourceFile || hasSource
	includeHeader := hasHeaderPath || hasCategory
	includeOrdinal := hasChunkOrdinal || hasStartLine

	if hasSourceFile {
		fields = append(fields, "source_file")
	} else if hasSource {
		fields = append(fields, "source")
	}
	if hasHeaderPath {
		fields = append(fields, "header_path")
	} else if hasCategory {
		fields = append(fields, "category")
	}
	if hasChunkOrdinal {
		fields = append(fields, "chunk_ordinal")
	} else if hasStartLine {
		fields = append(fields, "start_line")
	}

	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(fields, ", "), table)
	rows, err := s.db.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	type legacyRow struct {
		source     string
		headerPath string
		ordinal    int
		hasOrdinal bool
		content    string
		embedding  []byte
	}

	collected := make([]legacyRow, 0, 64)
	for rows.Next() {
		var (
			content   string
			embedding []byte
			source    sql.NullString
			header    sql.NullString
			ordinal   sql.NullInt64
		)

		args := []interface{}{&content, &embedding}
		if includeSource {
			args = append(args, &source)
		}
		if includeHeader {
			args = append(args, &header)
		}
		if includeOrdinal {
			args = append(args, &ordinal)
		}
		if err := rows.Scan(args...); err != nil {
			continue
		}

		sourceValue := strings.TrimSpace(source.String)
		if sourceValue == "" {
			sourceValue = defaultMigratedSource
		}
		headerPathValue := strings.TrimSpace(header.String)
		if headerPathValue == "" {
			headerPathValue = defaultHeaderPath
		}

		collected = append(collected, legacyRow{
			source:     sourceValue,
			headerPath: headerPathValue,
			ordinal:    int(ordinal.Int64),
			hasOrdinal: ordinal.Valid,
			content:    content,
			embedding:  embedding,
		})
	}
	if len(collected) == 0 {
		return nil
	}

	nextOrdinalByKey := make(map[string]int)
	for _, row := range collected {
		ordinal := row.ordinal
		if !row.hasOrdinal || ordinal < 0 {
			key := row.source + "\x00" + row.headerPath
			ordinal = nextOrdinalByKey[key]
			nextOrdinalByKey[key] = ordinal + 1
		}

		_, _ = s.db.Exec(`
			INSERT OR IGNORE INTO memory_chunks
				(source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at)
			VALUES
				(?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		`, row.source, row.headerPath, ordinal, row.content, hashChunkContent(row.content), row.embedding)
	}

	return nil
}

// IndexChunk stores or updates a markdown chunk in the index table.
func (s *MemoryStore) IndexChunk(ctx context.Context, source, headerPath string, startLine, endLine int, content string, embedding []float32) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return fmt.Errorf("source is required")
	}

	headerPath = strings.TrimSpace(headerPath)
	if headerPath == "" {
		headerPath = defaultHeaderPath
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("content is empty")
	}

	_ = endLine // line ranges are currently represented by chunk ordinal in v2 schema.

	ordinal := startLine
	if ordinal <= 0 {
		nextOrdinal, err := s.nextOrdinal(ctx, source, headerPath)
		if err != nil {
			return fmt.Errorf("resolve next chunk ordinal: %w", err)
		}
		ordinal = nextOrdinal
	}

	embeddingBytes, err := encodeEmbedding(embedding)
	if err != nil {
		return fmt.Errorf("failed to encode embedding: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO memory_chunks
			(source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at)
		VALUES
			(?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(source_file, header_path, chunk_ordinal) DO UPDATE SET
			content = excluded.content,
			content_hash = excluded.content_hash,
			embedding = excluded.embedding,
			indexed_at = CURRENT_TIMESTAMP`,
		source,
		headerPath,
		ordinal,
		content,
		hashChunkContent(content),
		embeddingBytes,
	)
	return err
}

func (s *MemoryStore) nextOrdinal(ctx context.Context, sourceFile, headerPath string) (int, error) {
	var next int
	err := s.db.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(chunk_ordinal), -1) + 1
		 FROM memory_chunks
		 WHERE source_file = ? AND header_path = ?`,
		sourceFile,
		headerPath,
	).Scan(&next)
	return next, err
}

// SearchChunks finds the most similar indexed chunks using cosine similarity.
func (s *MemoryStore) SearchChunks(ctx context.Context, queryEmbedding []float32, topK int) ([]MemoryResult, error) {
	if topK <= 0 {
		topK = 5
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at
		FROM memory_chunks
		ORDER BY indexed_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query memory chunks: %w", err)
	}
	defer rows.Close()

	var results []MemoryResult
	for rows.Next() {
		var (
			id           int64
			sourceFile   string
			headerPath   string
			chunkOrdinal int
			content      string
			contentHash  string
			embeddingRaw []byte
			indexedAt    time.Time
		)

		if err := rows.Scan(
			&id,
			&sourceFile,
			&headerPath,
			&chunkOrdinal,
			&content,
			&contentHash,
			&embeddingRaw,
			&indexedAt,
		); err != nil {
			continue
		}

		embedding, err := decodeEmbedding(embeddingRaw)
		if err != nil {
			continue
		}

		similarity := cosineSimilarity(queryEmbedding, embedding)
		results = append(results, MemoryResult{
			ID:           id,
			Source:       sourceFile,
			SourceFile:   sourceFile,
			HeaderPath:   headerPath,
			StartLine:    chunkOrdinal,
			EndLine:      chunkOrdinal,
			ChunkOrdinal: chunkOrdinal,
			Content:      content,
			ContentHash:  contentHash,
			Similarity:   similarity,
			UpdatedAt:    indexedAt,
			IndexedAt:    indexedAt,
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
