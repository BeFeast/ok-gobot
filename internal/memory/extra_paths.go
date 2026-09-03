package memory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ExtraSourcePrefix is the source_file prefix used for chunks that originate
// from a configured memory.extra_paths collection. Final source labels look
// like "extra:<collection>/<relative-markdown-path>".
const ExtraSourcePrefix = "extra:"

// DefaultExtraPathPattern is applied when an ExtraPath specifies no patterns.
const DefaultExtraPathPattern = "**/*.md"

var (
	extraCollectionNameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

// ExtraPath describes one additional named markdown collection to index.
// All fields are validated and normalized via NormalizeExtraPaths.
type ExtraPath struct {
	Name     string
	Path     string // resolved absolute path
	Patterns []string
	ReadOnly bool
	Scope    string
}

// ExtraPathStatus reports diagnostic information about a single configured
// extra path: whether it is reachable on disk, how many sources matched the
// configured globs, and how many indexed chunks belong to it.
type ExtraPathStatus struct {
	Name        string
	Path        string
	Scope       string
	ReadOnly    bool
	Available   bool
	Error       string
	SourceCount int
	ChunkCount  int
}

// HomeDirFunc resolves "~" prefixes in paths. Override in tests.
var HomeDirFunc = os.UserHomeDir

// expandHome expands a leading "~/" using the current user's home directory.
func expandHome(path string) (string, error) {
	if path == "~" {
		home, err := HomeDirFunc()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := HomeDirFunc()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// NormalizeExtraPaths validates a list of (name, path, patterns, readOnly,
// scope) tuples and returns canonical ExtraPath entries. Paths are expanded
// (~ prefix), made absolute, and cleaned. The returned slice is safe to use
// for indexing.
//
// Names must be unique and match [a-z0-9][a-z0-9_-]*. Empty names or paths
// are rejected.
func NormalizeExtraPaths(raw []RawExtraPath) ([]ExtraPath, error) {
	out := make([]ExtraPath, 0, len(raw))
	seenNames := make(map[string]struct{}, len(raw))

	for i, r := range raw {
		name := strings.ToLower(strings.TrimSpace(r.Name))
		if name == "" {
			return nil, fmt.Errorf("memory.extra_paths[%d]: name is required", i)
		}
		if !extraCollectionNameRegexp.MatchString(name) {
			return nil, fmt.Errorf("memory.extra_paths[%d]: invalid name %q (must match %s)", i, r.Name, extraCollectionNameRegexp.String())
		}
		if _, dup := seenNames[name]; dup {
			return nil, fmt.Errorf("memory.extra_paths[%d]: duplicate name %q", i, name)
		}
		seenNames[name] = struct{}{}

		rawPath := strings.TrimSpace(r.Path)
		if rawPath == "" {
			return nil, fmt.Errorf("memory.extra_paths[%q]: path is required", name)
		}
		expanded, err := expandHome(rawPath)
		if err != nil {
			return nil, fmt.Errorf("memory.extra_paths[%q]: expand home: %w", name, err)
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			return nil, fmt.Errorf("memory.extra_paths[%q]: resolve path: %w", name, err)
		}
		abs = filepath.Clean(abs)

		patterns := make([]string, 0, len(r.Patterns))
		for _, p := range r.Patterns {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			patterns = append(patterns, p)
		}
		if len(patterns) == 0 {
			patterns = []string{DefaultExtraPathPattern}
		}

		readOnly := true
		if r.ReadOnly != nil {
			readOnly = *r.ReadOnly
		}

		out = append(out, ExtraPath{
			Name:     name,
			Path:     abs,
			Patterns: patterns,
			ReadOnly: readOnly,
			Scope:    strings.TrimSpace(r.Scope),
		})
	}

	return out, nil
}

// RawExtraPath is the unvalidated form passed in from configuration.
// It mirrors config.MemoryExtraPathConfig without taking a dependency on the
// config package.
type RawExtraPath struct {
	Name     string
	Path     string
	Patterns []string
	ReadOnly *bool
	Scope    string
}

// SourceLabelForExtra builds the canonical source label for an indexed chunk
// originating from the named extra collection at relativePath (forward slashes).
func SourceLabelForExtra(collection, relativePath string) string {
	rel := filepath.ToSlash(filepath.Clean(relativePath))
	rel = strings.TrimPrefix(rel, "./")
	return ExtraSourcePrefix + collection + "/" + rel
}

// ParseExtraSourceLabel parses a source label (e.g. "extra:obsidian/notes/x.md")
// and returns the collection name and the relative path. The second return value
// is true only when the label has the "extra:" prefix and contains both a
// collection name and a relative path.
func ParseExtraSourceLabel(source string) (collection, relativePath string, ok bool) {
	if !strings.HasPrefix(source, ExtraSourcePrefix) {
		return "", "", false
	}
	rest := source[len(ExtraSourcePrefix):]
	idx := strings.Index(rest, "/")
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

// ExtraPathSources walks an extra path's configured globs and returns the
// matched markdown sources. Hidden directories (starting with '.') and any
// path with a hidden component are skipped. Symlinks (or paths beneath
// symlinked directories) that resolve outside the collection root are
// rejected to prevent indexing arbitrary filesystem content. Missing roots
// return an empty list and a nil error so callers can degrade gracefully and
// surface the missing path through ExtraPathDiagnostics instead.
func ExtraPathSources(extra ExtraPath) ([]ManagedSource, error) {
	info, err := os.Stat(extra.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat extra path %q: %w", extra.Path, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("extra path %q is not a directory", extra.Path)
	}

	patterns := extra.Patterns
	if len(patterns) == 0 {
		patterns = []string{DefaultExtraPathPattern}
	}

	resolvedRoot, err := filepath.EvalSymlinks(extra.Path)
	if err != nil {
		return nil, fmt.Errorf("resolve extra path %q: %w", extra.Path, err)
	}
	resolvedRootSep := resolvedRoot + string(os.PathSeparator)

	matches := make(map[string]struct{})
	walkErr := filepath.WalkDir(extra.Path, func(current string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if current == extra.Path {
			return nil
		}
		base := d.Name()
		if strings.HasPrefix(base, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(extra.Path, current)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		for _, pattern := range patterns {
			ok, err := matchExtraGlob(pattern, relSlash)
			if err != nil {
				return fmt.Errorf("invalid pattern %q: %w", pattern, err)
			}
			if ok {
				resolvedCurrent, err := filepath.EvalSymlinks(current)
				if err != nil {
					// Skip entries whose targets cannot be resolved (e.g.
					// broken symlinks) instead of aborting the entire walk.
					return nil
				}
				if resolvedCurrent != resolvedRoot && !strings.HasPrefix(resolvedCurrent, resolvedRootSep) {
					// Symlink (or symlinked-directory descendant) escapes
					// the collection root — skip it silently to avoid
					// surfacing arbitrary filesystem content.
					return nil
				}
				matches[current] = struct{}{}
				return nil
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sorted := make([]string, 0, len(matches))
	for path := range matches {
		sorted = append(sorted, path)
	}
	sort.Strings(sorted)

	out := make([]ManagedSource, 0, len(sorted))
	for _, abs := range sorted {
		rel, err := filepath.Rel(extra.Path, abs)
		if err != nil {
			continue
		}
		out = append(out, ManagedSource{
			Path:         filepath.Clean(abs),
			RelativePath: SourceLabelForExtra(extra.Name, rel),
		})
	}
	return out, nil
}

// matchExtraGlob matches a forward-slash relative path against a glob.
// It supports "**" as a recursive wildcard in addition to the standard
// filepath.Match semantics.
func matchExtraGlob(pattern, name string) (bool, error) {
	if !strings.Contains(pattern, "**") {
		return filepath.Match(pattern, name)
	}

	patternParts := strings.Split(pattern, "/")
	nameParts := strings.Split(name, "/")
	return matchSegments(patternParts, nameParts)
}

func matchSegments(pattern, name []string) (bool, error) {
	if len(pattern) == 0 {
		return len(name) == 0, nil
	}

	head := pattern[0]
	if head == "**" {
		// "**" matches zero or more path segments. Try every prefix length.
		// Skip consecutive "**" entries.
		rest := pattern[1:]
		for len(rest) > 0 && rest[0] == "**" {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return true, nil
		}
		for i := 0; i <= len(name); i++ {
			ok, err := matchSegments(rest, name[i:])
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}

	if len(name) == 0 {
		return false, nil
	}
	ok, err := filepath.Match(head, name[0])
	if err != nil || !ok {
		return false, err
	}
	return matchSegments(pattern[1:], name[1:])
}

// ExtraPathByLabel returns the extra path whose collection name matches the
// "extra:<name>/" prefix of source. The second return value is true only when
// the source label is well-formed and the collection is registered.
func ExtraPathByLabel(extras []ExtraPath, source string) (ExtraPath, string, bool) {
	collection, relativePath, ok := ParseExtraSourceLabel(source)
	if !ok {
		return ExtraPath{}, "", false
	}
	for _, e := range extras {
		if e.Name == collection {
			return e, relativePath, true
		}
	}
	return ExtraPath{}, "", false
}

// ExtraPathRelative returns the slash-relative path inside extra.Path for an
// absolute filesystem path. Returns false when absPath is outside extra.Path
// or its globs do not match. Hidden segments are rejected.
func ExtraPathRelative(extra ExtraPath, absPath string) (string, bool) {
	cleanAbs, err := filepath.Abs(absPath)
	if err != nil {
		return "", false
	}
	cleanAbs = filepath.Clean(cleanAbs)
	rel, err := filepath.Rel(extra.Path, cleanAbs)
	if err != nil {
		return "", false
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	relSlash := filepath.ToSlash(rel)
	for _, part := range strings.Split(relSlash, "/") {
		if strings.HasPrefix(part, ".") {
			return "", false
		}
	}

	patterns := extra.Patterns
	if len(patterns) == 0 {
		patterns = []string{DefaultExtraPathPattern}
	}
	for _, pattern := range patterns {
		ok, err := matchExtraGlob(pattern, relSlash)
		if err != nil {
			return "", false
		}
		if ok {
			return relSlash, true
		}
	}
	return "", false
}

// ResolveExtraPathFile returns the absolute filesystem path for a relative
// markdown file inside an extra-path collection. It rejects traversal attempts
// and symlink escapes outside the collection root. The file is not required to
// exist; callers handle ENOENT separately.
func ResolveExtraPathFile(extra ExtraPath, relativePath string) (string, error) {
	clean := filepath.Clean(relativePath)
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("extra path source must be relative, got %q", relativePath)
	}
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("extra path source escapes collection root: %q", relativePath)
	}
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("extra path source has hidden segment %q", part)
		}
	}

	full := filepath.Join(extra.Path, clean)
	rootWithSep := extra.Path + string(os.PathSeparator)
	if full != extra.Path && !strings.HasPrefix(full, rootWithSep) {
		return "", fmt.Errorf("extra path source %q is outside collection %q", relativePath, extra.Name)
	}

	resolvedRoot, err := filepath.EvalSymlinks(extra.Path)
	if err == nil {
		resolvedFull, err := filepath.EvalSymlinks(full)
		if err == nil {
			resolvedRootSep := resolvedRoot + string(os.PathSeparator)
			if resolvedFull != resolvedRoot && !strings.HasPrefix(resolvedFull, resolvedRootSep) {
				return "", fmt.Errorf("extra path source %q resolves outside collection (symlink escape)", relativePath)
			}
		}
	}

	return full, nil
}

// IndexExtraPaths indexes every configured extra path. Errors for individual
// paths are collected so a single missing/unmounted root does not abort the
// whole pass. The returned IndexRunStats counts files indexed across all
// extras.
func IndexExtraPaths(ctx context.Context, extras []ExtraPath, indexer *Indexer) (IndexRunStats, []error) {
	var (
		stats IndexRunStats
		errs  []error
	)
	if indexer == nil {
		return stats, []error{fmt.Errorf("memory indexer is nil")}
	}

	for _, extra := range extras {
		sources, err := ExtraPathSources(extra)
		if err != nil {
			errs = append(errs, fmt.Errorf("extra path %q: %w", extra.Name, err))
			continue
		}
		for _, source := range sources {
			// The daemon runs this pass in the background now; stop promptly
			// on shutdown instead of failing every remaining file one by one.
			if err := ctx.Err(); err != nil {
				return stats, append(errs, err)
			}
			if err := indexer.IndexFile(ctx, source.Path, source.RelativePath); err != nil {
				errs = append(errs, fmt.Errorf("index %s: %w", source.RelativePath, err))
				continue
			}
			stats.FilesIndexed++
		}
	}
	return stats, errs
}

// ClearExtraSources removes indexed chunks for the given collection name.
// When name is empty, all extra-path chunks are removed.
func (s *MemoryStore) ClearExtraSources(ctx context.Context, name string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("memory store is not configured")
	}
	if strings.TrimSpace(name) == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM memory_chunks WHERE source_file GLOB 'extra:*'`)
		if err != nil {
			return fmt.Errorf("clear extra memory sources: %w", err)
		}
		return nil
	}
	// Use GLOB rather than LIKE so the underscore in collection names like
	// "my_vault" is matched literally instead of as a single-character LIKE
	// wildcard, which would otherwise also match "myXvault" prefixes.
	prefix := ExtraSourcePrefix + name + "/*"
	_, err := s.db.ExecContext(ctx, `DELETE FROM memory_chunks WHERE source_file GLOB ?`, prefix)
	if err != nil {
		return fmt.Errorf("clear extra memory source %q: %w", name, err)
	}
	return nil
}

// ExtraPathDiagnostics returns one ExtraPathStatus per configured extra path.
// Missing or unmounted roots are reported with Available=false and a non-empty
// Error so callers can surface them via `memory status` without crashing.
func (s *MemoryStore) ExtraPathDiagnostics(ctx context.Context, extras []ExtraPath) []ExtraPathStatus {
	out := make([]ExtraPathStatus, 0, len(extras))
	for _, extra := range extras {
		st := ExtraPathStatus{
			Name:     extra.Name,
			Path:     extra.Path,
			Scope:    extra.Scope,
			ReadOnly: extra.ReadOnly,
		}
		if info, err := os.Stat(extra.Path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				st.Error = "path does not exist"
			} else {
				st.Error = err.Error()
			}
		} else if !info.IsDir() {
			st.Error = "path is not a directory"
		} else {
			st.Available = true
			if sources, err := ExtraPathSources(extra); err != nil {
				st.Error = err.Error()
			} else {
				st.SourceCount = len(sources)
			}
		}

		if s != nil && s.db != nil {
			// GLOB avoids LIKE's wildcard treatment of underscore so
			// collections like "my_vault" don't include chunks from a
			// hypothetical "myXvault" collection.
			prefix := ExtraSourcePrefix + extra.Name + "/*"
			var count int
			if err := s.db.QueryRowContext(
				ctx,
				`SELECT COUNT(*) FROM memory_chunks WHERE source_file GLOB ?`,
				prefix,
			).Scan(&count); err == nil {
				st.ChunkCount = count
			}
		}

		out = append(out, st)
	}
	return out
}
