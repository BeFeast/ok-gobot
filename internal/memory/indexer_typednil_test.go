package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestIndexFileWithTypedNilEmbedderDoesNotPanic reproduces the production
// startup crash: a nil *EmbeddingClient boxed into the EmbeddingBatchClient
// interface passes the `embedder == nil` guard, and the first changed chunk
// used to panic with a nil-pointer dereference inside GetEmbeddings.
func TestIndexFileWithTypedNilEmbedderDoesNotPanic(t *testing.T) {
	db := openIndexerTestDB(t)
	defer db.Close() //nolint:errcheck

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "MEMORY.md")
	if err := os.WriteFile(filePath, []byte("# Memory\n\nChanged chunk content."), 0o644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	var typedNil *EmbeddingClient
	indexer := NewIndexer(tmpDir, store, typedNil, WithIndexerChunking(32, 0))

	// Must not panic. With the nil-receiver guard the embed batch fails with
	// a clear error instead of a SIGSEGV; either a clean lexical index or an
	// explicit error is acceptable, a crash is not.
	err = indexer.IndexFile(context.Background(), filePath, "MEMORY.md")
	if err == nil {
		return
	}
	if want := "embedding client not configured"; !containsString(err.Error(), want) {
		t.Fatalf("expected typed-nil embedder to fail with %q, got: %v", want, err)
	}
}

func containsString(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 ||
		indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
