package memory

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQMDBackendMissingBinary(t *testing.T) {
	t.Parallel()

	backend := NewQMDBackend(QMDConfig{
		BinaryPath: "definitely-missing-qmd-binary",
		Timeout:    time.Second,
	})

	_, err := backend.Search(context.Background(), "query", 5, false)
	if err == nil || !strings.Contains(err.Error(), "qmd binary not found") {
		t.Fatalf("expected missing binary error, got %v", err)
	}

	d := backend.Diagnostics(context.Background())
	if d.BinaryFound {
		t.Fatalf("expected BinaryFound=false")
	}
	if !strings.Contains(d.LastError, "qmd binary not found") {
		t.Fatalf("expected diagnostics last error, got %q", d.LastError)
	}
}

func TestQMDBackendMalformedOutput(t *testing.T) {
	t.Parallel()

	bin := writeQMDTestBinary(t, `#!/bin/sh
printf 'not json\n'
`)
	backend := NewQMDBackend(QMDConfig{BinaryPath: bin, Timeout: time.Second})

	_, err := backend.Search(context.Background(), "query", 5, false)
	if err == nil || !strings.Contains(err.Error(), "parse qmd json output") {
		t.Fatalf("expected malformed output error, got %v", err)
	}
}

func TestQMDBackendSourceFormatting(t *testing.T) {
	t.Parallel()

	bin := writeQMDTestBinary(t, `#!/bin/sh
printf '%s\n' '[{"docid":"#abc123","score":0.91,"file":"qmd://workspace/MEMORY.md","title":"Memory","snippet":"@@ -7,2 @@\nworkspace hit","line":7},{"docid":"#def456","score":"50%","file":"qmd://daily/2026-04-29.md","title":"Daily","snippet":"daily hit"},{"docid":"#ghi789","score":0.4,"file":"qmd://sessions/dm-1.md","title":"Transcript","body":"session hit"}]'
`)
	backend := NewQMDBackend(QMDConfig{
		BinaryPath: bin,
		Timeout:    time.Second,
		Collections: QMDCollections{
			Workspace:          "workspace",
			DailyNotes:         "daily",
			SessionTranscripts: "sessions",
		},
	})

	results, err := backend.Search(context.Background(), "query", 3, false)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len(results)=%d, want 3", len(results))
	}

	if results[0].Source != "MEMORY.md" || results[0].SourceFile != "MEMORY.md" {
		t.Fatalf("workspace source=%q source_file=%q", results[0].Source, results[0].SourceFile)
	}
	if results[0].StartLine != 7 || results[0].EndLine != 8 {
		t.Fatalf("workspace lines=%d-%d, want 7-8", results[0].StartLine, results[0].EndLine)
	}
	if results[1].Source != "memory/2026-04-29.md" {
		t.Fatalf("daily source=%q", results[1].Source)
	}
	if results[1].Similarity != 0.5 {
		t.Fatalf("daily similarity=%v, want 0.5", results[1].Similarity)
	}
	if results[2].Source != "sessions/dm-1.md" {
		t.Fatalf("session source=%q", results[2].Source)
	}
}

func TestQMDBackendParsesWrappedResultsWithNoise(t *testing.T) {
	t.Parallel()

	bin := writeQMDTestBinary(t, `#!/bin/sh
printf '%s\n' '[node-llama-cpp] warning before json'
printf '%s\n' '{"results":[{"docid":"#abc123","score":0.7,"file":"qmd://workspace/MEMORY.md","snippet":"hit"}]}'
printf '%s\n' 'trailing notice'
`)
	backend := NewQMDBackend(QMDConfig{
		BinaryPath:  bin,
		Timeout:     time.Second,
		Collections: QMDCollections{Workspace: "workspace"},
	})

	results, err := backend.Search(context.Background(), "query", 1, false)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 || results[0].Source != "MEMORY.md" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestQMDBackendPassesConfiguredCollections(t *testing.T) {
	t.Parallel()

	argsPath := filepath.Join(t.TempDir(), "args.txt")
	bin := writeQMDTestBinary(t, `#!/bin/sh
printf '%s\n' "$@" > "`+argsPath+`"
printf '%s\n' '[]'
`)
	backend := NewQMDBackend(QMDConfig{
		BinaryPath: bin,
		Index:      "work",
		SearchMode: "query",
		Timeout:    time.Second,
		Collections: QMDCollections{
			Workspace:          "workspace",
			DailyNotes:         "daily",
			SessionTranscripts: "sessions",
			ExtraPaths:         []string{"docs", "notes"},
		},
	})

	if _, err := backend.Search(context.Background(), "release plan", 7, true); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	argsBytes, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(argsBytes)), "\n")
	want := []string{"--index", "work", "query", "release plan", "--json", "-n", "7", "--full", "-c", "workspace", "-c", "daily", "-c", "sessions", "-c", "docs", "-c", "notes"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args=%q, want %q", args, want)
	}
}

