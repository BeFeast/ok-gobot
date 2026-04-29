package memory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ManagedSource describes a markdown memory file managed by the built-in
// workspace memory index.
type ManagedSource struct {
	Path         string
	RelativePath string
}

// IndexRunStats describes one index pass over managed memory sources.
type IndexRunStats struct {
	FilesIndexed int
}

// IndexStatus describes the current state of the persisted memory index.
type IndexStatus struct {
	Enabled       bool
	RootPath      string
	ChunkCount    int
	SourceCount   int
	LastIndexedAt string
}

// ManagedSources returns the canonical markdown memory files for rootPath:
// MEMORY.md plus memory/*.md. Missing files/directories are skipped.
func ManagedSources(rootPath string) ([]ManagedSource, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return nil, fmt.Errorf("memory root path is empty")
	}
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve memory root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	var sources []ManagedSource
	rootMemory := filepath.Join(absRoot, "MEMORY.md")
	if info, err := os.Stat(rootMemory); err == nil && !info.IsDir() {
		sources = append(sources, ManagedSource{Path: rootMemory, RelativePath: "MEMORY.md"})
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat MEMORY.md: %w", err)
	}

	dailyPattern := filepath.Join(absRoot, "memory", "*.md")
	matches, err := filepath.Glob(dailyPattern)
	if err != nil {
		return nil, fmt.Errorf("glob daily memory files: %w", err)
	}
	sort.Strings(matches)
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat daily memory %s: %w", match, err)
		}
		if info.IsDir() {
			continue
		}
		rel, err := filepath.Rel(absRoot, match)
		if err != nil {
			return nil, fmt.Errorf("rel daily memory %s: %w", match, err)
		}
		sources = append(sources, ManagedSource{
			Path:         filepath.Clean(match),
			RelativePath: filepath.ToSlash(rel),
		})
	}

	return sources, nil
}

// ManagedRelativePath returns the canonical relative source path when absPath is
// part of the built-in memory source set. It intentionally excludes unrelated
// markdown files until extra indexed paths are introduced.
func ManagedRelativePath(rootPath, absPath string) (string, bool) {
	rootPath = strings.TrimSpace(rootPath)
	absPath = strings.TrimSpace(absPath)
	if rootPath == "" || absPath == "" {
		return "", false
	}

	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return "", false
	}
	resolved, err := filepath.Abs(absPath)
	if err != nil {
		return "", false
	}

	rel, err := filepath.Rel(filepath.Clean(absRoot), filepath.Clean(resolved))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "MEMORY.md" {
		return rel, true
	}
	if strings.HasPrefix(rel, "memory/") && strings.Count(rel, "/") == 1 && strings.EqualFold(filepath.Ext(rel), ".md") {
		return rel, true
	}
	return "", false
}

// IndexManagedSources indexes all canonical memory markdown files.
func IndexManagedSources(ctx context.Context, rootPath string, indexer *Indexer) (IndexRunStats, error) {
	if indexer == nil {
		return IndexRunStats{}, fmt.Errorf("memory indexer is nil")
	}
	sources, err := ManagedSources(rootPath)
	if err != nil {
		return IndexRunStats{}, err
	}

	stats := IndexRunStats{}
	for _, source := range sources {
		if err := indexer.IndexFile(ctx, source.Path, source.RelativePath); err != nil {
			return stats, err
		}
		stats.FilesIndexed++
	}
	return stats, nil
}

// ClearManagedSources removes built-in workspace memory chunks from the index.
func (s *MemoryStore) ClearManagedSources(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("memory store is not configured")
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM memory_chunks
		WHERE source_file = 'MEMORY.md'
		   OR source_file LIKE 'memory/%'
	`)
	if err != nil {
		return fmt.Errorf("clear managed memory sources: %w", err)
	}
	return nil
}

// Status returns lightweight diagnostics for the persisted memory index.
func (s *MemoryStore) Status(ctx context.Context, enabled bool, rootPath string) (IndexStatus, error) {
	status := IndexStatus{
		Enabled:  enabled,
		RootPath: rootPath,
	}
	if s == nil || s.db == nil {
		return status, fmt.Errorf("memory store is not configured")
	}

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_chunks`).Scan(&status.ChunkCount); err != nil {
		return status, fmt.Errorf("count memory chunks: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT source_file) FROM memory_chunks`).Scan(&status.SourceCount); err != nil {
		return status, fmt.Errorf("count memory sources: %w", err)
	}

	var last sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(indexed_at) FROM memory_chunks`).Scan(&last); err != nil {
		return status, fmt.Errorf("read last indexed time: %w", err)
	}
	if last.Valid {
		status.LastIndexedAt = last.String
	}
	return status, nil
}
