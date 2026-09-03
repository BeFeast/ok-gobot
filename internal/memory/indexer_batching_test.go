package memory

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// contextCappedEmbedder mimics a provider with a per-request context window:
// any request whose estimated tokens exceed maxTokens, or that contains a
// text from refuse, is rejected with the live Cloudflare 400 body.
type contextCappedEmbedder struct {
	mu        sync.Mutex
	maxTokens int
	refuse    map[string]bool
	requests  [][]string
}

func (e *contextCappedEmbedder) GetEmbeddings(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.requests = append(e.requests, append([]string(nil), texts...))
	e.mu.Unlock()

	tokens := 0
	for _, text := range texts {
		tokens += EstimateEmbeddingTokens(text)
		if e.refuse[text] {
			return nil, &EmbeddingAPIError{StatusCode: http.StatusBadRequest, Body: `{"errors":[{"code":3030,"message":"Sequence too long: 9000 > 8192"}]}`}
		}
	}
	if e.maxTokens > 0 && tokens > e.maxTokens {
		return nil, &EmbeddingAPIError{
			StatusCode: http.StatusBadRequest,
			Body:       fmt.Sprintf(`{"errors":[{"code":3030,"message":"AiError: Max context reached %d tokens but model supports only %d"}]}`, tokens, e.maxTokens),
		}
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(len(texts[i])), float32(i + 1)}
	}
	return out, nil
}

func (e *contextCappedEmbedder) snapshot() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]string, len(e.requests))
	for i := range e.requests {
		out[i] = append([]string(nil), e.requests[i]...)
	}
	return out
}

type capturingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *capturingLogger) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *capturingLogger) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// writeWordsFile writes one whitespace-separated "word" per chunk; with
// WithIndexerChunking(1, 0) every word becomes its own chunk.
func writeWordsFile(t *testing.T, dir string, words []string) string {
	t.Helper()
	path := filepath.Join(dir, "memory.txt")
	if err := os.WriteFile(path, []byte(strings.Join(words, "\n")), 0o644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}
	return path
}

func newBatchingTestStore(t *testing.T) (*sql.DB, *MemoryStore) {
	t.Helper()
	db := openIndexerTestDB(t)
	t.Cleanup(func() { db.Close() }) //nolint:errcheck
	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}
	return db, store
}

func loadEmbeddingSizes(t *testing.T, db *sql.DB, sourceFile string) map[string]int {
	t.Helper()
	rows, err := db.Query(`SELECT header_path || '#' || chunk_ordinal, length(embedding) FROM memory_chunks WHERE source_file = ?`, sourceFile)
	if err != nil {
		t.Fatalf("query chunks failed: %v", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var (
			key  string
			size sql.NullInt64
		)
		if err := rows.Scan(&key, &size); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		out[key] = int(size.Int64)
	}
	return out
}

func TestIndexerBatchesRespectCountCapAndTokenBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		wordRunes   int
		words       int
		wantBatches []int
	}{
		// 40 tiny chunks: only the 32-element cap matters.
		{name: "count cap", wordRunes: 10, words: 40, wantBatches: []int{32, 8}},
		// 40 chunks of 10 000 Latin runes ≈ 2 858 tokens each: 15 fit under the
		// 45 000-token budget, so the fixed 32-chunk batch (91k tokens) is
		// exactly what the provider rejected in production.
		{name: "token budget", wordRunes: 10_000, words: 40, wantBatches: []int{15, 15, 10}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, store := newBatchingTestStore(t)
			words := make([]string, tc.words)
			for i := range words {
				words[i] = strings.Repeat(string(rune('a'+i%26)), tc.wordRunes)
			}
			path := writeWordsFile(t, t.TempDir(), words)

			embedder := &contextCappedEmbedder{}
			indexer := NewIndexer(filepath.Dir(path), store, embedder, WithIndexerChunking(1, 0))
			if err := indexer.IndexFile(context.Background(), path, "memory.txt"); err != nil {
				t.Fatalf("IndexFile failed: %v", err)
			}

			requests := embedder.snapshot()
			got := make([]int, len(requests))
			for i, req := range requests {
				got[i] = len(req)
				tokens := 0
				for _, text := range req {
					tokens += EstimateEmbeddingTokens(text)
				}
				if tokens > embeddingRequestTokenBudget {
					t.Fatalf("request %d carries %d estimated tokens > budget %d", i, tokens, embeddingRequestTokenBudget)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.wantBatches) {
				t.Fatalf("batch sizes = %v, want %v", got, tc.wantBatches)
			}
		})
	}
}

