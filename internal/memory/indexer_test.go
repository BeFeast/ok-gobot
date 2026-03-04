package memory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestIndexerSkipsEmbeddingWhenContentHashUnchanged(t *testing.T) {
	db := openIndexerTestDB(t)
	defer db.Close() //nolint:errcheck

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "MEMORY.md")
	if err := os.WriteFile(filePath, []byte("# Memory\n\nThis is stable content."), 0o644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	embedder := &stubBatchEmbedder{}
	indexer := NewIndexer(tmpDir, store, embedder, WithIndexerChunking(32, 0))
	ctx := context.Background()

	if err := indexer.IndexFile(ctx, filePath, "MEMORY.md"); err != nil {
		t.Fatalf("first IndexFile failed: %v", err)
	}
	if calls := embedder.TotalCalls(); calls != 1 {
		t.Fatalf("expected 1 embedding call after first index, got %d", calls)
	}

	if err := indexer.IndexFile(ctx, filePath, "MEMORY.md"); err != nil {
		t.Fatalf("second IndexFile failed: %v", err)
	}
	if calls := embedder.TotalCalls(); calls != 1 {
		t.Fatalf("expected unchanged content to skip embedding, got %d calls", calls)
	}

	if err := os.WriteFile(filePath, []byte("# Memory\n\nThis content has changed now."), 0o644); err != nil {
		t.Fatalf("rewrite file failed: %v", err)
	}
	if err := indexer.IndexFile(ctx, filePath, "MEMORY.md"); err != nil {
		t.Fatalf("third IndexFile failed: %v", err)
	}
	if calls := embedder.TotalCalls(); calls != 2 {
		t.Fatalf("expected changed content to trigger embedding, got %d calls", calls)
	}
}

func TestIndexerEmbedsInBatchesOfUpTo32(t *testing.T) {
	db := openIndexerTestDB(t)
	defer db.Close() //nolint:errcheck

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "memory.txt")

	words := make([]string, 0, 120)
	for i := 0; i < 120; i++ {
		words = append(words, fmt.Sprintf("w%d", i))
	}
	if err := os.WriteFile(filePath, []byte(strings.Join(words, " ")), 0o644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	embedder := &stubBatchEmbedder{}
	indexer := NewIndexer(tmpDir, store, embedder, WithIndexerChunking(3, 0))

	if err := indexer.IndexFile(context.Background(), filePath, "memory.txt"); err != nil {
		t.Fatalf("IndexFile failed: %v", err)
	}

	batchSizes := embedder.BatchSizes()
	if len(batchSizes) != 2 {
		t.Fatalf("expected 2 embedding calls for 40 chunks, got %d (%v)", len(batchSizes), batchSizes)
	}
	if batchSizes[0] != 32 || batchSizes[1] != 8 {
		t.Fatalf("unexpected batch sizes: got %v, want [32 8]", batchSizes)
	}
	for _, size := range batchSizes {
		if size > 32 {
			t.Fatalf("batch size exceeded 32: %d", size)
		}
	}
}

func TestIndexerDeletesStaleChunksWhenFileShrinks(t *testing.T) {
	db := openIndexerTestDB(t)
	defer db.Close() //nolint:errcheck

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "notes.txt")

	embedder := &stubBatchEmbedder{}
	indexer := NewIndexer(tmpDir, store, embedder, WithIndexerChunking(4, 0))
	ctx := context.Background()

	if err := os.WriteFile(filePath, []byte("a b c d e f g h i j k l"), 0o644); err != nil {
		t.Fatalf("write initial file failed: %v", err)
	}
	if err := indexer.IndexFile(ctx, filePath, "notes.txt"); err != nil {
		t.Fatalf("initial IndexFile failed: %v", err)
	}

	if count := countSourceChunks(t, db, "notes.txt"); count != 3 {
		t.Fatalf("expected 3 chunks after first index, got %d", count)
	}

	if err := os.WriteFile(filePath, []byte("a b c d"), 0o644); err != nil {
		t.Fatalf("write smaller file failed: %v", err)
	}
	if err := indexer.IndexFile(ctx, filePath, "notes.txt"); err != nil {
		t.Fatalf("second IndexFile failed: %v", err)
	}

	if count := countSourceChunks(t, db, "notes.txt"); count != 1 {
		t.Fatalf("expected stale chunks to be deleted, got %d chunks", count)
	}
}

func openIndexerTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite db failed: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func countSourceChunks(t *testing.T, db *sql.DB, sourceFile string) int {
	t.Helper()

	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM memory_chunks WHERE source_file = ?`,
		sourceFile,
	).Scan(&count); err != nil {
		t.Fatalf("count source chunks failed: %v", err)
	}
	return count
}

type stubBatchEmbedder struct {
	mu         sync.Mutex
	callCount  int
	batchSizes []int
}

func (s *stubBatchEmbedder) GetEmbeddings(_ context.Context, texts []string) ([][]float32, error) {
	s.mu.Lock()
	s.callCount++
	s.batchSizes = append(s.batchSizes, len(texts))
	s.mu.Unlock()

	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(len(texts[i])), float32(i + 1)}
	}
	return out, nil
}

func (s *stubBatchEmbedder) TotalCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callCount
}

func (s *stubBatchEmbedder) BatchSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int, len(s.batchSizes))
	copy(out, s.batchSizes)
	return out
}
