package memory

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestNewSearcherLoadsChunksIntoRAMAtStartup(t *testing.T) {
	db := newMemoryChunksTestDB(t)

	insertChunk(t, db, "MEMORY.md", "People/Alice", "Alice likes espresso", []float32{1, 0, 0})
	insertChunk(t, db, "memory/2026-03-04.md", "Decisions/CLI", "Use pure-Go cosine search", []float32{0.95, 0.1, 0})
	insertChunk(t, db, "memory/2026-03-03.md", "Tasks/Done", "Old unrelated note", []float32{-1, 0, 0})

	searcher, err := NewSearcher(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	if got := searcher.ChunkCount(); got != 3 {
		t.Fatalf("ChunkCount() = %d, want 3", got)
	}

	// Delete rows to prove search uses in-memory data loaded at startup.
	if _, err := db.Exec(`DELETE FROM memory_chunks`); err != nil {
		t.Fatalf("failed deleting chunks from db: %v", err)
	}

	results := searcher.Search([]float32{1, 0, 0}, SearchOptions{})
	if len(results) == 0 {
		t.Fatalf("expected in-memory matches after DB delete, got none")
	}
}

func TestSearcherDefaultsThresholdAndTopK(t *testing.T) {
	db := newMemoryChunksTestDB(t)

	insertChunk(t, db, "memory/1.md", "A", "1", []float32{1.0, 0.0})
	insertChunk(t, db, "memory/2.md", "A", "2", []float32{0.95, 0.05})
	insertChunk(t, db, "memory/3.md", "A", "3", []float32{0.9, 0.1})
	insertChunk(t, db, "memory/4.md", "A", "4", []float32{0.8, 0.2})
	insertChunk(t, db, "memory/5.md", "A", "5", []float32{0.7, 0.3})
	insertChunk(t, db, "memory/6.md", "A", "6", []float32{0.6, 0.4})
	insertChunk(t, db, "memory/7.md", "A", "7", []float32{0.5, 0.5}) // below 0.75 threshold

	searcher, err := NewSearcher(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	results := searcher.Search([]float32{1, 0}, SearchOptions{})
	if len(results) != DefaultSearchTopK {
		t.Fatalf("len(results) = %d, want %d", len(results), DefaultSearchTopK)
	}

	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Fatalf("results are not sorted by descending score")
		}
	}

	for _, result := range results {
		if result.Score < DefaultSearchThreshold {
			t.Fatalf("got score %.4f below default threshold %.2f", result.Score, DefaultSearchThreshold)
		}
	}
}

func TestSearcherSupportsCustomThresholdAndTopK(t *testing.T) {
	db := newMemoryChunksTestDB(t)

	insertChunk(t, db, "memory/1.md", "A", "1", []float32{1.0, 0.0})
	insertChunk(t, db, "memory/2.md", "A", "2", []float32{0.95, 0.05})
	insertChunk(t, db, "memory/3.md", "A", "3", []float32{0.9, 0.1})
	insertChunk(t, db, "memory/4.md", "A", "4", []float32{0.8, 0.2})
	insertChunk(t, db, "memory/5.md", "A", "5", []float32{0.1, 0.9})

	searcher, err := NewSearcher(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	results := searcher.Search([]float32{1, 0}, SearchOptions{
		Threshold: 0.95,
		TopK:      3,
	})

	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	for _, result := range results {
		if result.Score < 0.95 {
			t.Fatalf("got score %.4f below custom threshold", result.Score)
		}
	}
}

func TestSearcherReturnsEmptyOnDimensionMismatch(t *testing.T) {
	db := newMemoryChunksTestDB(t)
	insertChunk(t, db, "memory/1.md", "A", "1", []float32{1.0, 0.0})

	searcher, err := NewSearcher(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	results := searcher.Search([]float32{1.0, 0.0, 0.0}, SearchOptions{})
	if len(results) != 0 {
		t.Fatalf("expected no results for dimension mismatch, got %d", len(results))
	}
}

func TestNewSearcherSkipsMalformedEmbeddings(t *testing.T) {
	db := newMemoryChunksTestDB(t)

	insertChunk(t, db, "memory/good.md", "A", "good", []float32{1.0, 0.0})
	if _, err := db.Exec(
		`INSERT INTO memory_chunks (file, header_path, text, embedding) VALUES (?, ?, ?, ?)`,
		"memory/bad.md",
		"A",
		"bad",
		[]byte{1, 2, 3}, // invalid binary embedding
	); err != nil {
		t.Fatalf("failed inserting malformed row: %v", err)
	}

	searcher, err := NewSearcher(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	if got := searcher.ChunkCount(); got != 1 {
		t.Fatalf("ChunkCount() = %d, want 1", got)
	}
}

func TestNewSearcherReturnsSchemaErrorWhenTableMissing(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = NewSearcher(context.Background(), db)
	if err == nil {
		t.Fatalf("expected error when memory_chunks table is missing")
	}
	if !strings.Contains(err.Error(), "memory_chunks") {
		t.Fatalf("expected memory_chunks error, got %v", err)
	}
}

func BenchmarkSearcher_Search10K(b *testing.B) {
	const (
		chunkCount = 10000
		dimension  = 1536
	)

	rng := rand.New(rand.NewSource(42))
	chunks := make([]indexedChunk, chunkCount)
	for i := 0; i < chunkCount; i++ {
		chunks[i] = indexedChunk{
			File:       fmt.Sprintf("memory/%05d.md", i),
			HeaderPath: fmt.Sprintf("H%d", i%32),
			Text:       "synthetic chunk",
			Embedding:  randomNormalizedVector(rng, dimension),
		}
	}

	searcher := &Searcher{
		chunks:    chunks,
		dimension: dimension,
	}
	query := randomNormalizedVector(rng, dimension)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = searcher.Search(query, SearchOptions{})
	}
}

func newMemoryChunksTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
		CREATE TABLE memory_chunks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			file TEXT NOT NULL,
			header_path TEXT DEFAULT '',
			text TEXT NOT NULL,
			embedding BLOB NOT NULL
		);
	`)
	if err != nil {
		t.Fatalf("failed creating memory_chunks table: %v", err)
	}

	return db
}

func insertChunk(t *testing.T, db *sql.DB, file, headerPath, text string, embedding []float32) {
	t.Helper()

	encoded, err := encodeEmbedding(embedding)
	if err != nil {
		t.Fatalf("failed encoding embedding: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO memory_chunks (file, header_path, text, embedding) VALUES (?, ?, ?, ?)`,
		file,
		headerPath,
		text,
		encoded,
	)
	if err != nil {
		t.Fatalf("failed inserting chunk: %v", err)
	}
}