func TestIndexerTruncatesOversizeChunkButStoresFullContent(t *testing.T) {
	t.Parallel()

	db, store := newBatchingTestStore(t)
	// 30 000 Latin runes ≈ 8 572 tokens: over the 8 192 per-input provider
	// cap (the 179k-char vault chunk in production was the extreme case).
	oversize := strings.Repeat("a", 30_000)
	path := writeWordsFile(t, t.TempDir(), []string{oversize, "tail"})

	embedder := &contextCappedEmbedder{}
	logs := &capturingLogger{}
	indexer := NewIndexer(filepath.Dir(path), store, embedder, WithIndexerChunking(1, 0), WithIndexerLogger(logs.logf))
	if err := indexer.IndexFile(context.Background(), path, "memory.txt"); err != nil {
		t.Fatalf("IndexFile failed: %v", err)
	}

	requests := embedder.snapshot()
	if len(requests) != 1 || len(requests[0]) != 2 {
		t.Fatalf("expected one request with two inputs, got %v", requests)
	}
	if n := utf8.RuneCountInString(requests[0][0]); n != 21_000 {
		t.Fatalf("embedder received %d runes, want the 21000-rune head", n)
	}
	if requests[0][1] != "tail" {
		t.Fatalf("second input altered: %q", requests[0][1])
	}

	var stored string
	if err := db.QueryRow(`SELECT content FROM memory_chunks WHERE source_file = ? AND chunk_ordinal = 0`, "memory.txt").Scan(&stored); err != nil {
		t.Fatalf("query stored content failed: %v", err)
	}
	if stored != oversize {
		t.Fatalf("stored content was truncated to %d runes; the DB must keep the full chunk", utf8.RuneCountInString(stored))
	}
	sizes := loadEmbeddingSizes(t, db, "memory.txt")
	if sizes["root#0"] == 0 {
		t.Fatalf("oversize chunk should carry a head-only vector, got empty embedding")
	}
	if logged := logs.joined(); !strings.Contains(logged, "embedding input truncated file=memory.txt chunk=root#0 runes=30000") {
		t.Fatalf("expected a truncation log line, got: %q", logged)
	}
}

func TestIndexerSplitsRejectedBatchAndPersistsEveryChunk(t *testing.T) {
	t.Parallel()

	db, store := newBatchingTestStore(t)
	// Eight 200-rune chunks ≈ 58 tokens each. The provider window of 120
	// tokens rejects the full batch (464) and the halves (232); pairs pass.
	// Chunk w5 is refused on its own as well, so it must end up lexical-only
	// while the other seven keep their vectors — the file is never abandoned.
	words := make([]string, 8)
	for i := range words {
		words[i] = fmt.Sprintf("w%d", i) + strings.Repeat("x", 198)
	}
	path := writeWordsFile(t, t.TempDir(), words)

	embedder := &contextCappedEmbedder{maxTokens: 120, refuse: map[string]bool{words[5]: true}}
	logs := &capturingLogger{}
	indexer := NewIndexer(filepath.Dir(path), store, embedder, WithIndexerChunking(1, 0), WithIndexerLogger(logs.logf))
	if err := indexer.IndexFile(context.Background(), path, "memory.txt"); err != nil {
		t.Fatalf("IndexFile must survive a rejected batch, got: %v", err)
	}

	sizes := loadEmbeddingSizes(t, db, "memory.txt")
	if len(sizes) != 8 {
		t.Fatalf("persisted %d chunks, want all 8: %v", len(sizes), sizes)
	}
	for key, size := range sizes {
		switch key {
		case "root#5":
			if size != 0 {
				t.Fatalf("refused chunk %s should be stored with an empty embedding, got %d bytes", key, size)
			}
		default:
			if size == 0 {
				t.Fatalf("chunk %s lost its embedding: %v", key, sizes)
			}
		}
	}

	for _, req := range embedder.snapshot() {
		tokens := 0
		for _, text := range req {
			tokens += EstimateEmbeddingTokens(text)
		}
		if tokens > 120 && len(req) > 1 {
			// Expected while splitting: 8 → 4 → 2. Only single-input
			// requests may still exceed the window.
			if len(req) != 8 && len(req) != 4 {
				t.Fatalf("unexpected oversize request of %d inputs", len(req))
			}
		}
	}
	if logged := logs.joined(); !strings.Contains(logged, "lexical-only file=memory.txt chunk=root#5") {
		t.Fatalf("expected a lexical-only log line for chunk 5, got: %q", logged)
	}
}

func TestIndexerStillFailsFileOnNonSizeErrors(t *testing.T) {
	t.Parallel()

	db, store := newBatchingTestStore(t)
	path := writeWordsFile(t, t.TempDir(), []string{"alpha", "beta"})

	embedder := &staticErrorEmbedder{err: &EmbeddingAPIError{StatusCode: http.StatusUnauthorized, Body: `{"error":"invalid api key"}`}}
	indexer := NewIndexer(filepath.Dir(path), store, embedder, WithIndexerChunking(1, 0))
	err := indexer.IndexFile(context.Background(), path, "memory.txt")
	if err == nil || !strings.Contains(err.Error(), "embed batch 0-2") {
		t.Fatalf("expected the auth failure to abort the file, got %v", err)
	}
	if count := countSourceChunks(t, db, "memory.txt"); count != 0 {
		t.Fatalf("no chunks should be persisted after a hard error, got %d", count)
	}
}

type staticErrorEmbedder struct{ err error }

func (e *staticErrorEmbedder) GetEmbeddings(context.Context, []string) ([][]float32, error) {
	return nil, e.err
}