func TestQMDBackendDiagnosticsReadsIndexMetadata(t *testing.T) {
	t.Parallel()

	bin := writeQMDTestBinary(t, `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' 'qmd 2.1.0'
  exit 0
fi
printf '%s\n' '[]'
`)
	dbPath := filepath.Join(t.TempDir(), "qmd.sqlite")
	seedQMDTestIndex(t, dbPath)

	backend := NewQMDBackend(QMDConfig{
		BinaryPath: bin,
		IndexPath:  dbPath,
		Timeout:    time.Second,
		Collections: QMDCollections{
			Workspace:  "workspace",
			DailyNotes: "daily",
			ExtraPaths: []string{"missing-extra"},
		},
	})

	d := backend.Diagnostics(context.Background())
	if !d.BinaryFound || d.Version != "qmd 2.1.0" {
		t.Fatalf("unexpected binary diagnostics: %+v", d)
	}
	if !d.IndexExists || d.TotalDocuments != 2 || d.NeedsEmbedding != 1 || !d.HasVectorIndex {
		t.Fatalf("unexpected index diagnostics: %+v", d)
	}
	if d.EmbeddingReady {
		t.Fatalf("expected embedding readiness false while one document is pending")
	}
	if d.UpdateState != "pending embeddings" {
		t.Fatalf("UpdateState=%q, want pending embeddings", d.UpdateState)
	}
	if !hasQMDCollectionStatus(d.Collections, "workspace", true, "workspace") {
		t.Fatalf("missing workspace collection status: %+v", d.Collections)
	}
	if !hasQMDCollectionStatus(d.Collections, "missing-extra", false, "extra_paths") {
		t.Fatalf("missing absent extra collection status: %+v", d.Collections)
	}
}

func writeQMDTestBinary(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qmd")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatalf("write fake qmd: %v", err)
	}
	if _, err := f.WriteString(script); err != nil {
		_ = f.Close()
		t.Fatalf("write fake qmd: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close fake qmd: %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod fake qmd: %v", err)
	}
	return path
}

func seedQMDTestIndex(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close() //nolint:errcheck

	statements := []string{
		`CREATE TABLE store_collections (name TEXT PRIMARY KEY, path TEXT NOT NULL, pattern TEXT NOT NULL DEFAULT '**/*.md')`,
		`CREATE TABLE documents (id INTEGER PRIMARY KEY AUTOINCREMENT, collection TEXT NOT NULL, path TEXT NOT NULL, title TEXT NOT NULL, hash TEXT NOT NULL, modified_at TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE content_vectors (hash TEXT NOT NULL, seq INTEGER NOT NULL DEFAULT 0, pos INTEGER NOT NULL DEFAULT 0, model TEXT NOT NULL, embedded_at TEXT NOT NULL, PRIMARY KEY (hash, seq))`,
		`CREATE TABLE vectors_vec (id TEXT PRIMARY KEY)`,
		`INSERT INTO store_collections (name, path, pattern) VALUES ('workspace', '/tmp/workspace', '**/*.md'), ('daily', '/tmp/workspace/memory', '**/*.md')`,
		`INSERT INTO documents (collection, path, title, hash, modified_at, active) VALUES ('workspace', 'MEMORY.md', 'Memory', 'hash1', '2026-04-29T00:00:00Z', 1), ('daily', '2026-04-29.md', 'Daily', 'hash2', '2026-04-29T00:00:00Z', 1)`,
		`INSERT INTO content_vectors (hash, seq, pos, model, embedded_at) VALUES ('hash1', 0, 0, 'test-model', '2026-04-29T00:00:00Z')`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

func hasQMDCollectionStatus(collections []QMDCollectionStatus, name string, present bool, role string) bool {
	for _, col := range collections {
		if col.Name == name && col.Present == present && col.Role == role {
			return true
		}
	}
	return false
}
