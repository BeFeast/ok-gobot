package memory

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"

	_ "github.com/mattn/go-sqlite3"
)

type retrievalEvalMode string

const (
	retrievalEvalLexical retrievalEvalMode = "lexical"
	retrievalEvalVector  retrievalEvalMode = "vector"
	retrievalEvalHybrid  retrievalEvalMode = "hybrid"
	retrievalEvalQMD     retrievalEvalMode = "qmd"
)

type retrievalEvalQuery struct {
	Name     string
	Mode     retrievalEvalMode
	Query    string
	TopK     int
	Expected []retrievalExpectedChunk
}

type retrievalExpectedChunk struct {
	SourceFile   string
	HeaderPath   string
	ChunkOrdinal int
}

type retrievalEvalHit struct {
	SourceFile   string
	HeaderPath   string
	ChunkOrdinal int
	Content      string
	Score        float32
}

type retrievalEvalChunk struct {
	SourceFile   string
	HeaderPath   string
	ChunkOrdinal int
	Content      string
	Embedding    []float32
}

type retrievalEvalHarness struct {
	store    *MemoryStore
	embedder *retrievalEvalEmbedder
	chunks   []retrievalEvalChunk
}

func TestMemoryRetrievalEvaluationHarness(t *testing.T) {
	ctx := context.Background()
	harness := newRetrievalEvalHarness(t, ctx)
	queries := retrievalEvalQueries()

	if len(queries) < 8 {
		t.Fatalf("retrieval evaluation query count = %d, want at least 8", len(queries))
	}

	report, missed := harness.Evaluate(ctx, queries)
	if missed > 0 {
		t.Fatalf("memory retrieval evaluation missed %d expected source(s):\n%s", missed, report)
	}
	t.Logf("memory retrieval evaluation passed:\n%s", report)
}

