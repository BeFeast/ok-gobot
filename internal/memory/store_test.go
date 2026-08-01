package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

func TestMemoryStoreCreatesFTSIndexWhenAvailable(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}
	if !store.ftsAvailable {
		t.Skip("sqlite fts5 is not available in this build")
	}

	ok, err := store.tableExists(memoryChunksFTSTable)
	if err != nil {
		t.Fatalf("tableExists(%q) failed: %v", memoryChunksFTSTable, err)
	}
	if !ok {
		t.Fatalf("expected %s table to exist", memoryChunksFTSTable)
	}
}

func TestMemoryStoreSearchTextWorksWithoutEmbeddings(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	ctx := context.Background()
	if err := store.IndexChunk(ctx, "MEMORY.md", "Projects > Coffee", 7, 8, "Alice keeps espresso calibration notes.", nil); err != nil {
		t.Fatalf("IndexChunk failed: %v", err)
	}
	if err := store.IndexChunk(ctx, "memory/other.md", "Journal", 1, 1, "Walked the dog after lunch.", nil); err != nil {
		t.Fatalf("IndexChunk failed: %v", err)
	}

	results, err := store.SearchText(ctx, "espresso", 3)
	if err != nil {
		t.Fatalf("SearchText failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 lexical result, got %d", len(results))
	}
	got := results[0]
	if got.SourceFile != "MEMORY.md" || got.HeaderPath != "Projects > Coffee" || got.ChunkOrdinal != 7 {
		t.Fatalf("unexpected source metadata: %+v", got)
	}
	if got.LexicalScore <= 0 || got.VectorScore != 0 {
		t.Fatalf("unexpected score components: lexical=%f vector=%f", got.LexicalScore, got.VectorScore)
	}
}

func TestMemoryStoreSearchChunksScopedFiltersBeforeRanking(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	ctx := context.Background()
	for _, source := range []string{
		"memory/users/202/2026-04-30.md",
		"memory/users/101/2026-04-30.md",
		"memory/chats/-100/2026-04-30.md",
	} {
		if err := store.IndexChunk(ctx, source, "root", 1, 1, "same topic", []float32{1, 0}); err != nil {
			t.Fatalf("IndexChunk(%q) failed: %v", source, err)
		}
	}

	policy := NewRecallPolicy(RecallContext{UserID: 101, ChatID: 101, ChatType: "private"})
	results, err := store.SearchChunksScoped(ctx, []float32{1, 0}, 10, policy)
	if err != nil {
		t.Fatalf("SearchChunksScoped failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 scoped result, got %d: %+v", len(results), results)
	}
	if results[0].SourceFile != "memory/users/101/2026-04-30.md" {
		t.Fatalf("unexpected source: %q", results[0].SourceFile)
	}
}

func TestMemoryManagerFallsBackToLexicalWhenEmbeddingUnavailable(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}
	if err := store.IndexChunk(context.Background(), "MEMORY.md", "Fallback", 1, 1, "Lexical fallback finds espresso.", nil); err != nil {
		t.Fatalf("IndexChunk failed: %v", err)
	}

	manager := NewMemoryManager(failingQueryEmbedder{}, store)
	results, err := manager.Search(context.Background(), "espresso", 1)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 || !strings.Contains(results[0].Content, "espresso") {
		t.Fatalf("expected lexical fallback result, got %+v", results)
	}
}

func TestMemoryStoreHybridReranksCombinedSignals(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	ctx := context.Background()
	if err := store.IndexChunk(ctx, "lexical.md", "Root", 1, 1, "phoenix keyword only", []float32{0, 1}); err != nil {
		t.Fatalf("IndexChunk lexical failed: %v", err)
	}
	if err := store.IndexChunk(ctx, "hybrid.md", "Root", 1, 1, "phoenix semantic plan", []float32{1, 0}); err != nil {
		t.Fatalf("IndexChunk hybrid failed: %v", err)
	}
	if err := store.IndexChunk(ctx, "semantic.md", "Root", 1, 1, "semantic plan without the keyword", []float32{1, 0}); err != nil {
		t.Fatalf("IndexChunk semantic failed: %v", err)
	}

	results, err := store.SearchHybrid(ctx, "phoenix", []float32{1, 0}, 3)
	if err != nil {
		t.Fatalf("SearchHybrid failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected hybrid results")
	}
	if results[0].SourceFile != "hybrid.md" {
		t.Fatalf("expected combined lexical+semantic hit first, got %+v", results)
	}
	if results[0].LexicalScore <= 0 || results[0].VectorScore <= 0 || results[0].HybridScore <= 0 {
		t.Fatalf("expected score components on top result, got %+v", results[0])
	}
}

