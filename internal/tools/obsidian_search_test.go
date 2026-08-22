package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/memory"
)

// fakeVaultIndex is a minimal lexical store: it matches a single term as a
// substring of chunk content, which is what the real lexical leg does for one
// composed term. It exists so the search path can be exercised without CGO,
// SQLite, or a particular FTS5 build tag.
type fakeVaultIndex struct {
	chunks    map[string]string // source_file -> content
	available bool
	countErr  error
	searchErr error
	queries   []string
}

func (f *fakeVaultIndex) LexicalIndexAvailable() bool { return f.available }

func (f *fakeVaultIndex) CountChunks(ctx context.Context, sourcePrefix string) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	count := 0
	for source := range f.chunks {
		if sourcePrefix == "" || strings.HasPrefix(source, sourcePrefix) {
			count++
		}
	}
	return count, nil
}

func (f *fakeVaultIndex) SearchScoped(ctx context.Context, query string, topK int, policy *memory.RecallPolicy) ([]memory.MemoryResult, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	f.queries = append(f.queries, query)

	var results []memory.MemoryResult
	for source, content := range f.chunks {
		if strings.Contains(strings.ToLower(content), strings.ToLower(query)) {
			results = append(results, memory.MemoryResult{
				Source:     source,
				SourceFile: source,
				Content:    content,
				Score:      1,
			})
		}
	}
	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

func russianVaultChunks() map[string]string {
	return map[string]string{
		"extra:vault/HomeLab/Resources/Runbooks/proxmox-backup.md": "Настройка backup для Proxmox через ZFS snapshot на сервере loki. " +
			"Расписание backup ежедневное, retention 30 дней.",
		"extra:vault/Dev/Areas/maestro/routing.md":     "Maestro routing config: backend claude, router_model claude.",
		"extra:vault/Personal/Recipes/borsch.md":       "Рецепт борща: свёкла, капуста, мясной бульон.",
		"extra:vault/HomeLab/Resources/networking.md":  "Настройка VLAN на сервере, порты и firewall.",
		"extra:other-collection/notes/proxmox-tips.md": "Proxmox backup tips from another collection.",
	}
}

// A natural-language Russian question must find the right note. As a literal
// string the question occurs in no note at all, which is exactly why the old
// strings.Contains implementation scored zero on real questions.
func TestVaultSearchAnswersRussianNaturalLanguageQuestion(t *testing.T) {
	const question = "Как я настраивал backup для Proxmox на сервере?"

	chunks := russianVaultChunks()
	for source, content := range chunks {
		if strings.Contains(content, question) {
			t.Fatalf("precondition broken: %s contains the question verbatim", source)
		}
	}

	index := &fakeVaultIndex{chunks: chunks, available: true}
	tool := NewObsidianTool(t.TempDir()).WithIndex(index, "extra:vault/")

	result, err := tool.SearchNotes(context.Background(), question, DefaultVaultSearchLimit)
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	if len(result.Hits) == 0 {
		t.Fatal("Russian natural-language question returned no notes")
	}

	want := "HomeLab/Resources/Runbooks/proxmox-backup"
	if result.Hits[0].Note != want {
		t.Fatalf("top hit = %q, want %q (hits: %v)", result.Hits[0].Note, want, result.Notes())
	}
	if len(result.Hits[0].MatchedTerms) < 2 {
		t.Fatalf("top hit matched %v, expected at least two distinct terms", result.Hits[0].MatchedTerms)
	}

	// Stopwords must never reach the index.
	for _, q := range index.queries {
		if memory.IsQueryStopword(q) {
			t.Errorf("stopword %q was searched", q)
		}
	}
	// The whole question must never be sent as one literal string.
	for _, q := range index.queries {
		if strings.Contains(q, " ") {
			t.Errorf("query %q was not decomposed into single terms", q)
		}
	}
}

// Silent degradation is the root cause of the symptoms this fix addresses:
// an index that cannot serve keyword queries must produce an error, never an
// empty result set that reads like "no such note".
func TestVaultSearchFailsLoudlyWhenLexicalIndexUnavailable(t *testing.T) {
	index := &fakeVaultIndex{chunks: russianVaultChunks(), available: false}
	tool := NewObsidianTool(t.TempDir()).WithIndex(index, "extra:vault/")

	result, err := tool.SearchNotes(context.Background(), "Как я настраивал backup для Proxmox?", DefaultVaultSearchLimit)
	if err == nil {
		t.Fatalf("expected an explicit error, got %d hits", len(result.Hits))
	}
	for _, want := range []string{"lexical index is unavailable", "sqlite_fts5"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// The same failure must surface through the tool-call surfaces too.
	if _, err := tool.Execute(context.Background(), "search", "backup Proxmox"); err == nil {
		t.Error("Execute swallowed the unavailable index")
	}
	if _, err := tool.ExecuteJSON(context.Background(), map[string]string{"operation": "search", "query": "backup Proxmox"}); err == nil {
		t.Error("ExecuteJSON swallowed the unavailable index")
	}
}

func TestVaultSearchFailsLoudlyWhenVaultIsNotIndexed(t *testing.T) {
	index := &fakeVaultIndex{chunks: map[string]string{}, available: true}
	tool := NewObsidianTool(t.TempDir()).WithIndex(index, "extra:vault/")

	if _, err := tool.SearchNotes(context.Background(), "backup Proxmox", DefaultVaultSearchLimit); err == nil {
		t.Fatal("expected an error for an unindexed vault")
	} else if !strings.Contains(err.Error(), "extra_paths") {
		t.Errorf("error %q should name the fix (memory.extra_paths)", err)
	}
}

func TestVaultSearchPropagatesIndexErrors(t *testing.T) {
	index := &fakeVaultIndex{
		chunks:    russianVaultChunks(),
		available: true,
		searchErr: fmt.Errorf("database is locked"),
	}
	tool := NewObsidianTool(t.TempDir()).WithIndex(index, "extra:vault/")

	if _, err := tool.SearchNotes(context.Background(), "backup Proxmox", DefaultVaultSearchLimit); err == nil {
		t.Fatal("expected the index error to surface")
	} else if !strings.Contains(err.Error(), "database is locked") {
		t.Errorf("error %q lost the underlying cause", err)
	}
}

// Ranking is by distinct term coverage, not by raw hit count.
func TestVaultSearchRanksByDistinctTermCoverage(t *testing.T) {
	index := &fakeVaultIndex{
		available: true,
		chunks: map[string]string{
			"extra:vault/covers-three.md": "backup proxmox zfs",
			"extra:vault/covers-one.md":   "backup backup backup backup backup",
			"extra:vault/covers-two.md":   "backup zfs",
		},
	}
	tool := NewObsidianTool(t.TempDir()).WithIndex(index, "extra:vault/")

	result, err := tool.SearchNotes(context.Background(), "как сделать backup proxmox на zfs", DefaultVaultSearchLimit)
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	want := []string{"covers-three", "covers-two", "covers-one"}
	got := result.Notes()
	if len(got) != len(want) {
		t.Fatalf("notes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("notes = %v, want %v", got, want)
		}
	}
}

// Results must stay inside the collection that holds this vault.
func TestVaultSearchScopesResultsToVaultCollection(t *testing.T) {
	index := &fakeVaultIndex{chunks: russianVaultChunks(), available: true}
	tool := NewObsidianTool(t.TempDir()).WithIndex(index, "extra:vault/")

	result, err := tool.SearchNotes(context.Background(), "Proxmox backup", DefaultVaultSearchLimit)
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	if len(result.Hits) == 0 {
		t.Fatal("no hits")
	}
	for _, note := range result.Notes() {
		if strings.Contains(note, "other-collection") || strings.Contains(note, "extra:") {
			t.Errorf("hit %q escaped the vault collection", note)
		}
	}
}

// Without a retrieval store the tool still decomposes the query; it must not
// regress to matching the whole question as one literal string.
func TestVaultSearchFilesystemScanAnswersRussianQuestion(t *testing.T) {
	vault := t.TempDir()
	files := map[string]string{
		"HomeLab/Runbooks/proxmox-backup.md": "# Proxmox backup\n\nНастройка backup через ZFS snapshot на сервере loki.\n",
		"Personal/Recipes/borsch.md":         "# Борщ\n\nСвёкла, капуста, бульон.\n",
		"Dev/Areas/maestro/routing.md":       "# Routing\n\nMaestro backend claude.\n",
	}
	for rel, content := range files {
		full := filepath.Join(vault, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tool := NewObsidianTool(vault)
	const question = "Как я настраивал backup для Proxmox на сервере?"

	result, err := tool.SearchNotes(context.Background(), question, DefaultVaultSearchLimit)
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	if len(result.Hits) == 0 {
		t.Fatal("filesystem scan returned nothing for a natural-language question")
	}
	if want := "HomeLab/Runbooks/proxmox-backup"; result.Hits[0].Note != want {
		t.Fatalf("top hit = %q, want %q (hits: %v)", result.Hits[0].Note, want, result.Notes())
	}
	if !strings.Contains(result.Backend, "filesystem") {
		t.Errorf("backend = %q, expected it to name the filesystem scan", result.Backend)
	}
}

// The rendered output must always disclose which backend ran and which terms
// were searched, including when nothing matched.
func TestVaultSearchFormatDisclosesBackendAndTerms(t *testing.T) {
	index := &fakeVaultIndex{chunks: russianVaultChunks(), available: true}
	tool := NewObsidianTool(t.TempDir()).WithIndex(index, "extra:vault/")

	out, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"operation": "search",
		"query":     "совершенно несуществующая тема",
	})
	if err != nil {
		t.Fatalf("ExecuteJSON: %v", err)
	}
	for _, want := range []string{"Vault search via", "Terms searched:", "No notes matched"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
	if strings.Contains(out, " как,") || strings.Contains(out, "Terms searched: как") {
		t.Errorf("stopwords leaked into the searched terms: %q", out)
	}
}

func TestVaultSearchRejectsStopwordOnlyNoise(t *testing.T) {
	tool := NewObsidianTool(t.TempDir())
	if _, err := tool.SearchNotes(context.Background(), "?!  ...", DefaultVaultSearchLimit); err == nil {
		t.Fatal("expected an error for a query with no searchable terms")
	}
}

func TestVaultIndexPrefixFor(t *testing.T) {
	extras := []memory.ExtraPath{
		{Name: "obsidian", Path: "/home/u/Vault"},
		{Name: "homelab", Path: "/home/u/HomeLab"},
	}
	if got := vaultIndexPrefixFor(extras, "/home/u/Vault"); got != "extra:obsidian/" {
		t.Errorf("prefix = %q, want %q", got, "extra:obsidian/")
	}
	if got := vaultIndexPrefixFor(extras, "/home/u/Vault/"); got != "extra:obsidian/" {
		t.Errorf("trailing-slash prefix = %q, want %q", got, "extra:obsidian/")
	}
	if got := vaultIndexPrefixFor(extras, "/home/u/Elsewhere"); got != "" {
		t.Errorf("unconfigured vault prefix = %q, want empty", got)
	}
}