func retrievalEvalQueries() []retrievalEvalQuery {
	return []retrievalEvalQuery{
		{
			Name:  "exact-lexical-release-codename",
			Mode:  retrievalEvalLexical,
			Query: "capybara release codename",
			TopK:  3,
			Expected: []retrievalExpectedChunk{{
				SourceFile:   "MEMORY.md",
				HeaderPath:   "Memory > Projects > Ok Gobot > Release Facts",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "exact-lexical-user-preference",
			Mode:  retrievalEvalLexical,
			Query: "Morgan prefers espresso planning reviews",
			TopK:  3,
			Expected: []retrievalExpectedChunk{{
				SourceFile:   "MEMORY.md",
				HeaderPath:   "Memory > Projects > Ok Gobot > User Preferences",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "semantic-vector-image-preview-latency",
			Mode:  retrievalEvalVector,
			Query: "How can image previews load faster through a CDN?",
			TopK:  3,
			Expected: []retrievalExpectedChunk{{
				SourceFile:   "memory/2026-04-10.md",
				HeaderPath:   "Daily Memory: 2026-04-10 > Decisions",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "semantic-vector-login-credentials",
			Mode:  retrievalEvalVector,
			Query: "login outage from old credentials",
			TopK:  3,
			Expected: []retrievalExpectedChunk{{
				SourceFile:   "memory/2026-04-11.md",
				HeaderPath:   "Daily Memory: 2026-04-11 > Incidents",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "transcript-lexical-parser-blocker",
			Mode:  retrievalEvalLexical,
			Query: "standup transcript slash escaping command parser blocker",
			TopK:  3,
			Expected: []retrievalExpectedChunk{{
				SourceFile:   "transcripts/2026-04-12-standup.md",
				HeaderPath:   "Transcript > Standup Snippet",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "external-path-hybrid-phoenix-runbook",
			Mode:  retrievalEvalHybrid,
			Query: "Phoenix deploy rollback migrations runbook",
			TopK:  3,
			Expected: []retrievalExpectedChunk{{
				SourceFile:   "extra/project-phoenix-runbook.md",
				HeaderPath:   "Phoenix Runbook > Deployment",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "daily-note-lexical-accessibility-reminder",
			Mode:  retrievalEvalLexical,
			Query: "Friday accessibility audit contrast keyboard focus",
			TopK:  3,
			Expected: []retrievalExpectedChunk{{
				SourceFile:   "memory/2026-04-10.md",
				HeaderPath:   "Daily Memory: 2026-04-10 > Reminders",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "qmd-lexical-retrieval-coverage",
			Mode:  retrievalEvalQMD,
			Query: "quarto notebook recall hybrid retrieval qmd",
			TopK:  3,
			Expected: []retrievalExpectedChunk{{
				SourceFile:   "research/phoenix-eval.qmd",
				HeaderPath:   "Research > Phoenix QMD Plan",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "offline-policy-lexical-deterministic",
			Mode:  retrievalEvalLexical,
			Query: "deterministic offline embeddings remote model downloads",
			TopK:  3,
			Expected: []retrievalExpectedChunk{{
				SourceFile:   "MEMORY.md",
				HeaderPath:   "Memory > Projects > Ok Gobot > Offline Policy",
				ChunkOrdinal: 0,
			}},
		},
	}
}

func newRetrievalEvalHarness(t *testing.T, ctx context.Context) *retrievalEvalHarness {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite db failed: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	fixtureRoot, err := filepath.Abs(filepath.Join("testdata", "retrieval_eval"))
	if err != nil {
		t.Fatalf("resolve fixture root failed: %v", err)
	}

	embedder := &retrievalEvalEmbedder{}
	indexer := NewIndexer(fixtureRoot, store, embedder, WithIndexerChunking(256, 0))
	for _, path := range retrievalEvalFixturePaths(t, fixtureRoot) {
		rel, err := filepath.Rel(fixtureRoot, path)
		if err != nil {
			t.Fatalf("rel fixture path failed for %s: %v", path, err)
		}
		if err := indexer.IndexFile(ctx, path, filepath.ToSlash(rel)); err != nil {
			t.Fatalf("index fixture %s failed: %v", filepath.ToSlash(rel), err)
		}
	}

	chunks := loadRetrievalEvalChunks(t, ctx, db)
	if len(chunks) == 0 {
		t.Fatal("retrieval evaluation fixtures produced no indexed chunks")
	}

	return &retrievalEvalHarness{
		store:    store,
		embedder: embedder,
		chunks:   chunks,
	}
}

func retrievalEvalFixturePaths(t *testing.T, root string) []string {
	t.Helper()

	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".md", ".qmd":
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk retrieval eval fixtures failed: %v", err)
	}
	sort.Strings(paths)
	return paths
}

func loadRetrievalEvalChunks(t *testing.T, ctx context.Context, db *sql.DB) []retrievalEvalChunk {
	t.Helper()

	rows, err := db.QueryContext(ctx, `
		SELECT source_file, header_path, chunk_ordinal, content, embedding
		FROM memory_chunks
		ORDER BY source_file, header_path, chunk_ordinal
	`)
	if err != nil {
		t.Fatalf("query retrieval eval chunks failed: %v", err)
	}
	defer rows.Close()

	var chunks []retrievalEvalChunk
	for rows.Next() {
		var chunk retrievalEvalChunk
		var rawEmbedding []byte
		if err := rows.Scan(&chunk.SourceFile, &chunk.HeaderPath, &chunk.ChunkOrdinal, &chunk.Content, &rawEmbedding); err != nil {
			t.Fatalf("scan retrieval eval chunk failed: %v", err)
		}
		chunk.Embedding, err = decodeEmbedding(rawEmbedding)
		if err != nil {
			t.Fatalf("decode retrieval eval embedding for %s: %v", chunk.SourceFile, err)
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate retrieval eval chunks failed: %v", err)
	}
	return chunks
}

func (h *retrievalEvalHarness) Evaluate(ctx context.Context, queries []retrievalEvalQuery) (string, int) {
	var details strings.Builder
	totalExpected := 0
	totalHits := 0

	for _, query := range queries {
		hits, err := h.runQuery(ctx, query)
		if err != nil {
			fmt.Fprintf(&details, "FAIL %s [%s@%d] error: %v\n", query.Name, query.Mode, query.TopK, err)
			totalExpected += len(query.Expected)
			continue
		}

		missed := missedExpectedChunks(query.Expected, hits)
		hitCount := len(query.Expected) - len(missed)
		totalExpected += len(query.Expected)
		totalHits += hitCount

		status := "PASS"
		if len(missed) > 0 {
			status = "FAIL"
		}
		fmt.Fprintf(
			&details,
			"%s %s [%s@%d] recall@%d=%d/%d\n",
			status,
			query.Name,
			query.Mode,
			query.TopK,
			query.TopK,
			hitCount,
			len(query.Expected),
		)
		if len(missed) > 0 {
			details.WriteString("  missed:\n")
			for _, expected := range missed {
				fmt.Fprintf(&details, "    - %s\n", expected.String())
			}
		}
		details.WriteString("  expected:\n")
		for _, expected := range query.Expected {
			fmt.Fprintf(&details, "    - %s\n", expected.String())
		}
		details.WriteString("  hits:\n")
		if len(hits) == 0 {
			details.WriteString("    - none\n")
		}
		for idx, hit := range hits {
			fmt.Fprintf(&details, "    - %d. %s score=%.4f %q\n", idx+1, hit.String(), hit.Score, summarizeRetrievalEvalContent(hit.Content))
		}
	}

	missed := totalExpected - totalHits
	summary := fmt.Sprintf("overall recall=%d/%d missed=%d", totalHits, totalExpected, missed)
	return summary + "\n" + strings.TrimRight(details.String(), "\n"), missed
}

func (h *retrievalEvalHarness) runQuery(ctx context.Context, query retrievalEvalQuery) ([]retrievalEvalHit, error) {
	topK := query.TopK
	if topK <= 0 {
		topK = 3
	}

	switch query.Mode {
	case retrievalEvalLexical:
		return h.lexicalSearch(query.Query, topK, nil), nil
	case retrievalEvalVector:
		return h.vectorSearch(ctx, query.Query, topK)
	case retrievalEvalHybrid:
		return h.hybridSearch(query.Query, topK), nil
	case retrievalEvalQMD:
		return h.lexicalSearch(query.Query, topK, func(chunk retrievalEvalChunk) bool {
			return strings.EqualFold(filepath.Ext(chunk.SourceFile), ".qmd")
		}), nil
	default:
		return nil, fmt.Errorf("unknown retrieval eval mode %q", query.Mode)
	}
}

func (h *retrievalEvalHarness) lexicalSearch(query string, topK int, include func(retrievalEvalChunk) bool) []retrievalEvalHit {
	queryTokens := uniqueRetrievalEvalTokens(query)
	if len(queryTokens) == 0 {
		return nil
	}

	var hits []retrievalEvalHit
	for _, chunk := range h.chunks {
		if include != nil && !include(chunk) {
			continue
		}
		score := lexicalRetrievalEvalScore(queryTokens, chunk.Content)
		if score <= 0 {
			continue
		}
		hits = append(hits, chunk.hit(score))
	}
	return topRetrievalEvalHits(hits, topK)
}

func (h *retrievalEvalHarness) vectorSearch(ctx context.Context, query string, topK int) ([]retrievalEvalHit, error) {
	results, err := h.store.SearchChunks(ctx, h.embedder.embed(query), topK)
	if err != nil {
		return nil, err
	}

	hits := make([]retrievalEvalHit, 0, len(results))
	for _, result := range results {
		hits = append(hits, retrievalEvalHit{
			SourceFile:   result.SourceFile,
			HeaderPath:   result.HeaderPath,
			ChunkOrdinal: result.ChunkOrdinal,
			Content:      result.Content,
			Score:        result.Similarity,
		})
	}
	return hits, nil
}

func (h *retrievalEvalHarness) hybridSearch(query string, topK int) []retrievalEvalHit {
	queryTokens := uniqueRetrievalEvalTokens(query)
	queryEmbedding := h.embedder.embed(query)

	hits := make([]retrievalEvalHit, 0, len(h.chunks))
	for _, chunk := range h.chunks {
		lexicalScore := lexicalRetrievalEvalScore(queryTokens, chunk.Content)
		vectorScore := cosineSimilarity(queryEmbedding, chunk.Embedding)
		if vectorScore < 0 {
			vectorScore = 0
		}
		score := lexicalScore + vectorScore
		if score <= 0 {
			continue
		}
		hits = append(hits, chunk.hit(score))
	}
	return topRetrievalEvalHits(hits, topK)
}

func (c retrievalEvalChunk) hit(score float32) retrievalEvalHit {
	return retrievalEvalHit{
		SourceFile:   c.SourceFile,
		HeaderPath:   c.HeaderPath,
		ChunkOrdinal: c.ChunkOrdinal,
		Content:      c.Content,
		Score:        score,
	}
}

func topRetrievalEvalHits(hits []retrievalEvalHit, topK int) []retrievalEvalHit {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].String() < hits[j].String()
	})
	if len(hits) > topK {
		return hits[:topK]
	}
	return hits
}

func missedExpectedChunks(expected []retrievalExpectedChunk, hits []retrievalEvalHit) []retrievalExpectedChunk {
	var missed []retrievalExpectedChunk
	for _, want := range expected {
		found := false
		for _, hit := range hits {
			if hit.SourceFile == want.SourceFile && hit.HeaderPath == want.HeaderPath && hit.ChunkOrdinal == want.ChunkOrdinal {
				found = true
				break
			}
		}
		if !found {
			missed = append(missed, want)
		}
	}
	return missed
}

func lexicalRetrievalEvalScore(queryTokens map[string]struct{}, content string) float32 {
	contentTokens := uniqueRetrievalEvalTokens(content)
	if len(queryTokens) == 0 || len(contentTokens) == 0 {
		return 0
	}

	matches := 0
	for token := range queryTokens {
		if _, ok := contentTokens[token]; ok {
			matches++
		}
	}
	return float32(matches) / float32(len(queryTokens))
}

func uniqueRetrievalEvalTokens(text string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, token := range retrievalEvalTokens(text) {
		if _, stop := retrievalEvalStopwords[token]; stop {
			continue
		}
		out[token] = struct{}{}
	}
	return out
}

func retrievalEvalTokens(text string) []string {
	var normalized strings.Builder
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			normalized.WriteRune(r)
			continue
		}
		normalized.WriteByte(' ')
	}
	return strings.Fields(normalized.String())
}

func summarizeRetrievalEvalContent(content string) string {
	content = strings.Join(strings.Fields(content), " ")
	if len(content) <= 96 {
		return content
	}
	return content[:93] + "..."
}

func (e retrievalExpectedChunk) String() string {
	return fmt.Sprintf("%s :: %s#%d", e.SourceFile, e.HeaderPath, e.ChunkOrdinal)
}

func (h retrievalEvalHit) String() string {
	return fmt.Sprintf("%s :: %s#%d", h.SourceFile, h.HeaderPath, h.ChunkOrdinal)
}

type retrievalEvalEmbedder struct{}

func (e *retrievalEvalEmbedder) GetEmbeddings(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = e.embed(text)
	}
	return out, nil
}

func (e *retrievalEvalEmbedder) embed(text string) []float32 {
	vector := make([]float32, retrievalEvalVectorDimensions)
	for _, token := range retrievalEvalTokens(text) {
		for _, weight := range retrievalEvalVectorWeights[token] {
			vector[weight.dimension] += weight.value
		}
	}

	var norm float64
	for _, value := range vector {
		norm += float64(value * value)
	}
	if norm == 0 {
		vector[retrievalEvalDimFallback] = 1
	}
	return vector
}

type retrievalEvalVectorWeight struct {
	dimension int
	value     float32
}

const (
	retrievalEvalDimCodename = iota
	retrievalEvalDimCoffee
	retrievalEvalDimCaching
	retrievalEvalDimAccessibility
	retrievalEvalDimBilling
	retrievalEvalDimAuth
	retrievalEvalDimTranscript
	retrievalEvalDimDeployment
	retrievalEvalDimQMD
	retrievalEvalDimOffline
	retrievalEvalDimFallback
	retrievalEvalVectorDimensions
)

var retrievalEvalVectorWeights = map[string][]retrievalEvalVectorWeight{
	"118":            {{retrievalEvalDimCodename, 1}},
	"accessibility":  {{retrievalEvalDimAccessibility, 1}},
	"account":        {{retrievalEvalDimBilling, 1}},
	"alex":           {{retrievalEvalDimTranscript, 1}},
	"audit":          {{retrievalEvalDimAccessibility, 1}},
	"auth":           {{retrievalEvalDimAuth, 1}},
	"before":         {{retrievalEvalDimDeployment, 0.25}},
	"billing":        {{retrievalEvalDimBilling, 1}},
	"blocked":        {{retrievalEvalDimTranscript, 1}},
	"blocker":        {{retrievalEvalDimTranscript, 1}},
	"build":          {{retrievalEvalDimCodename, 1}},
	"caching":        {{retrievalEvalDimCaching, 1}},
	"capybara":       {{retrievalEvalDimCodename, 1}},
	"cdn":            {{retrievalEvalDimCaching, 1}, {retrievalEvalDimDeployment, 0.5}},
	"checks":         {{retrievalEvalDimQMD, 1}},
	"codename":       {{retrievalEvalDimCodename, 1}},
	"command":        {{retrievalEvalDimTranscript, 1}},
	"concise":        {{retrievalEvalDimCoffee, 0.5}},
	"contrast":       {{retrievalEvalDimAccessibility, 1}},
	"coverage":       {{retrievalEvalDimQMD, 1}},
	"credential":     {{retrievalEvalDimAuth, 1}},
	"credentials":    {{retrievalEvalDimAuth, 1}},
	"customer":       {{retrievalEvalDimBilling, 1}},
	"deploy":         {{retrievalEvalDimDeployment, 1}},
	"deploying":      {{retrievalEvalDimDeployment, 1}},
	"deployment":     {{retrievalEvalDimDeployment, 1}},
	"deterministic":  {{retrievalEvalDimOffline, 1}},
	"download":       {{retrievalEvalDimOffline, 1}},
	"downloads":      {{retrievalEvalDimOffline, 1}},
	"edge":           {{retrievalEvalDimCaching, 1}},
	"embeddings":     {{retrievalEvalDimOffline, 1}},
	"espresso":       {{retrievalEvalDimCoffee, 1}},
	"evaluation":     {{retrievalEvalDimOffline, 0.5}, {retrievalEvalDimQMD, 0.5}},
	"experiments":    {{retrievalEvalDimQMD, 1}},
	"expired":        {{retrievalEvalDimAuth, 1}},
	"failed":         {{retrievalEvalDimDeployment, 0.5}},
	"faster":         {{retrievalEvalDimCaching, 1}},
	"focus":          {{retrievalEvalDimAccessibility, 1}},
	"forwarded":      {{retrievalEvalDimTranscript, 1}},
	"friday":         {{retrievalEvalDimAccessibility, 1}},
	"gobot":          {{retrievalEvalDimCodename, 1}},
	"hybrid":         {{retrievalEvalDimQMD, 1}},
	"image":          {{retrievalEvalDimCaching, 1}},
	"incidents":      {{retrievalEvalDimAuth, 1}},
	"invoice":        {{retrievalEvalDimBilling, 1}},
	"jamie":          {{retrievalEvalDimTranscript, 1}},
	"keyboard":       {{retrievalEvalDimAccessibility, 1}},
	"keys":           {{retrievalEvalDimAuth, 1}},
	"latency":        {{retrievalEvalDimCaching, 1}},
	"load":           {{retrievalEvalDimCaching, 1}},
	"login":          {{retrievalEvalDimAuth, 1}},
	"messages":       {{retrievalEvalDimTranscript, 1}},
	"migrations":     {{retrievalEvalDimDeployment, 1}},
	"mobile":         {{retrievalEvalDimCaching, 1}},
	"model":          {{retrievalEvalDimOffline, 1}},
	"morgan":         {{retrievalEvalDimCoffee, 1}},
	"notebook":       {{retrievalEvalDimQMD, 1}},
	"oauth":          {{retrievalEvalDimAuth, 1}},
	"offline":        {{retrievalEvalDimOffline, 1}},
	"outage":         {{retrievalEvalDimAuth, 1}},
	"paid":           {{retrievalEvalDimBilling, 1}},
	"parser":         {{retrievalEvalDimTranscript, 1}},
	"payment":        {{retrievalEvalDimBilling, 1}},
	"phoenix":        {{retrievalEvalDimCaching, 0.5}, {retrievalEvalDimDeployment, 0.5}, {retrievalEvalDimQMD, 0.5}},
	"planning":       {{retrievalEvalDimCoffee, 1}},
	"prefers":        {{retrievalEvalDimCoffee, 1}},
	"previews":       {{retrievalEvalDimCaching, 1}, {retrievalEvalDimDeployment, 0.5}},
	"qmd":            {{retrievalEvalDimQMD, 1}},
	"quarto":         {{retrievalEvalDimQMD, 1}},
	"recall":         {{retrievalEvalDimQMD, 1}},
	"reconciliation": {{retrievalEvalDimBilling, 1}},
	"redis":          {{retrievalEvalDimDeployment, 1}},
	"release":        {{retrievalEvalDimCodename, 1}},
	"remote":         {{retrievalEvalDimOffline, 1}},
	"retrieval":      {{retrievalEvalDimQMD, 1}},
	"reviews":        {{retrievalEvalDimCoffee, 1}},
	"rollback":       {{retrievalEvalDimDeployment, 1}},
	"rotate":         {{retrievalEvalDimAuth, 1}},
	"runbook":        {{retrievalEvalDimDeployment, 1}},
	"settings":       {{retrievalEvalDimAccessibility, 1}},
	"slash":          {{retrievalEvalDimTranscript, 1}},
	"standup":        {{retrievalEvalDimTranscript, 1}},
	"stripe":         {{retrievalEvalDimBilling, 1}},
	"switch":         {{retrievalEvalDimDeployment, 1}},
	"telegram":       {{retrievalEvalDimTranscript, 1}},
	"three":          {{retrievalEvalDimQMD, 1}},
	"transcript":     {{retrievalEvalDimTranscript, 1}},
	"users":          {{retrievalEvalDimCaching, 0.5}},
	"warm":           {{retrievalEvalDimDeployment, 1}},
	"webhook":        {{retrievalEvalDimBilling, 1}},
	"webhooks":       {{retrievalEvalDimBilling, 1}},
	"worker":         {{retrievalEvalDimAuth, 1}},
}

var retrievalEvalStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "as": {}, "at": {}, "be": {}, "by": {}, "can": {}, "for": {},
	"from": {}, "how": {}, "in": {}, "is": {}, "it": {}, "of": {}, "on": {}, "or": {}, "the": {},
	"this": {}, "through": {}, "to": {}, "we": {}, "with": {},
}
