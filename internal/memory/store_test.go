package memory

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float32
		delta    float32
	}{
		{
			name:     "identical vectors",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{1.0, 0.0, 0.0},
			expected: 1.0,
			delta:    0.001,
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{0.0, 1.0, 0.0},
			expected: 0.0,
			delta:    0.001,
		},
		{
			name:     "opposite vectors",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{-1.0, 0.0, 0.0},
			expected: -1.0,
			delta:    0.001,
		},
		{
			name:     "similar vectors",
			a:        []float32{1.0, 1.0, 0.0},
			b:        []float32{1.0, 0.5, 0.0},
			expected: 0.948,
			delta:    0.01,
		},
		{
			name:     "different length vectors",
			a:        []float32{1.0, 0.0},
			b:        []float32{1.0, 0.0, 0.0},
			expected: 0.0,
			delta:    0.001,
		},
		{
			name:     "zero vectors",
			a:        []float32{0.0, 0.0, 0.0},
			b:        []float32{1.0, 0.0, 0.0},
			expected: 0.0,
			delta:    0.001,
		},
		{
			name:     "normalized vectors",
			a:        []float32{0.6, 0.8},
			b:        []float32{0.8, 0.6},
			expected: 0.96,
			delta:    0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cosineSimilarity(tt.a, tt.b)
			diff := result - tt.expected
			if diff < 0 {
				diff = -diff
			}
			if diff > tt.delta {
				t.Errorf("cosineSimilarity(%v, %v) = %f, want %f (delta %f)",
					tt.a, tt.b, result, tt.expected, tt.delta)
			}
		})
	}
}

func TestEncodeDecodeEmbedding(t *testing.T) {
	tests := []struct {
		name      string
		embedding []float32
	}{
		{
			name:      "simple vector",
			embedding: []float32{1.0, 2.0, 3.0},
		},
		{
			name:      "negative values",
			embedding: []float32{-1.5, 2.7, -3.2, 0.5},
		},
		{
			name:      "typical embedding dimensions",
			embedding: make([]float32, 1536),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := range tt.embedding {
				tt.embedding[i] = float32(i) * 0.01
			}

			encoded, err := encodeEmbedding(tt.embedding)
			if err != nil {
				t.Fatalf("encodeEmbedding failed: %v", err)
			}

			decoded, err := decodeEmbedding(encoded)
			if err != nil {
				t.Fatalf("decodeEmbedding failed: %v", err)
			}

			if len(decoded) != len(tt.embedding) {
				t.Fatalf("length mismatch: got %d, want %d", len(decoded), len(tt.embedding))
			}

			for i := range tt.embedding {
				if decoded[i] != tt.embedding[i] {
					t.Errorf("value mismatch at index %d: got %f, want %f", i, decoded[i], tt.embedding[i])
				}
			}
		})
	}
}

