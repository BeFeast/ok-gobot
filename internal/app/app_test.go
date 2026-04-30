package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"ok-gobot/internal/config"
	"ok-gobot/internal/memory"
)

func TestAppMemoryQMDStatusUsesDiagnosticsForMissingIndex(t *testing.T) {
	t.Parallel()
	cfg := newAppQMDTestConfig(t)
	cfg.Memory.QMD.IndexPath = filepath.Join(t.TempDir(), "missing.sqlite")

	got := appMemoryQMDStatus(t.Context(), cfg)
	for _, want := range []string{"unavailable", "index missing", "fallback=builtin"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in status %q", want, got)
		}
	}
	if strings.Contains(got, "used") {
		t.Fatalf("expected missing index to be unavailable, got %q", got)
	}
}

func TestAppMemoryQMDStatusIncludesRuntimeFallbackReason(t *testing.T) {
	t.Parallel()
	cfg := newAppQMDTestConfig(t)
	cfg.Memory.QMD.IndexPath = filepath.Join(t.TempDir(), "qmd.sqlite")
	seedAppQMDTestIndex(t, cfg.Memory.QMD.IndexPath)

	qmdBackend := memory.NewQMDBackend(appQMDConfig(cfg.Memory.QMD))
	fallbackBackend := memory.NewFallbackBackend(
		appTestBackend{name: "qmd", err: fmt.Errorf("server unavailable")},
		appTestBackend{name: "builtin", results: []memory.MemoryResult{{Source: "MEMORY.md", Content: "fallback"}}},
		time.Minute,
	)
	if _, err := fallbackBackend.Search(context.Background(), "query", 1, false); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}

	got := appMemoryQMDRuntimeStatus(t.Context(), cfg, qmdBackend, fallbackBackend)
	for _, want := range []string{"unavailable", "server unavailable", "fallback=builtin"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in status %q", want, got)
		}
	}
}

func newAppQMDTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{Memory: config.MemoryConfig{
		Enabled: true,
		Backend: "qmd",
		QMD: config.MemoryQMDConfig{
			BinaryPath: writeAppQMDTestBinary(t),
			Index:      "work",
			SearchMode: "search",
			Timeout:    "1s",
		},
	}}
}

func writeAppQMDTestBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qmd")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' 'qmd 2.1.0'
  exit 0
fi
printf '%s\n' '[]'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake qmd: %v", err)
	}
	return path
}

func seedAppQMDTestIndex(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close() //nolint:errcheck

	statements := []string{
		`CREATE TABLE store_collections (name TEXT PRIMARY KEY, path TEXT NOT NULL, pattern TEXT NOT NULL DEFAULT '**/*.md')`,
		`CREATE TABLE documents (id INTEGER PRIMARY KEY AUTOINCREMENT, collection TEXT NOT NULL, path TEXT NOT NULL, title TEXT NOT NULL, hash TEXT NOT NULL, modified_at TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE vectors_vec (id TEXT PRIMARY KEY)`,
		`INSERT INTO store_collections (name, path, pattern) VALUES ('workspace', '/tmp/workspace', '**/*.md')`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

type appTestBackend struct {
	name    string
	results []memory.MemoryResult
	err     error
}

func (b appTestBackend) Name() string { return b.name }

func (b appTestBackend) Search(context.Context, string, int, bool) ([]memory.MemoryResult, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.results, nil
}
