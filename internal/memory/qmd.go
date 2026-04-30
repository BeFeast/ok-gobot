package memory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	DefaultQMDBinaryPath = "qmd"
	DefaultQMDIndex      = "index"
	DefaultQMDSearchMode = "search"
	DefaultQMDTimeout    = 10 * time.Second
)

var qmdHunkLineRegexp = regexp.MustCompile(`@@\s+-(\d+)(?:,(\d+))?`)

// QMDConfig configures the optional read-only QMD CLI backend.
type QMDConfig struct {
	BinaryPath  string
	Index       string
	IndexPath   string
	SearchMode  string
	Timeout     time.Duration
	Collections QMDCollections
}

// QMDCollections maps ok-gobot memory roles to pre-existing QMD collection names.
// ok-gobot does not add/update/remove QMD collections in v1.
type QMDCollections struct {
	Workspace          string
	DailyNotes         string
	SessionTranscripts string
	ExtraPaths         []string
}

// QMDBackend searches a pre-existing QMD index through the qmd CLI.
type QMDBackend struct {
	cfg QMDConfig

	mu      sync.Mutex
	lastErr error
}

// QMDCollectionStatus describes one QMD collection for deep diagnostics.
type QMDCollectionStatus struct {
	Role        string
	Name        string
	Path        string
	Pattern     string
	Present     bool
	Documents   int
	LastUpdated string
}

// QMDDiagnostics describes read-only QMD backend health.
type QMDDiagnostics struct {
	Configured      bool
	BinaryPath      string
	BinaryFound     bool
	Version         string
	IndexName       string
	IndexPath       string
	IndexExists     bool
	SearchMode      string
	Collections     []QMDCollectionStatus
	TotalDocuments  int
	NeedsEmbedding  int
	HasVectorIndex  bool
	EmbeddingReady  bool
	UpdateState     string
	LastError       string
	StableContract  string
	ModelSafeStatus string
}

type qmdSearchResult struct {
	DocID     string          `json:"docid"`
	Score     json.RawMessage `json:"score"`
	File      string          `json:"file"`
	Path      string          `json:"path"`
	Title     string          `json:"title"`
	Context   string          `json:"context"`
	Body      string          `json:"body"`
	Content   string          `json:"content"`
	Text      string          `json:"text"`
	Snippet   string          `json:"snippet"`
	Line      int             `json:"line"`
	StartLine int             `json:"start_line"`
	EndLine   int             `json:"end_line"`
}

// NewQMDBackend creates a QMD CLI memory backend.
func NewQMDBackend(cfg QMDConfig) *QMDBackend {
	return &QMDBackend{cfg: normalizeQMDConfig(cfg)}
}

func (b *QMDBackend) Name() string {
	return "qmd"
}

func (b *QMDBackend) Search(ctx context.Context, query string, topK int, expand bool) ([]MemoryResult, error) {
	if b == nil {
		return nil, fmt.Errorf("qmd backend is not configured")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("qmd query is empty")
	}
	if topK <= 0 {
		topK = DefaultSearchTopK
	}

	args := b.searchArgs(query, topK, expand)
	out, err := b.run(ctx, args...)
	if err != nil {
		b.setLastError(err)
		return nil, err
	}

	rows, err := parseQMDSearchOutput(out)
	if err != nil {
		err = fmt.Errorf("parse qmd json output: %w", err)
		b.setLastError(err)
		return nil, err
	}

	results := make([]MemoryResult, 0, len(rows))
	for i, row := range rows {
		results = append(results, b.convertResult(row, int64(i+1)))
	}
	b.setLastError(nil)
	return results, nil
}

// Update runs QMD's explicit update/index lifecycle command. This is only
// called from operator commands; ok-gobot never updates QMD automatically.
func (b *QMDBackend) Update(ctx context.Context) ([]byte, error) {
	if b == nil {
		return nil, fmt.Errorf("qmd backend is not configured")
	}
	out, err := b.run(ctx, b.updateArgs()...)
	if err != nil {
		b.setLastError(err)
		return nil, err
	}
	b.setLastError(nil)
	return out, nil
}