func TestSearchExpandBranchCombinesSameBranch(t *testing.T) {
	db := newMemoryChunksTestDBWithOrdinal(t)

	// Three chunks from the same branch (file + header_path), only one matches query.
	insertChunkWithOrdinal(t, db, "memory/2026-03-10.md", "Meeting Notes", 0, "Alice presented the architecture proposal", []float32{1, 0, 0})
	insertChunkWithOrdinal(t, db, "memory/2026-03-10.md", "Meeting Notes", 1, "Bob raised concerns about latency", []float32{0.5, 0.5, 0})
	insertChunkWithOrdinal(t, db, "memory/2026-03-10.md", "Meeting Notes", 2, "Decision: use edge caching", []float32{0.3, 0.3, 0.3})

	// Unrelated chunk in different branch.
	insertChunkWithOrdinal(t, db, "memory/2026-03-09.md", "Tasks", 0, "Deploy staging env", []float32{-1, 0, 0})

	searcher, err := NewSearcher(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	// Search with expand: query matches chunk 0 strongly.
	results := searcher.Search([]float32{1, 0, 0}, SearchOptions{
		Threshold:    0.5,
		TopK:         5,
		ExpandBranch: true,
	})

	if len(results) != 1 {
		t.Fatalf("expected 1 expanded branch, got %d", len(results))
	}

	branch := results[0]
	if branch.File != "memory/2026-03-10.md" {
		t.Fatalf("expected file memory/2026-03-10.md, got %s", branch.File)
	}
	if branch.HeaderPath != "Meeting Notes" {
		t.Fatalf("expected header_path 'Meeting Notes', got %s", branch.HeaderPath)
	}

	// All three chunks should be combined.
	if !strings.Contains(branch.Text, "Alice presented") {
		t.Fatalf("expanded branch missing chunk 0 text")
	}
	if !strings.Contains(branch.Text, "Bob raised") {
		t.Fatalf("expanded branch missing chunk 1 text")
	}
	if !strings.Contains(branch.Text, "Decision: use edge caching") {
		t.Fatalf("expanded branch missing chunk 2 text")
	}
}

func TestSearchExpandBranchMultipleBranches(t *testing.T) {
	db := newMemoryChunksTestDBWithOrdinal(t)

	// Branch A: two chunks
	insertChunkWithOrdinal(t, db, "memory/notes.md", "Project Alpha", 0, "Alpha kickoff meeting", []float32{0.9, 0.1, 0})
	insertChunkWithOrdinal(t, db, "memory/notes.md", "Project Alpha", 1, "Alpha timeline set to Q2", []float32{0.85, 0.15, 0})

	// Branch B: two chunks
	insertChunkWithOrdinal(t, db, "memory/notes.md", "Project Beta", 0, "Beta requirements gathered", []float32{0.8, 0.2, 0})
	insertChunkWithOrdinal(t, db, "memory/notes.md", "Project Beta", 1, "Beta design review", []float32{0.75, 0.25, 0})

	// Unrelated
	insertChunkWithOrdinal(t, db, "memory/old.md", "Archive", 0, "Deprecated notes", []float32{-1, 0, 0})

	searcher, err := NewSearcher(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	results := searcher.Search([]float32{1, 0, 0}, SearchOptions{
		Threshold:    0.7,
		TopK:         5,
		ExpandBranch: true,
	})

	if len(results) != 2 {
		t.Fatalf("expected 2 expanded branches, got %d", len(results))
	}

	// Results should be sorted by score descending.
	if results[0].Score < results[1].Score {
		t.Fatalf("branches not sorted by score descending")
	}

	// First branch should be Project Alpha (higher scoring match).
	if !strings.Contains(results[0].Text, "Alpha kickoff") || !strings.Contains(results[0].Text, "Alpha timeline") {
		t.Fatalf("first branch should contain all Project Alpha chunks")
	}
}

func TestSearchExpandBranchPreservesOrdinalOrder(t *testing.T) {
	db := newMemoryChunksTestDBWithOrdinal(t)

	// Insert chunks out of ordinal order.
	insertChunkWithOrdinal(t, db, "memory/doc.md", "Steps", 2, "Step three", []float32{0.9, 0.1, 0})
	insertChunkWithOrdinal(t, db, "memory/doc.md", "Steps", 0, "Step one", []float32{0.95, 0.05, 0})
	insertChunkWithOrdinal(t, db, "memory/doc.md", "Steps", 1, "Step two", []float32{0.8, 0.2, 0})

	searcher, err := NewSearcher(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	results := searcher.Search([]float32{1, 0, 0}, SearchOptions{
		Threshold:    0.7,
		TopK:         5,
		ExpandBranch: true,
	})

	if len(results) != 1 {
		t.Fatalf("expected 1 branch, got %d", len(results))
	}

	// Text should be in ordinal order: one, two, three.
	text := results[0].Text
	idxOne := strings.Index(text, "Step one")
	idxTwo := strings.Index(text, "Step two")
	idxThree := strings.Index(text, "Step three")

	if idxOne < 0 || idxTwo < 0 || idxThree < 0 {
		t.Fatalf("expanded text missing steps, got: %s", text)
	}
	if idxOne >= idxTwo || idxTwo >= idxThree {
		t.Fatalf("chunks not in ordinal order: one@%d, two@%d, three@%d", idxOne, idxTwo, idxThree)
	}
}

func TestSearchExpandBranchFalseReturnsIndividualChunks(t *testing.T) {
	db := newMemoryChunksTestDBWithOrdinal(t)

	insertChunkWithOrdinal(t, db, "memory/f.md", "H", 0, "chunk A", []float32{1, 0, 0})
	insertChunkWithOrdinal(t, db, "memory/f.md", "H", 1, "chunk B", []float32{0.9, 0.1, 0})

	searcher, err := NewSearcher(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSearcher() error = %v", err)
	}

	// Without expand, both matching chunks should be separate results.
	results := searcher.Search([]float32{1, 0, 0}, SearchOptions{
		Threshold:    0.5,
		TopK:         5,
		ExpandBranch: false,
	})

	if len(results) != 2 {
		t.Fatalf("expected 2 individual chunks, got %d", len(results))
	}
}

func newMemoryChunksTestDBWithOrdinal(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
		CREATE TABLE memory_chunks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			file TEXT NOT NULL,
			header_path TEXT DEFAULT '',
			chunk_ordinal INTEGER DEFAULT 0,
			text TEXT NOT NULL,
			embedding BLOB NOT NULL
		);
	`)
	if err != nil {
		t.Fatalf("failed creating memory_chunks table: %v", err)
	}

	return db
}

func insertChunkWithOrdinal(t *testing.T, db *sql.DB, file, headerPath string, ordinal int, text string, embedding []float32) {
	t.Helper()

	encoded, err := encodeEmbedding(embedding)
	if err != nil {
		t.Fatalf("failed encoding embedding: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO memory_chunks (file, header_path, chunk_ordinal, text, embedding) VALUES (?, ?, ?, ?, ?)`,
		file,
		headerPath,
		ordinal,
		text,
		encoded,
	)
	if err != nil {
		t.Fatalf("failed inserting chunk: %v", err)
	}
}

func randomNormalizedVector(rng *rand.Rand, dimensions int) []float32 {
	vector := make([]float32, dimensions)
	for i := 0; i < dimensions; i++ {
		vector[i] = (rng.Float32() * 2) - 1
	}

	normalized, ok := normalizeEmbedding(vector)
	if !ok {
		panic("failed to normalize synthetic vector")
	}

	return normalized
}