func TestMemoryStoreMigrateCreatesV2SchemaAndClearsLegacyMemories(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE memories (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		content TEXT NOT NULL,
		embedding BLOB NOT NULL,
		category TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`); err != nil {
		t.Fatalf("failed to create legacy memories table: %v", err)
	}

	legacyEmbedding, err := encodeEmbedding([]float32{1, 0})
	if err != nil {
		t.Fatalf("failed to encode embedding: %v", err)
	}

	if _, err := db.Exec(
		"INSERT INTO memories (content, embedding, category) VALUES (?, ?, ?)",
		"legacy memory",
		legacyEmbedding,
		"general",
	); err != nil {
		t.Fatalf("failed to seed legacy memories table: %v", err)
	}

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	for _, column := range []string{
		"source_file",
		"header_path",
		"chunk_ordinal",
		"content_hash",
		"indexed_at",
	} {
		ok, err := store.columnExists(memoryChunksTable, column)
		if err != nil {
			t.Fatalf("columnExists(%q) failed: %v", column, err)
		}
		if !ok {
			t.Fatalf("expected v2 column %q to exist", column)
		}
	}

	var legacyRows int
	if err := db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&legacyRows); err != nil {
		t.Fatalf("failed to count memories rows: %v", err)
	}
	if legacyRows != 0 {
		t.Fatalf("expected legacy memories table to be empty, got %d row(s)", legacyRows)
	}

	var migratedRows int
	if err := db.QueryRow("SELECT COUNT(*) FROM memory_chunks WHERE source_file = ?", defaultMigratedSource).Scan(&migratedRows); err != nil {
		t.Fatalf("failed to count migrated chunks: %v", err)
	}
	if migratedRows != 1 {
		t.Fatalf("expected 1 migrated chunk, got %d", migratedRows)
	}
}

func TestMemoryStoreMigrateRecreatesLegacyMemoryChunksSchema(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE memory_chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source TEXT NOT NULL,
		header_path TEXT NOT NULL DEFAULT '',
		start_line INTEGER NOT NULL DEFAULT 1,
		end_line INTEGER NOT NULL DEFAULT 1,
		content TEXT NOT NULL,
		embedding BLOB NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`); err != nil {
		t.Fatalf("failed to create legacy memory_chunks table: %v", err)
	}

	embeddingBytes, err := encodeEmbedding([]float32{0.3, 0.7})
	if err != nil {
		t.Fatalf("failed to encode legacy embedding: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO memory_chunks (source, header_path, start_line, end_line, content, embedding)
		VALUES (?, ?, ?, ?, ?, ?)
	`, "MEMORY.md", "root", 1, 2, "legacy chunk", embeddingBytes); err != nil {
		t.Fatalf("failed to seed legacy memory_chunks table: %v", err)
	}

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	if err := store.IndexChunk(context.Background(), "MEMORY.md", "root", 1, 2, "content", []float32{1, 0}); err != nil {
		t.Fatalf("IndexChunk failed after migration: %v", err)
	}

	var hasSourceFile int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('memory_chunks') WHERE name='source_file'
	`).Scan(&hasSourceFile); err != nil {
		t.Fatalf("failed to inspect migrated memory_chunks columns: %v", err)
	}
	if hasSourceFile != 1 {
		t.Fatalf("expected migrated memory_chunks table to contain source_file column")
	}
}

func TestMemoryStoreSearchChunks(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	ctx := context.Background()
	if err := store.IndexChunk(
		ctx,
		"MEMORY.md",
		"projects > ok-gobot",
		12,
		16,
		"Decided to use markdown-first memory indexing.",
		[]float32{1, 0},
	); err != nil {
		t.Fatalf("failed to index chunk 1: %v", err)
	}

	if err := store.IndexChunk(
		ctx,
		"memory/2026-03-04.md",
		"journal",
		4,
		7,
		"Bought groceries and walked the dog.",
		[]float32{0, 1},
	); err != nil {
		t.Fatalf("failed to index chunk 2: %v", err)
	}

	results, err := store.SearchChunks(ctx, []float32{0.9, 0.1}, 1)
	if err != nil {
		t.Fatalf("SearchChunks failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	got := results[0]
	if got.Source != "MEMORY.md" {
		t.Fatalf("unexpected source: got %q", got.Source)
	}
	if got.SourceFile != "MEMORY.md" {
		t.Fatalf("unexpected source_file: got %q", got.SourceFile)
	}
	if got.HeaderPath != "projects > ok-gobot" {
		t.Fatalf("unexpected header path: got %q", got.HeaderPath)
	}
	if got.ChunkOrdinal != 12 {
		t.Fatalf("unexpected chunk ordinal: got %d", got.ChunkOrdinal)
	}
	if got.ContentHash == "" {
		t.Fatal("expected content hash to be populated")
	}
	if got.Similarity <= 0 {
		t.Fatalf("expected positive similarity, got %f", got.Similarity)
	}
}

func TestMemoryStoreLegacyMutationsAreDeprecated(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	if err := store.Save(context.Background(), "x", "general", []float32{1}); err == nil || !strings.Contains(err.Error(), "deprecated") {
		t.Fatalf("expected deprecated save error, got %v", err)
	}
	if err := store.Delete(1); err == nil || !strings.Contains(err.Error(), "deprecated") {
		t.Fatalf("expected deprecated delete error, got %v", err)
	}
	if _, err := store.List(10); err == nil || !strings.Contains(err.Error(), "deprecated") {
		t.Fatalf("expected deprecated list error, got %v", err)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}
