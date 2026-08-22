package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ok-gobot/internal/memory"
)

// DefaultVaultSearchLimit is how many notes a vault search returns unless the
// caller asks for more.
const DefaultVaultSearchLimit = 10

// maxVaultSearchLimit caps the note count so a broad query cannot flood a
// chat message.
const maxVaultSearchLimit = 50

// vaultScanMaxFileSize skips pathological files during the filesystem scan.
const vaultScanMaxFileSize = 4 << 20

// minVaultCandidatesPerTerm is the floor on chunks fetched per query term.
// Notes are chunked by header, so a handful of chunks per term is not enough
// candidate breadth to rank notes by term coverage.
const minVaultCandidatesPerTerm = 60

// VaultIndex is the retrieval store the Obsidian search path queries. It is
// the subset of *memory.MemoryManager that vault search needs, expressed as an
// interface so the tool can be exercised without a database.
type VaultIndex interface {
	// LexicalIndexAvailable reports whether keyword search actually works.
	// False means the caller must fail loudly instead of returning nothing.
	LexicalIndexAvailable() bool
	// CountChunks reports how many indexed chunks live under sourcePrefix.
	CountChunks(ctx context.Context, sourcePrefix string) (int, error)
	// SearchScoped runs one term against the index.
	SearchScoped(ctx context.Context, query string, topK int, policy *memory.RecallPolicy) ([]memory.MemoryResult, error)
}

// VaultSearchHit is one note that matched at least one query term.
type VaultSearchHit struct {
	// Note is the vault-relative path with the .md suffix removed.
	Note string
	// MatchedTerms are the distinct composed query terms this note matched.
	MatchedTerms []string
	// Score is the retrieval score summed across matching chunks; it breaks
	// ties between notes with equal term coverage.
	Score float32
}

// VaultSearchResult carries the hits plus enough provenance for the caller to
// tell a good result from a degraded one.
type VaultSearchResult struct {
	// Backend names the path that produced the hits.
	Backend string
	// Terms are the content terms actually searched, after stopword removal.
	Terms []string
	Hits  []VaultSearchHit
}

// WithIndex returns a copy of the tool whose search operation queries the
// retrieval index instead of scanning the filesystem. sourcePrefix scopes
// results to the collection holding this vault (e.g. "extra:obsidian/"); an
// empty prefix searches the whole index.
func (o *ObsidianTool) WithIndex(index VaultIndex, sourcePrefix string) *ObsidianTool {
	if o == nil {
		return nil
	}
	clone := *o
	clone.index = index
	clone.indexPrefix = sourcePrefix
	return &clone
}

// SearchNotes finds vault notes relevant to a natural-language query.
//
// The query is never matched verbatim: a whole question ("как я настраивал
// backup на сервере?") occurs in no note, which is why the previous
// strings.Contains implementation returned nothing for real questions. Instead
// the question is decomposed into content terms, each term is searched
// separately, and notes are ranked by how many distinct terms they match.
func (o *ObsidianTool) SearchNotes(ctx context.Context, query string, limit int) (VaultSearchResult, error) {
	if o == nil || strings.TrimSpace(o.VaultPath) == "" {
		return VaultSearchResult{}, fmt.Errorf("Obsidian vault directory is not configured")
	}

	terms := memory.ComposeQueryTerms(query)
	if len(terms) == 0 {
		return VaultSearchResult{}, fmt.Errorf("obsidian search: %q contains no searchable terms", query)
	}

	limit = clampVaultSearchLimit(limit)
	if o.index != nil {
		return o.searchViaIndex(ctx, terms, limit)
	}
	return o.searchViaFilesystem(terms, limit)
}

// searchViaIndex queries the retrieval store once per term and merges by note.
func (o *ObsidianTool) searchViaIndex(ctx context.Context, terms []string, limit int) (VaultSearchResult, error) {
	if !o.index.LexicalIndexAvailable() {
		return VaultSearchResult{}, fmt.Errorf(
			"obsidian search: the lexical index is unavailable — this binary was built without SQLite FTS5, " +
				"so keyword search cannot run (rebuild with `go build -tags sqlite_fts5`). " +
				"Refusing to return silently degraded results")
	}

	count, err := o.index.CountChunks(ctx, o.indexPrefix)
	if err != nil {
		return VaultSearchResult{}, fmt.Errorf("obsidian search: cannot inspect the retrieval index: %w", err)
	}
	if count == 0 {
		scope := "the retrieval index"
		if o.indexPrefix != "" {
			scope = fmt.Sprintf("retrieval index scope %q", o.indexPrefix)
		}
		return VaultSearchResult{}, fmt.Errorf(
			"obsidian search: %s holds no chunks for this vault (%s) — "+
				"add the vault to memory.extra_paths and run the indexer",
			scope, o.VaultPath)
	}

	// Ask for more chunks than notes wanted: one note contributes many chunks,
	// and a term that appears in a single popular note must not crowd out the
	// rest of the candidate set.
	perTerm := limit * 5
	if perTerm < minVaultCandidatesPerTerm {
		perTerm = minVaultCandidatesPerTerm
	}

	notes := make(map[string]*VaultSearchHit)
	for _, term := range terms {
		results, err := o.index.SearchScoped(ctx, term, perTerm, nil)
		if err != nil {
			return VaultSearchResult{}, fmt.Errorf("obsidian search: term %q failed against the retrieval index: %w", term, err)
		}
		for _, result := range results {
			note, ok := o.noteFromSource(result.SourceFile)
			if !ok {
				continue
			}
			hit := notes[note]
			if hit == nil {
				hit = &VaultSearchHit{Note: note}
				notes[note] = hit
			}
			hit.Score += result.Score
			if !containsString(hit.MatchedTerms, term) {
				hit.MatchedTerms = append(hit.MatchedTerms, term)
			}
		}
	}

	return VaultSearchResult{
		Backend: "retrieval index (fts5/bm25)",
		Terms:   terms,
		Hits:    rankVaultHits(notes, limit),
	}, nil
}