func TestMemoryStoreSemanticOnlyWhenFTSUnavailable(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	ctx := context.Background()
	if err := store.IndexChunk(ctx, "lexical.md", "Root", 1, 1, "espresso exact lexical hit", []float32{0, 1}); err != nil {
		t.Fatalf("IndexChunk lexical failed: %v", err)
	}
	if err := store.IndexChunk(ctx, "semantic.md", "Root", 1, 1, "tea note with semantic vector", []float32{1, 0}); err != nil {
		t.Fatalf("IndexChunk semantic failed: %v", err)
	}

	store.ftsAvailable = false
	results, err := store.SearchChunks(ctx, []float32{1, 0}, 1)
	if err != nil {
		t.Fatalf("SearchChunks failed: %v", err)
	}
	if len(results) != 1 || results[0].SourceFile != "semantic.md" {
		t.Fatalf("expected semantic-only result when FTS is unavailable, got %+v", results)
	}
	if results[0].LexicalScore != 0 || results[0].VectorScore <= 0 {
		t.Fatalf("unexpected score components: %+v", results[0])
	}
}

func TestMemoryStoreSearchSkipsMalformedVectors(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO memory_chunks (source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, "bad.md", "Root", 1, "bad vector", hashChunkContent("bad vector"), []byte{1, 2, 3}); err != nil {
		t.Fatalf("failed to insert malformed vector: %v", err)
	}
	if err := store.IndexChunk(context.Background(), "good.md", "Root", 1, 1, "good vector", []float32{1, 0}); err != nil {
		t.Fatalf("IndexChunk failed: %v", err)
	}

	results, err := store.SearchChunks(context.Background(), []float32{1, 0}, 2)
	if err != nil {
		t.Fatalf("SearchChunks failed: %v", err)
	}
	if len(results) != 1 || results[0].SourceFile != "good.md" {
		t.Fatalf("expected malformed vector to be skipped, got %+v", results)
	}
}

func TestMemoryStoreVectorSearchSkipsUnembeddedCandidateRows(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	if err := store.IndexChunk(context.Background(), "embedded.md", "Root", 1, 1, "older embedded vector", []float32{1, 0}); err != nil {
		t.Fatalf("IndexChunk failed: %v", err)
	}

	for i := 0; i < memoryVectorCandidateMultiplier+5; i++ {
		content := fmt.Sprintf("recent unembedded chunk %02d", i)
		if _, err := db.Exec(`
			INSERT INTO memory_chunks (source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at)
			VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		`, "empty.md", "Root", i+1, content, hashChunkContent(content), []byte{}); err != nil {
			t.Fatalf("failed to insert unembedded chunk: %v", err)
		}
	}

	results, err := store.SearchChunks(context.Background(), []float32{1, 0}, 1)
	if err != nil {
		t.Fatalf("SearchChunks failed: %v", err)
	}
	if len(results) != 1 || results[0].SourceFile != "embedded.md" {
		t.Fatalf("expected embedded row to survive unembedded candidate pool, got %+v", results)
	}
}

func TestMemoryStoreSearchTextFindsExistingRowsAfterMigration(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE memory_chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source_file TEXT NOT NULL,
		header_path TEXT NOT NULL,
		chunk_ordinal INTEGER NOT NULL DEFAULT 0,
		content TEXT NOT NULL,
		content_hash TEXT NOT NULL,
		embedding BLOB NOT NULL,
		indexed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(source_file, header_path, chunk_ordinal)
	);`); err != nil {
		t.Fatalf("failed to create current memory_chunks table: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO memory_chunks (source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, "MEMORY.md", "Migrated", 3, "backfilled lexical migration row", hashChunkContent("backfilled lexical migration row"), []byte{}); err != nil {
		t.Fatalf("failed to seed current memory_chunks row: %v", err)
	}

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	results, err := store.SearchText(context.Background(), "backfilled", 1)
	if err != nil {
		t.Fatalf("SearchText failed: %v", err)
	}
	if len(results) != 1 || results[0].SourceFile != "MEMORY.md" || results[0].HeaderPath != "Migrated" {
		t.Fatalf("expected migrated lexical row, got %+v", results)
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

type failingQueryEmbedder struct{}

func (f failingQueryEmbedder) GetEmbedding(context.Context, string) ([]float32, error) {
	return nil, errors.New("embedding provider unavailable")
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