// Diagnostics returns read-only QMD health information. It intentionally avoids
// `qmd status` because QMD 2.1.0 status may initialize local model tooling.
func (b *QMDBackend) Diagnostics(ctx context.Context) QMDDiagnostics {
	if b == nil {
		return QMDDiagnostics{Configured: false}
	}
	cfg := normalizeQMDConfig(b.cfg)
	d := QMDDiagnostics{
		Configured:      true,
		BinaryPath:      cfg.BinaryPath,
		IndexName:       cfg.Index,
		IndexPath:       inferQMDIndexPath(cfg),
		SearchMode:      cfg.SearchMode,
		StableContract:  "qmd --version; qmd search|vsearch|query --json -n N [-c collection]",
		ModelSafeStatus: "uses qmd --version plus SQLite metadata; does not run qmd status",
		LastError:       b.LastError(),
	}

	if _, err := exec.LookPath(cfg.BinaryPath); err != nil {
		d.LastError = fmt.Sprintf("qmd binary not found: %v", err)
		d.UpdateState = "qmd unavailable"
		return d
	}
	d.BinaryFound = true
	if version, err := b.run(ctx, "--version"); err == nil {
		d.Version = strings.TrimSpace(string(version))
	} else if d.LastError == "" {
		d.LastError = err.Error()
	}

	if _, err := os.Stat(d.IndexPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			d.UpdateState = "index missing"
		} else {
			d.UpdateState = "index unavailable"
			d.LastError = err.Error()
		}
		return d
	}
	d.IndexExists = true

	if err := loadQMDIndexDiagnostics(&d, cfg.Collections); err != nil {
		d.UpdateState = "index unreadable"
		if d.LastError == "" {
			d.LastError = err.Error()
		}
		return d
	}

	if d.TotalDocuments == 0 {
		d.UpdateState = "empty index"
	} else if d.NeedsEmbedding > 0 {
		d.UpdateState = "pending embeddings"
	} else {
		d.UpdateState = "ready"
	}
	d.EmbeddingReady = d.HasVectorIndex && d.TotalDocuments > 0 && d.NeedsEmbedding == 0
	return d
}

// LastError returns the last search error observed by the QMD backend.
func (b *QMDBackend) LastError() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lastErr == nil {
		return ""
	}
	return b.lastErr.Error()
}

func (b *QMDBackend) setLastError(err error) {
	b.mu.Lock()
	b.lastErr = err
	b.mu.Unlock()
}

func (b *QMDBackend) searchArgs(query string, topK int, expand bool) []string {
	mode := b.cfg.SearchMode
	if mode == "" {
		mode = DefaultQMDSearchMode
	}

	args := make([]string, 0, 8+len(b.cfg.Collections.Names())*2)
	if b.cfg.Index != "" {
		args = append(args, "--index", b.cfg.Index)
	}
	args = append(args, mode, query, "--json", "-n", strconv.Itoa(topK))
	if expand {
		args = append(args, "--full")
	}
	for _, collection := range b.cfg.Collections.Names() {
		args = append(args, "-c", collection)
	}
	return args
}

func (b *QMDBackend) updateArgs() []string {
	args := make([]string, 0, 3)
	if b.cfg.Index != "" {
		args = append(args, "--index", b.cfg.Index)
	}
	return append(args, "update")
}