// searchViaFilesystem is the no-index path. It is still term-decomposed and
// coverage-ranked — the naive version matched the whole question as one
// literal string and therefore matched nothing.
func (o *ObsidianTool) searchViaFilesystem(terms []string, limit int) (VaultSearchResult, error) {
	notes := make(map[string]*VaultSearchHit)

	walkErr := filepath.WalkDir(o.VaultPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A single unreadable subtree must not abort the whole search.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != o.VaultPath && strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(name), ".md") {
			return nil
		}
		if info, err := entry.Info(); err == nil && info.Size() > vaultScanMaxFileSize {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(o.VaultPath, path)
		if err != nil {
			return nil
		}
		note := strings.TrimSuffix(filepath.ToSlash(rel), ".md")

		// The note path is part of the haystack: vault filenames carry the
		// subject ("Areas/backup/restore-runbook.md") more reliably than any
		// single line of body text.
		haystack := strings.ToLower(note + "\n" + string(content))

		var matched []string
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				matched = append(matched, term)
			}
		}
		if len(matched) == 0 {
			return nil
		}
		notes[note] = &VaultSearchHit{
			Note:         note,
			MatchedTerms: matched,
			Score:        float32(len(matched)),
		}
		return nil
	})
	if walkErr != nil {
		return VaultSearchResult{}, fmt.Errorf("obsidian search: scanning %s failed: %w", o.VaultPath, walkErr)
	}

	return VaultSearchResult{
		Backend: "filesystem scan (no retrieval index configured)",
		Terms:   terms,
		Hits:    rankVaultHits(notes, limit),
	}, nil
}

// noteFromSource maps an indexed chunk's source label back to a vault-relative
// note name, rejecting chunks that belong to a different collection.
func (o *ObsidianTool) noteFromSource(sourceFile string) (string, bool) {
	source := strings.TrimSpace(sourceFile)
	if source == "" {
		return "", false
	}
	if o.indexPrefix != "" {
		if !strings.HasPrefix(source, o.indexPrefix) {
			return "", false
		}
		source = strings.TrimPrefix(source, o.indexPrefix)
	}
	source = strings.TrimPrefix(filepath.ToSlash(source), "./")
	if source == "" {
		return "", false
	}
	return strings.TrimSuffix(source, ".md"), true
}

// rankVaultHits orders notes by distinct term coverage first and retrieval
// score second. Coverage is the ranking signal that matters: a note matching
// four of five query terms is about the question, a note matching one common
// term is not.
func rankVaultHits(notes map[string]*VaultSearchHit, limit int) []VaultSearchHit {
	if len(notes) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = DefaultVaultSearchLimit
	}

	hits := make([]VaultSearchHit, 0, len(notes))
	for _, hit := range notes {
		sort.Strings(hit.MatchedTerms)
		hits = append(hits, *hit)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if len(hits[i].MatchedTerms) != len(hits[j].MatchedTerms) {
			return len(hits[i].MatchedTerms) > len(hits[j].MatchedTerms)
		}
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Note < hits[j].Note
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// Format renders a search result. The backend and the searched terms are
// always shown, including when nothing matched, so a degraded or misdirected
// search is visible instead of looking like "no such note".
func (r VaultSearchResult) Format() string {
	var out strings.Builder
	fmt.Fprintf(&out, "Vault search via %s\n", r.Backend)
	fmt.Fprintf(&out, "Terms searched: %s\n\n", strings.Join(r.Terms, ", "))

	if len(r.Hits) == 0 {
		out.WriteString("No notes matched any of these terms.")
		return out.String()
	}

	fmt.Fprintf(&out, "%d note(s), ranked by how many query terms each matches:\n", len(r.Hits))
	for i, hit := range r.Hits {
		fmt.Fprintf(&out, "%d. %s (%d/%d terms: %s)\n",
			i+1, hit.Note, len(hit.MatchedTerms), len(r.Terms), strings.Join(hit.MatchedTerms, ", "))
	}
	return strings.TrimRight(out.String(), "\n")
}

// Notes returns just the note paths, best first.
func (r VaultSearchResult) Notes() []string {
	notes := make([]string, 0, len(r.Hits))
	for _, hit := range r.Hits {
		notes = append(notes, hit.Note)
	}
	return notes
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// vaultIndexPrefixFor returns the indexed source prefix for vaultPath when the
// vault is one of the configured extra collections, so search results can be
// scoped to it. An empty result means the vault is not a known collection.
func vaultIndexPrefixFor(extras []memory.ExtraPath, vaultPath string) string {
	target, err := filepath.Abs(vaultPath)
	if err != nil {
		return ""
	}
	target = filepath.Clean(target)
	for _, extra := range extras {
		if filepath.Clean(extra.Path) == target {
			return memory.ExtraSourcePrefix + extra.Name + "/"
		}
	}
	return ""
}

// clampVaultSearchLimit keeps caller-supplied limits inside sane bounds.
func clampVaultSearchLimit(limit int) int {
	if limit <= 0 {
		return DefaultVaultSearchLimit
	}
	if limit > maxVaultSearchLimit {
		return maxVaultSearchLimit
	}
	return limit
}
