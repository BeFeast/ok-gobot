package tools

import (
	"context"
	"fmt"
	"io/fs"
	"math"
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
	// Score is the retrieval score summed across matching chunks. It breaks
	// ties between notes with equal term coverage, but only at the resolution
	// of vaultScoreMantissaBits: its low bits are summation-order noise, so
	// ranking never treats an exact float difference as meaningful.
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

// vaultScoreMantissaBits is how many mantissa bits of a note's retrieval score
// survive into the sort key. Score is a float32 (24 mantissa bits) accumulated
// with += across every matching chunk of a note, one query term at a time, so
// its low bits carry summation-order noise rather than signal: the same note
// over the same chunks lands on a different last bit when the index hands the
// chunks back in a different order. Keeping the top 16 bits gives a relative
// resolution of ~1.5e-5 — comfortably above that noise floor (a few hundred
// float32 additions drift by ~1e-6 relative) and comfortably below any score
// gap that means anything for ranking.
const vaultScoreMantissaBits = 16

// vaultScoreKey quantises a retrieval score to vaultScoreMantissaBits of
// relative precision, so two notes whose scores differ only by accumulation
// noise share a key and let the deterministic tiebreak decide the order.
//
// Quantising first is what makes the comparator sound. The obvious
// alternative — a pairwise |a-b| < epsilon test — is not transitive (a≈b and
// b≈c does not give a≈c), so it is not a strict weak ordering and sort's
// output would be undefined. Any pure function of the score is transitive by
// construction. The rounding is relative rather than absolute because the
// accumulation error scales with the size of the sum: a note built from 200
// chunks has a far larger absolute noise floor than one built from three.
func vaultScoreKey(score float32) float64 {
	value := float64(score)
	if math.IsNaN(value) {
		// NaN compares false against everything, which would make it
		// "equivalent" to every other score and break transitivity. Pin a
		// corrupt score to the bottom instead.
		return math.Inf(-1)
	}
	if value == 0 || math.IsInf(value, 0) {
		return value
	}
	frac, exp := math.Frexp(value)
	const scale = 1 << vaultScoreMantissaBits
	return math.Ldexp(math.Round(frac*scale)/scale, exp)
}

// rankVaultHits orders notes by distinct term coverage first, quantised
// retrieval score second, and note path last. Coverage is the ranking signal
// that matters: a note matching four of five query terms is about the
// question, a note matching one common term is not.
//
// The ordering is total and deterministic. Note paths are the keys of the
// notes map, so they are unique, so the final tiebreak always resolves: for a
// given set of hits exactly one output permutation is valid, and the
// randomised map iteration order that seeds the slice cannot reach the result.
func rankVaultHits(notes map[string]*VaultSearchHit, limit int) []VaultSearchHit {
	if len(notes) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = DefaultVaultSearchLimit
	}

	type rankedVaultHit struct {
		hit      VaultSearchHit
		coverage int
		scoreKey float64
	}

	ranked := make([]rankedVaultHit, 0, len(notes))
	for _, hit := range notes {
		sort.Strings(hit.MatchedTerms)
		ranked = append(ranked, rankedVaultHit{
			hit:      *hit,
			coverage: len(hit.MatchedTerms),
			scoreKey: vaultScoreKey(hit.Score),
		})
	}

	// The score key is compared with > and < only, never for equality: a float
	// is an ordering signal here and must never be asked whether two values
	// are "the same". Scores that rank equal fall through to the note path.
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].coverage != ranked[j].coverage {
			return ranked[i].coverage > ranked[j].coverage
		}
		if ranked[i].scoreKey > ranked[j].scoreKey {
			return true
		}
		if ranked[i].scoreKey < ranked[j].scoreKey {
			return false
		}
		return ranked[i].hit.Note < ranked[j].hit.Note
	})

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	hits := make([]VaultSearchHit, len(ranked))
	for i, entry := range ranked {
		hits[i] = entry.hit
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
