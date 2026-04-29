package memory

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestManagedSourcesFindsOnlyCanonicalMemoryFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "MEMORY.md"), "# Memory\n")
	mustWrite(t, filepath.Join(root, "memory", "2026-04-28.md"), "# Yesterday\n")
	mustWrite(t, filepath.Join(root, "memory", "2026-04-29.md"), "# Today\n")
	mustWrite(t, filepath.Join(root, "notes.md"), "# Not indexed yet\n")
	mustWrite(t, filepath.Join(root, "memory", "nested", "ignore.md"), "# Nested\n")

	sources, err := ManagedSources(root)
	if err != nil {
		t.Fatalf("ManagedSources failed: %v", err)
	}

	got := make([]string, len(sources))
	for i, source := range sources {
		got[i] = source.RelativePath
	}
	want := []string{"MEMORY.md", "memory/2026-04-28.md", "memory/2026-04-29.md"}
	if len(got) != len(want) {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sources = %v, want %v", got, want)
		}
	}
}

func TestManagedRelativePath(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name string
		path string
		want string
		ok   bool
	}{
		{name: "root memory", path: filepath.Join(root, "MEMORY.md"), want: "MEMORY.md", ok: true},
		{name: "daily memory", path: filepath.Join(root, "memory", "2026-04-29.md"), want: "memory/2026-04-29.md", ok: true},
		{name: "other markdown", path: filepath.Join(root, "notes.md"), ok: false},
		{name: "nested memory", path: filepath.Join(root, "memory", "nested", "x.md"), ok: false},
		{name: "outside", path: filepath.Join(filepath.Dir(root), "MEMORY.md"), ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ManagedRelativePath(root, tt.path)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ManagedRelativePath() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestIndexManagedSourcesAndClear(t *testing.T) {
	db := openIndexerTestDB(t)
	defer db.Close() //nolint:errcheck

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "MEMORY.md"), "# Memory\n\nDurable fact.")
	mustWrite(t, filepath.Join(root, "memory", "2026-04-29.md"), "# Daily\n\nToday we fixed memory.")
	mustWrite(t, filepath.Join(root, "notes.md"), "# Not managed\n\nIgnore me.")

	indexer := NewIndexer(root, store, &stubBatchEmbedder{}, WithIndexerChunking(64, 0))
	stats, err := IndexManagedSources(context.Background(), root, indexer)
	if err != nil {
		t.Fatalf("IndexManagedSources failed: %v", err)
	}
	if stats.FilesIndexed != 2 {
		t.Fatalf("FilesIndexed = %d, want 2", stats.FilesIndexed)
	}
	if count := countAllChunks(t, db); count != 2 {
		t.Fatalf("chunk count = %d, want 2", count)
	}
	if count := countSourceChunks(t, db, "notes.md"); count != 0 {
		t.Fatalf("notes.md should not be indexed, got %d chunk(s)", count)
	}

	status, err := store.Status(context.Background(), true, root)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !status.Enabled || status.SourceCount != 2 || status.ChunkCount != 2 || status.LastIndexedAt == "" {
		t.Fatalf("unexpected status: %+v", status)
	}

	if err := store.ClearManagedSources(context.Background()); err != nil {
		t.Fatalf("ClearManagedSources failed: %v", err)
	}
	if count := countAllChunks(t, db); count != 0 {
		t.Fatalf("chunk count after clear = %d, want 0", count)
	}
}

func TestIndexerDeletesManagedChunksWhenSourceIsRemoved(t *testing.T) {
	db := openIndexerTestDB(t)
	defer db.Close() //nolint:errcheck

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	root := t.TempDir()
	sourcePath := filepath.Join(root, "MEMORY.md")
	mustWrite(t, sourcePath, "# Memory\n\nTemporary fact.")

	indexer := NewIndexer(root, store, &stubBatchEmbedder{}, WithIndexerChunking(64, 0))
	if err := indexer.IndexFile(context.Background(), sourcePath, "MEMORY.md"); err != nil {
		t.Fatalf("IndexFile failed: %v", err)
	}
	if count := countSourceChunks(t, db, "MEMORY.md"); count != 1 {
		t.Fatalf("chunk count = %d, want 1", count)
	}

	if err := os.Remove(sourcePath); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if err := indexer.IndexFile(context.Background(), sourcePath, "MEMORY.md"); err != nil {
		t.Fatalf("IndexFile delete failed: %v", err)
	}
	if count := countSourceChunks(t, db, "MEMORY.md"); count != 0 {
		t.Fatalf("chunk count after delete = %d, want 0", count)
	}
}

func mustWrite(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func countAllChunks(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_chunks`).Scan(&count); err != nil {
		t.Fatalf("count all chunks failed: %v", err)
	}
	return count
}