func (b *QMDBackend) run(ctx context.Context, args ...string) ([]byte, error) {
	cfg := normalizeQMDConfig(b.cfg)
	path, err := exec.LookPath(cfg.BinaryPath)
	if err != nil {
		return nil, fmt.Errorf("qmd binary not found: %w", err)
	}

	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultQMDTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, path, args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	if cfg.IndexPath != "" {
		cmd.Env = append(cmd.Env, "INDEX_PATH="+expandUserPath(cfg.IndexPath))
	}
	out, err := cmd.CombinedOutput()
	if runCtx.Err() != nil {
		return nil, fmt.Errorf("qmd command timed out after %s", cfg.Timeout)
	}
	if err != nil {
		return nil, fmt.Errorf("qmd command failed: %w: %s", err, truncateForError(strings.TrimSpace(string(out)), 1200))
	}
	return out, nil
}

func (b *QMDBackend) convertResult(row qmdSearchResult, fallbackID int64) MemoryResult {
	source := firstNonEmpty(row.File, row.Path)
	sourceFile := b.sourceForQMDFile(source)
	content := firstNonEmpty(row.Body, row.Content, row.Text, row.Snippet)
	if row.Context != "" && content != "" && !strings.Contains(content, row.Context) {
		content = "Context: " + row.Context + "\n\n" + content
	}

	startLine := row.StartLine
	endLine := row.EndLine
	if startLine == 0 && row.Line > 0 {
		startLine = row.Line
	}
	if hunkStart, hunkEnd := parseQMDHunkLine(row.Snippet); hunkStart > 0 {
		if startLine == 0 {
			startLine = hunkStart
		}
		if endLine == 0 && startLine == hunkStart {
			endLine = hunkEnd
		}
	}
	if endLine == 0 && startLine > 0 {
		endLine = startLine
	}

	return MemoryResult{
		ID:           fallbackID,
		Source:       sourceFile,
		SourceFile:   sourceFile,
		HeaderPath:   "",
		StartLine:    startLine,
		EndLine:      endLine,
		ChunkOrdinal: startLine,
		Content:      strings.TrimSpace(content),
		ContentHash:  strings.TrimPrefix(row.DocID, "#"),
		Similarity:   parseQMDScore(row.Score),
	}
}

func (b *QMDBackend) sourceForQMDFile(file string) string {
	collection, rel, ok := parseQMDURI(file)
	if !ok {
		return strings.TrimSpace(file)
	}

	role := b.cfg.Collections.Role(collection)
	switch role {
	case "workspace":
		return rel
	case "daily_notes":
		return joinSlash("memory", rel)
	case "session_transcripts":
		return joinSlash("sessions", rel)
	default:
		return rel
	}
}

func parseQMDSearchOutput(out []byte) ([]qmdSearchResult, error) {
	jsonBytes, err := extractFirstJSON(out)
	if err != nil {
		return nil, err
	}

	var rows []qmdSearchResult
	if err := json.Unmarshal(jsonBytes, &rows); err == nil {
		return rows, nil
	}

	var wrapped struct {
		Results []qmdSearchResult `json:"results"`
	}
	if err := json.Unmarshal(jsonBytes, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Results, nil
}

func extractFirstJSON(out []byte) ([]byte, error) {
	start := findLikelyJSONStart(out)
	if start < 0 {
		return nil, fmt.Errorf("no JSON object or array found")
	}

	inString := false
	escaped := false
	depth := 0
	for i := start; i < len(out); i++ {
		ch := out[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				return bytes.TrimSpace(out[start : i+1]), nil
			}
		}
	}

	return nil, fmt.Errorf("unterminated JSON output")
}

func findLikelyJSONStart(out []byte) int {
	for i, ch := range out {
		switch ch {
		case '{':
			return i
		case '[':
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\n' || out[j] == '\r' || out[j] == '\t') {
				j++
			}
			if j < len(out) && (out[j] == '{' || out[j] == ']') {
				return i
			}
		}
	}
	return -1
}

func parseQMDScore(raw json.RawMessage) float32 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return float32(f)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if parsed, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 32); err == nil {
			if strings.HasSuffix(s, "%") {
				parsed = parsed / 100
			}
			return float32(parsed)
		}
	}
	return 0
}

