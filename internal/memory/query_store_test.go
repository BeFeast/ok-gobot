package memory

import (
	"context"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// End-to-end over a real SQLite store. It runs on both lexical legs: with the
// sqlite_fts5 build tag it exercises FTS5/BM25, without it the LIKE fallback.
// Both must answer a Russian natural-language question, because the shipped
// binary has historically been built without the tag.
func TestStoreLexicalSearchAnswersRussianQuestion(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	chunks := []struct {
		source  string
		header  string
		content string
	}{
		{"extra:vault/HomeLab/proxmox-backup.md", "Backup",
			"Настройка backup для Proxmox через ZFS snapshot на сервере loki. Retention 30 дней."},
		{"extra:vault/Personal/borsch.md", "Рецепт",
			"Рецепт борща: свёкла, капуста, мясной бульон, сметана."},
		{"extra:vault/Dev/maestro-routing.md", "Routing",
			"Maestro routing: backend claude, router_model claude, escalation policy."},
		{"extra:vault/HomeLab/networking.md", "Network",
			"Настройка VLAN на сервере, порты, firewall и статические маршруты."},
	}
	for i, c := range chunks {
		if err := store.IndexChunk(ctx, c.source, c.header, i+1, i+1, c.content, nil); err != nil {
			t.Fatalf("IndexChunk %s: %v", c.source, err)
		}
	}

	if count, err := store.CountChunks(ctx, "extra:vault/"); err != nil || count != len(chunks) {
		t.Fatalf("CountChunks = %d, %v; want %d", count, err, len(chunks))
	}
	if count, err := store.CountChunks(ctx, "extra:missing/"); err != nil || count != 0 {
		t.Fatalf("CountChunks(missing) = %d, %v; want 0", count, err)
	}

	const question = "Как я настраивал backup для Proxmox на сервере?"
	results, err := store.SearchText(ctx, question, 5)
	if err != nil {
		t.Fatalf("SearchText: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("Russian question returned nothing (fts5 available: %v)", store.LexicalIndexAvailable())
	}
	if want := "extra:vault/HomeLab/proxmox-backup.md"; results[0].SourceFile != want {
		t.Fatalf("top hit = %q, want %q (fts5: %v)", results[0].SourceFile, want, store.LexicalIndexAvailable())
	}

	// The whole question as a literal string matches nothing — the failure
	// mode the old filesystem search reproduced on every real query.
	for _, c := range chunks {
		if strings.Contains(c.content, question) {
			t.Fatalf("fixture %s contains the question verbatim", c.source)
		}
	}
}

// The LIKE fallback is the production lexical engine whenever FTS5 is missing,
// so it must rank by term coverage rather than by recency.
func TestLikeFallbackRanksByTermCoverageNotRecency(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	store.ftsAvailable = false

	// Indexed in this order, so "newest" is the low-coverage chunk.
	if err := store.IndexChunk(ctx, "old-but-relevant.md", "root", 1, 1,
		"backup proxmox zfs snapshot retention", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.IndexChunk(ctx, "new-but-irrelevant.md", "root", 1, 1,
		"backup of the coffee machine settings", nil); err != nil {
		t.Fatal(err)
	}

	results, err := store.SearchText(ctx, "как сделать backup proxmox на zfs", 5)
	if err != nil {
		t.Fatalf("SearchText: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected both chunks, got %d", len(results))
	}
	if want := "old-but-relevant.md"; results[0].SourceFile != want {
		t.Fatalf("top hit = %q, want %q — LIKE fallback is still ranking by recency", results[0].SourceFile, want)
	}
}

// `memory status` must disclose whether lexical search has a real index. This
// is the operator-facing half of failing loudly: the FTS5 module has been
// absent from shipped binaries with no visible symptom other than bad results.
func TestStatusReportsLexicalIndexAvailability(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	status, err := CollectStatus(ctx, store, StatusOptions{Enabled: true, RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("CollectStatus: %v", err)
	}
	want := LexicalIndexFTS5
	if !store.LexicalIndexAvailable() {
		want = LexicalIndexUnavailable
	}
	if status.LexicalIndex != want {
		t.Errorf("LexicalIndex = %q, want %q", status.LexicalIndex, want)
	}
	if out := FormatStatusCLI(status); !strings.Contains(out, "Lexical index:") {
		t.Errorf("CLI status does not report the lexical index:\n%s", out)
	}

	// A degraded index must be spelled out, with the fix named.
	store.ftsAvailable = false
	degraded, err := CollectStatus(ctx, store, StatusOptions{Enabled: true, RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("CollectStatus degraded: %v", err)
	}
	if !strings.Contains(degraded.LexicalIndex, "sqlite_fts5") {
		t.Errorf("degraded status %q does not name the build tag", degraded.LexicalIndex)
	}
	if out := FormatStatusTelegram(degraded); !strings.Contains(out, "sqlite_fts5") {
		t.Errorf("Telegram status hides the degradation:\n%s", out)
	}
}
