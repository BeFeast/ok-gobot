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
	Enabled       bool     `json:"enabled"`
	RootPath      string   `json:"root_path"`
	BackendType   string   `json:"backend_type"`
	WatcherState  string   `json:"watcher_state"`
	ChunkCount    int      `json:"chunk_count"`
	SourceCount   int      `json:"source_count"`
	LastIndexedAt string   `json:"last_indexed_at"`
	LastError     string   `json:"last_error"`
	Stale         bool     `json:"stale"`
	State         string   `json:"state"`
	Action        string   `json:"action,omitempty"`
	ExtraPaths    []string `json:"extra_paths,omitempty"`
	QMDStatus     string   `json:"qmd_status"`
}

// StatusOptions carries runtime/config state that is not persisted in SQLite.
type StatusOptions struct {
	Enabled      bool
	RootPath     string
	BackendType  string
	WatcherState string
	LastError    string
	ExtraPaths   []string
	QMDStatus    string
}

const (
	BackendSQLite = "sqlite"

	WatcherStateDisabled   = "disabled"
	WatcherStateStarting   = "starting"
	WatcherStateActive     = "active"
	WatcherStateNotRunning = "not_running"
	WatcherStateError      = "error"
	WatcherStateUnknown    = "unknown"

	MemoryStateDisabled = "disabled"
	MemoryStateOK       = "ok"
	MemoryStateStale    = "stale"
	MemoryStateError    = "error"
)

// ManagedSources returns canonical markdown memory files for rootPath: MEMORY.md,
// legacy memory/*.md, and scoped memory/<scope>/<id>/*.md files.
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
	scopedMatches, err := scopedMemoryMatches(absRoot)
	if err != nil {
		return nil, err
	}
	matches = append(matches, scopedMatches...)
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
// part of the built-in memory source set. Unlabelled external markdown files are
// intentionally excluded.
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
	if isScopedMemoryRelativePath(rel) {
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

func scopedMemoryMatches(absRoot string) ([]string, error) {
	scopes := []string{"users", "chats", "sessions", "roles", "jobs", "external"}
	var matches []string
	for _, scope := range scopes {
		pattern := filepath.Join(absRoot, "memory", scope, "*", "*.md")
		found, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("glob scoped memory files: %w", err)
		}
		matches = append(matches, found...)
	}
	return matches, nil
}

func isScopedMemoryRelativePath(rel string) bool {
	if !strings.HasPrefix(rel, "memory/") || !strings.EqualFold(filepath.Ext(rel), ".md") {
		return false
	}
	parts := strings.Split(rel, "/")
	if len(parts) != 4 {
		return false
	}
	switch parts[1] {
	case "users", "chats", "sessions", "roles", "jobs", "external":
		return strings.TrimSpace(parts[2]) != "" && strings.TrimSpace(parts[3]) != ""
	default:
		return false
	}
}

// Status returns lightweight diagnostics for the persisted memory index.
func (s *MemoryStore) Status(ctx context.Context, opts StatusOptions) (IndexStatus, error) {
	return CollectStatus(ctx, s, opts)
}

// CollectStatus returns persisted and runtime memory health diagnostics.
func CollectStatus(ctx context.Context, store *MemoryStore, opts StatusOptions) (IndexStatus, error) {
	status := IndexStatus{
		Enabled:      opts.Enabled,
		RootPath:     opts.RootPath,
		BackendType:  strings.TrimSpace(opts.BackendType),
		WatcherState: strings.TrimSpace(opts.WatcherState),
		LastError:    strings.TrimSpace(opts.LastError),
		ExtraPaths:   append([]string(nil), opts.ExtraPaths...),
	}
	if status.BackendType == "" {
		status.BackendType = BackendSQLite
	}
	if status.WatcherState == "" {
		if status.Enabled {
			status.WatcherState = WatcherStateUnknown
		} else {
			status.WatcherState = WatcherStateDisabled
		}
	}
	status.QMDStatus = strings.TrimSpace(opts.QMDStatus)
	if status.QMDStatus == "" {
		status.QMDStatus = "disabled"
	}

	if !status.Enabled {
		status.State = MemoryStateDisabled
		status.Action = "Set memory.enabled: true, configure embeddings, then run ok-gobot memory index."
		return status, nil
	}

	if store == nil || store.db == nil {
		if status.LastError == "" {
			status.LastError = "memory store is not configured"
		}
		status.State = MemoryStateError
		status.Action = "Check storage_path and restart ok-gobot."
		return status, fmt.Errorf("memory store is not configured")
	}

	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_chunks`).Scan(&status.ChunkCount); err != nil {
		status.LastError = fmt.Sprintf("count memory chunks: %v", err)
		status.State = MemoryStateError
		status.Action = "Check the memory database and run ok-gobot memory index --force."
		return status, fmt.Errorf("count memory chunks: %w", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT source_file) FROM memory_chunks`).Scan(&status.SourceCount); err != nil {
		status.LastError = fmt.Sprintf("count memory sources: %v", err)
		status.State = MemoryStateError
		status.Action = "Check the memory database and run ok-gobot memory index --force."
		return status, fmt.Errorf("count memory sources: %w", err)
	}

	var last sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT MAX(indexed_at) FROM memory_chunks`).Scan(&last); err != nil {
		status.LastError = fmt.Sprintf("read last indexed time: %v", err)
		status.State = MemoryStateError
		status.Action = "Check the memory database and run ok-gobot memory index --force."
		return status, fmt.Errorf("read last indexed time: %w", err)
	}
	if last.Valid {
		status.LastIndexedAt = last.String
	}
	finalizeStatus(&status)
	return status, nil
}

func finalizeStatus(status *IndexStatus) {
	if status == nil {
		return
	}
	if !status.Enabled {
		status.State = MemoryStateDisabled
		if status.Action == "" {
			status.Action = "Set memory.enabled: true, configure embeddings, then run ok-gobot memory index."
		}
		return
	}
	if status.LastError != "" || status.WatcherState == WatcherStateError {
		status.State = MemoryStateError
		if status.Action == "" {
			status.Action = "Fix the error, then run ok-gobot memory index --force."
		}
		return
	}
	if status.LastIndexedAt == "" || status.ChunkCount == 0 {
		status.State = MemoryStateStale
		status.Stale = true
		if status.Action == "" {
			status.Action = "Run ok-gobot memory index to build the memory index."
		}
		return
	}
	status.State = MemoryStateOK
}