func parseQMDHunkLine(snippet string) (int, int) {
	matches := qmdHunkLineRegexp.FindStringSubmatch(snippet)
	if len(matches) == 0 {
		return 0, 0
	}
	start, _ := strconv.Atoi(matches[1])
	count := 1
	if len(matches) > 2 && matches[2] != "" {
		count, _ = strconv.Atoi(matches[2])
	}
	if count <= 0 {
		count = 1
	}
	return start, start + count - 1
}

func loadQMDIndexDiagnostics(d *QMDDiagnostics, collections QMDCollections) error {
	db, err := sql.Open("sqlite3", sqliteReadOnlyDSN(d.IndexPath))
	if err != nil {
		return fmt.Errorf("open qmd index: %w", err)
	}
	defer db.Close() //nolint:errcheck

	if exists, err := qmdTableExists(db, "store_collections"); err != nil || !exists {
		if err != nil {
			return err
		}
		return fmt.Errorf("qmd store_collections table not found")
	}
	if exists, err := qmdTableExists(db, "documents"); err != nil || !exists {
		if err != nil {
			return err
		}
		return fmt.Errorf("qmd documents table not found")
	}

	rows, err := db.Query(`
		SELECT name, path, pattern
		FROM store_collections
		ORDER BY name
	`)
	if err != nil {
		return fmt.Errorf("query qmd collections: %w", err)
	}
	defer rows.Close()

	configured := collections.RoleMap()
	present := make(map[string]bool)
	for rows.Next() {
		var name, path, pattern string
		if err := rows.Scan(&name, &path, &pattern); err != nil {
			return fmt.Errorf("scan qmd collection: %w", err)
		}
		present[name] = true
		status := QMDCollectionStatus{
			Role:    configured[name],
			Name:    name,
			Path:    path,
			Pattern: pattern,
			Present: true,
		}
		if status.Role == "" {
			status.Role = "default"
		}
		_ = db.QueryRow(`SELECT COUNT(*) FROM documents WHERE active = 1 AND collection = ?`, name).Scan(&status.Documents)
		_ = db.QueryRow(`SELECT COALESCE(MAX(modified_at), '') FROM documents WHERE active = 1 AND collection = ?`, name).Scan(&status.LastUpdated)
		d.Collections = append(d.Collections, status)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read qmd collections: %w", err)
	}

	for name, role := range configured {
		if name == "" || present[name] {
			continue
		}
		d.Collections = append(d.Collections, QMDCollectionStatus{Role: role, Name: name, Present: false})
	}
	sort.SliceStable(d.Collections, func(i, j int) bool {
		if d.Collections[i].Role == d.Collections[j].Role {
			return d.Collections[i].Name < d.Collections[j].Name
		}
		return d.Collections[i].Role < d.Collections[j].Role
	})

	if err := db.QueryRow(`SELECT COUNT(*) FROM documents WHERE active = 1`).Scan(&d.TotalDocuments); err != nil {
		return fmt.Errorf("count qmd documents: %w", err)
	}
	d.NeedsEmbedding = 0
	if exists, err := qmdTableExists(db, "content_vectors"); err != nil {
		return err
	} else if exists {
		if err := db.QueryRow(`
			SELECT COUNT(DISTINCT d.hash)
			FROM documents d
			LEFT JOIN content_vectors v ON d.hash = v.hash AND v.seq = 0
			WHERE d.active = 1 AND v.hash IS NULL
		`).Scan(&d.NeedsEmbedding); err != nil {
			return fmt.Errorf("count qmd pending embeddings: %w", err)
		}
	} else {
		d.NeedsEmbedding = d.TotalDocuments
	}
	d.HasVectorIndex, err = qmdTableExists(db, "vectors_vec")
	if err != nil {
		return err
	}
	return nil
}

func sqliteReadOnlyDSN(path string) string {
	absPath, err := filepath.Abs(path)
	if err == nil {
		path = absPath
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
}

func qmdTableExists(db *sql.DB, name string) (bool, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect qmd table %s: %w", name, err)
	}
	return count > 0, nil
}

func normalizeQMDConfig(cfg QMDConfig) QMDConfig {
	cfg.BinaryPath = strings.TrimSpace(cfg.BinaryPath)
	if cfg.BinaryPath == "" {
		cfg.BinaryPath = DefaultQMDBinaryPath
	}
	cfg.Index = strings.TrimSpace(cfg.Index)
	if cfg.Index == "" {
		cfg.Index = DefaultQMDIndex
	}
	cfg.IndexPath = strings.TrimSpace(cfg.IndexPath)
	cfg.SearchMode = strings.ToLower(strings.TrimSpace(cfg.SearchMode))
	if cfg.SearchMode == "" {
		cfg.SearchMode = DefaultQMDSearchMode
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultQMDTimeout
	}
	cfg.Collections = cfg.Collections.Normalized()
	return cfg
}

// Names returns configured collection names without duplicates.
func (c QMDCollections) Names() []string {
	c = c.Normalized()
	seen := make(map[string]bool)
	var names []string
	for _, name := range []string{c.Workspace, c.DailyNotes, c.SessionTranscripts} {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, name := range c.ExtraPaths {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// Role returns the ok-gobot role for a QMD collection name.
func (c QMDCollections) Role(name string) string {
	return c.RoleMap()[name]
}

// RoleMap returns configured collection name to role mappings.
func (c QMDCollections) RoleMap() map[string]string {
	c = c.Normalized()
	out := make(map[string]string)
	if c.Workspace != "" {
		out[c.Workspace] = "workspace"
	}
	if c.DailyNotes != "" {
		out[c.DailyNotes] = "daily_notes"
	}
	if c.SessionTranscripts != "" {
		out[c.SessionTranscripts] = "session_transcripts"
	}
	for _, name := range c.ExtraPaths {
		if name != "" {
			out[name] = "extra_paths"
		}
	}
	return out
}

// Normalized trims collection names.
func (c QMDCollections) Normalized() QMDCollections {
	c.Workspace = strings.TrimSpace(c.Workspace)
	c.DailyNotes = strings.TrimSpace(c.DailyNotes)
	c.SessionTranscripts = strings.TrimSpace(c.SessionTranscripts)
	cleaned := make([]string, 0, len(c.ExtraPaths))
	for _, name := range c.ExtraPaths {
		name = strings.TrimSpace(name)
		if name != "" {
			cleaned = append(cleaned, name)
		}
	}
	c.ExtraPaths = cleaned
	return c
}

func inferQMDIndexPath(cfg QMDConfig) string {
	if cfg.IndexPath != "" {
		return expandUserPath(cfg.IndexPath)
	}
	if envPath := strings.TrimSpace(os.Getenv("INDEX_PATH")); envPath != "" {
		return expandUserPath(envPath)
	}
	cacheDir := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
	if cacheDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		cacheDir = filepath.Join(home, ".cache")
	}
	index := strings.TrimSpace(cfg.Index)
	if index == "" {
		index = DefaultQMDIndex
	}
	return filepath.Join(expandUserPath(cacheDir), "qmd", index+".sqlite")
}

func parseQMDURI(file string) (string, string, bool) {
	file = strings.TrimSpace(file)
	if !strings.HasPrefix(file, "qmd://") {
		return "", "", false
	}
	path := strings.TrimPrefix(file, "qmd://")
	path = strings.TrimLeft(path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], filepath.ToSlash(filepath.Clean(parts[1])), true
}

func joinSlash(prefix, rel string) string {
	rel = strings.Trim(strings.TrimSpace(rel), "/")
	if rel == "" || rel == "." {
		return prefix
	}
	return prefix + "/" + filepath.ToSlash(rel)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func expandUserPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func truncateForError(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
