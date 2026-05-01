package memory

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	_ "github.com/mattn/go-sqlite3"
)

const (
	// DefaultRetrievalEvalMaxLatency is intentionally loose: the harness should
	// catch pathological regressions without making CI noisy.
	DefaultRetrievalEvalMaxLatency = 500 * time.Millisecond

	retrievalEvalFixtureRoot       = "testdata/retrieval_eval"
	retrievalEvalFreshnessSource   = "memory/2026-04-13.md"
	retrievalEvalFreshnessOldOwner = "Casey"
)

//go:embed testdata/retrieval_eval/**
var retrievalEvalFixtureFS embed.FS

// RetrievalEvalMode identifies the backend path exercised by a golden query.
type RetrievalEvalMode string

const (
	RetrievalEvalModeFTSBM25                RetrievalEvalMode = "fts_bm25"
	RetrievalEvalModeFTSLikeFallback        RetrievalEvalMode = "fts_like_fallback"
	RetrievalEvalModeVector                 RetrievalEvalMode = "vector"
	RetrievalEvalModeHybrid                 RetrievalEvalMode = "hybrid"
	RetrievalEvalModeVectorUnavailable      RetrievalEvalMode = "vector_unavailable"
	RetrievalEvalModeQMDUnavailableFallback RetrievalEvalMode = "qmd_unavailable_fallback"
)

// RetrievalEvalOptions configures the deterministic fixture-backed evaluation.
type RetrievalEvalOptions struct {
	Queries    []RetrievalEvalQuery
	MaxLatency time.Duration
}

// RetrievalEvalQuery is one golden query with expected citations and leak guards.
type RetrievalEvalQuery struct {
	Name     string
	Mode     RetrievalEvalMode
	Query    string
	TopK     int
	Expected []RetrievalEvalCitation

	// Forbidden citations model privacy boundaries; returning any of them is a
	// test failure, not just a low-quality retrieval.
	Forbidden []RetrievalEvalCitation
	Policy    *RecallPolicy

	RequiredContent  []RetrievalEvalContentRule
	ForbiddenContent []RetrievalEvalContentRule
	WantFallback     bool
}

// RetrievalEvalCitation identifies a specific indexed memory chunk.
type RetrievalEvalCitation struct {
	SourceFile   string
	HeaderPath   string
	ChunkOrdinal int
}

// RetrievalEvalContentRule verifies freshness or other qualitative content checks.
type RetrievalEvalContentRule struct {
	Text         string
	FailureClass string
}

// RetrievalEvalHit is one retrieved chunk included in the report.
type RetrievalEvalHit struct {
	SourceFile   string
	HeaderPath   string
	ChunkOrdinal int
	Content      string
	Score        float32
}

// RetrievalEvalQueryResult records the measured outcome for one query.
type RetrievalEvalQueryResult struct {
	Name           string
	Mode           RetrievalEvalMode
	Query          string
	TopK           int
	Backend        string
	Duration       time.Duration
	Precision      float64
	Recall         float64
	Expected       []RetrievalEvalCitation
	Forbidden      []RetrievalEvalCitation
	Hits           []RetrievalEvalHit
	Missed         []RetrievalEvalCitation
	Leaks          []RetrievalEvalHit
	MissingContent []string
	StaleContent   []string
	Failures       []string
	FallbackUsed   bool
	FallbackReason string
	Error          string
}

// RetrievalEvalReport is the aggregate memory retrieval evaluation result.
type RetrievalEvalReport struct {
	StartedAt time.Time
	Duration  time.Duration

	TotalQueries  int
	PassedQueries int
	FailedQueries int

	TotalExpected   int
	ExpectedFound   int
	TotalResults    int
	RelevantResults int
	Precision       float64
	Recall          float64

	PrivacyLeaks      int
	MissingCitations  int
	FreshnessFailures int
	LatencyFailures   int
	FallbackFailures  int
	BackendErrors     int

	FailureClasses map[string]int
	QueryResults   []RetrievalEvalQueryResult
}

type retrievalEvalHarness struct {
	store    *MemoryStore
	embedder *retrievalEvalEmbedder
	chunks   []retrievalEvalChunk
}

type retrievalEvalChunk struct {
	SourceFile   string
	HeaderPath   string
	ChunkOrdinal int
	Content      string
	Embedding    []float32
}

type retrievalEvalSearchOutcome struct {
	hits           []RetrievalEvalHit
	backend        string
	fallbackUsed   bool
	fallbackReason string
	err            error
}

// RunRetrievalEval runs the offline retrieval evaluation against embedded fixtures.
func RunRetrievalEval(ctx context.Context, opts RetrievalEvalOptions) (RetrievalEvalReport, error) {
	queries := opts.Queries
	if len(queries) == 0 {
		queries = DefaultRetrievalEvalQueries()
	}
	maxLatency := opts.MaxLatency
	if maxLatency <= 0 {
		maxLatency = DefaultRetrievalEvalMaxLatency
	}

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return RetrievalEvalReport{}, fmt.Errorf("open retrieval eval sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close() //nolint:errcheck

	harness, err := newRetrievalEvalHarness(ctx, db)
	if err != nil {
		return RetrievalEvalReport{}, err
	}
	return harness.Evaluate(ctx, queries, maxLatency), nil
}

// DefaultRetrievalEvalQueries returns the built-in golden query set.
func DefaultRetrievalEvalQueries() []RetrievalEvalQuery {
	chatPolicy := NewRecallPolicy(RecallContext{ChatID: 111, ChatType: "private"})
	rolePolicy := NewRecallPolicy(RecallContext{RoleName: "ops"})

	return []RetrievalEvalQuery{
		{
			Name:  "exact-lexical-release-codename",
			Mode:  RetrievalEvalModeFTSBM25,
			Query: "capybara release codename",
			TopK:  3,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "MEMORY.md",
				HeaderPath:   "Memory > Projects > Ok Gobot > Release Facts",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:         "fts-unavailable-like-fallback-user-preference",
			Mode:         RetrievalEvalModeFTSLikeFallback,
			Query:        "Morgan prefers espresso planning reviews",
			TopK:         3,
			WantFallback: true,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "MEMORY.md",
				HeaderPath:   "Memory > Projects > Ok Gobot > User Preferences",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "semantic-vector-image-preview-latency",
			Mode:  RetrievalEvalModeVector,
			Query: "How can image previews load faster through a CDN?",
			TopK:  3,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "memory/2026-04-10.md",
				HeaderPath:   "Daily Memory: 2026-04-10 > Decisions",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "semantic-vector-login-credentials",
			Mode:  RetrievalEvalModeVector,
			Query: "login outage from old credentials",
			TopK:  3,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "memory/2026-04-11.md",
				HeaderPath:   "Daily Memory: 2026-04-11 > Incidents",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "transcript-lexical-parser-blocker",
			Mode:  RetrievalEvalModeFTSBM25,
			Query: "standup transcript slash escaping command parser blocker",
			TopK:  3,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "transcripts/2026-04-12-standup.md",
				HeaderPath:   "Transcript > Standup Snippet",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "external-path-hybrid-phoenix-runbook",
			Mode:  RetrievalEvalModeHybrid,
			Query: "Phoenix deploy rollback migrations runbook",
			TopK:  3,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "extra:phoenix/project-phoenix-runbook.md",
				HeaderPath:   "Phoenix Runbook > Deployment",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "daily-note-lexical-accessibility-reminder",
			Mode:  RetrievalEvalModeFTSBM25,
			Query: "Friday accessibility audit contrast keyboard focus",
			TopK:  3,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "memory/2026-04-10.md",
				HeaderPath:   "Daily Memory: 2026-04-10 > Reminders",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:         "vector-unavailable-lexical-offline-policy",
			Mode:         RetrievalEvalModeVectorUnavailable,
			Query:        "deterministic offline embeddings remote model downloads",
			TopK:         3,
			WantFallback: true,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "MEMORY.md",
				HeaderPath:   "Memory > Projects > Ok Gobot > Offline Policy",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:         "qmd-unavailable-falls-back-to-builtin-qmd-file",
			Mode:         RetrievalEvalModeQMDUnavailableFallback,
			Query:        "quarto notebook recall hybrid retrieval qmd",
			TopK:         3,
			WantFallback: true,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "research/phoenix-eval.qmd",
				HeaderPath:   "Research > Phoenix QMD Plan",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:   "privacy-chat-scope-blocks-other-chat",
			Mode:   RetrievalEvalModeHybrid,
			Query:  "Atlas escalation route handoff",
			TopK:   5,
			Policy: chatPolicy,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "memory/chats/111/project-atlas.md",
				HeaderPath:   "Chat 111 Memory > Project Atlas",
				ChunkOrdinal: 0,
			}},
			Forbidden: []RetrievalEvalCitation{{
				SourceFile:   "memory/chats/222/project-atlas-secret.md",
				HeaderPath:   "Chat 222 Memory > Project Atlas Secret",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:   "privacy-role-scope-blocks-admin-role",
			Mode:   RetrievalEvalModeHybrid,
			Query:  "ops deploy checks bastion audit queue",
			TopK:   5,
			Policy: rolePolicy,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "memory/roles/ops/deploy.md",
				HeaderPath:   "Ops Role Memory > Deploy Checks",
				ChunkOrdinal: 0,
			}},
			Forbidden: []RetrievalEvalCitation{{
				SourceFile:   "memory/roles/admin/private.md",
				HeaderPath:   "Admin Role Memory > Deploy Secrets",
				ChunkOrdinal: 0,
			}},
		},
		{
			Name:  "freshness-after-flush-compaction",
			Mode:  RetrievalEvalModeHybrid,
			Query: "current billing processor owner after compaction flush",
			TopK:  3,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   retrievalEvalFreshnessSource,
				HeaderPath:   "Daily Memory: 2026-04-13 > Compaction",
				ChunkOrdinal: 0,
			}},
			RequiredContent: []RetrievalEvalContentRule{{Text: "Riley", FailureClass: "freshness"}},
			ForbiddenContent: []RetrievalEvalContentRule{{
				Text:         retrievalEvalFreshnessOldOwner,
				FailureClass: "freshness",
			}},
		},
	}
}

func newRetrievalEvalHarness(ctx context.Context, db *sql.DB) (*retrievalEvalHarness, error) {
	store, err := NewMemoryStore(db)
	if err != nil {
		return nil, fmt.Errorf("create retrieval eval memory store: %w", err)
	}

	embedder := &retrievalEvalEmbedder{}
	indexer := NewIndexer("", store, embedder, WithIndexerChunking(256, 0))
	if err := indexRetrievalEvalContent(ctx, indexer, retrievalEvalFreshnessSource, retrievalEvalStaleFreshnessMarkdown()); err != nil {
		return nil, err
	}

	paths, err := retrievalEvalFixturePaths()
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		content, err := retrievalEvalFixtureFS.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read retrieval eval fixture %s: %w", path, err)
		}
		source := retrievalEvalSourceLabel(strings.TrimPrefix(path, retrievalEvalFixtureRoot+"/"))
		if err := indexRetrievalEvalContent(ctx, indexer, source, string(content)); err != nil {
			return nil, err
		}
	}

	chunks, err := loadRetrievalEvalChunks(ctx, db)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("retrieval evaluation fixtures produced no indexed chunks")
	}

	return &retrievalEvalHarness{store: store, embedder: embedder, chunks: chunks}, nil
}

func retrievalEvalFixturePaths() ([]string, error) {
	var paths []string
	err := fs.WalkDir(retrievalEvalFixtureFS, retrievalEvalFixtureRoot, func(path string, d fs.DirEntry, walkErr error) error {
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
		return nil, fmt.Errorf("walk retrieval eval fixtures: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

func retrievalEvalSourceLabel(relativePath string) string {
	rel := filepath.ToSlash(filepath.Clean(relativePath))
	rel = strings.TrimPrefix(rel, "./")
	if strings.HasPrefix(rel, "extra/") {
		return SourceLabelForExtra("phoenix", strings.TrimPrefix(rel, "extra/"))
	}
	return rel
}

func retrievalEvalStaleFreshnessMarkdown() string {
	return "# Daily Memory: 2026-04-13\n\n" +
		"## Compaction\n\n" +
		"After memory compaction, the billing processor owner is Casey. " +
		"This stale owner should disappear after the next memory flush.\n"
}

func indexRetrievalEvalContent(ctx context.Context, indexer *Indexer, source, content string) error {
	chunks := indexer.chunkFile(source, content)
	if len(chunks) == 0 {
		return indexer.deleteBySourceFile(ctx, source)
	}

	existingHashes, err := indexer.loadChunkHashes(ctx, source)
	if err != nil {
		return err
	}

	changedIndexes := make([]int, 0, len(chunks))
	changedTexts := make([]string, 0, len(chunks))
	for idx := range chunks {
		key := chunkKey{headerPath: chunks[idx].HeaderPath, ordinal: chunks[idx].ChunkOrdinal}
		if hash, exists := existingHashes[key]; exists && hash == chunks[idx].ContentHash {
			delete(existingHashes, key)
			continue
		}
		changedIndexes = append(changedIndexes, idx)
		changedTexts = append(changedTexts, chunks[idx].Content)
		delete(existingHashes, key)
	}

	embeddings, err := indexer.embedChangedChunks(ctx, changedTexts)
	if err != nil {
		return err
	}
	for pos, chunkIndex := range changedIndexes {
		chunks[chunkIndex].Embedding = embeddings[pos]
	}

	if err := indexer.persistChunks(ctx, source, chunks, changedIndexes, existingHashes); err != nil {
		return fmt.Errorf("index retrieval eval fixture %s: %w", source, err)
	}
	return nil
}

func loadRetrievalEvalChunks(ctx context.Context, db *sql.DB) ([]retrievalEvalChunk, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT source_file, header_path, chunk_ordinal, content, embedding
		FROM memory_chunks
		ORDER BY source_file, header_path, chunk_ordinal
	`)
	if err != nil {
		return nil, fmt.Errorf("query retrieval eval chunks: %w", err)
	}
	defer rows.Close()

	var chunks []retrievalEvalChunk
	for rows.Next() {
		var chunk retrievalEvalChunk
		var rawEmbedding []byte
		if err := rows.Scan(&chunk.SourceFile, &chunk.HeaderPath, &chunk.ChunkOrdinal, &chunk.Content, &rawEmbedding); err != nil {
			return nil, fmt.Errorf("scan retrieval eval chunk: %w", err)
		}
		chunk.Embedding, err = decodeEmbedding(rawEmbedding)
		if err != nil {
			return nil, fmt.Errorf("decode retrieval eval embedding for %s: %w", chunk.SourceFile, err)
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retrieval eval chunks: %w", err)
	}
	return chunks, nil
}

// Evaluate runs queries through the configured memory backends and scores results.
func (h *retrievalEvalHarness) Evaluate(ctx context.Context, queries []RetrievalEvalQuery, maxLatency time.Duration) RetrievalEvalReport {
	started := time.Now()
	report := RetrievalEvalReport{
		StartedAt:      started.UTC(),
		FailureClasses: make(map[string]int),
		QueryResults:   make([]RetrievalEvalQueryResult, 0, len(queries)),
	}

	for _, query := range queries {
		result := h.evaluateQuery(ctx, query, maxLatency)
		report.addQueryResult(result)
	}

	report.Duration = time.Since(started)
	report.Precision = retrievalEvalRatio(report.RelevantResults, report.TotalResults)
	report.Recall = retrievalEvalRatio(report.ExpectedFound, report.TotalExpected)
	return report
}

func (h *retrievalEvalHarness) evaluateQuery(ctx context.Context, query RetrievalEvalQuery, maxLatency time.Duration) RetrievalEvalQueryResult {
	topK := query.TopK
	if topK <= 0 {
		topK = DefaultSearchTopK
	}
	if query.Mode == "" {
		query.Mode = RetrievalEvalModeHybrid
	}

	result := RetrievalEvalQueryResult{
		Name:      strings.TrimSpace(query.Name),
		Mode:      query.Mode,
		Query:     strings.TrimSpace(query.Query),
		TopK:      topK,
		Expected:  append([]RetrievalEvalCitation(nil), query.Expected...),
		Forbidden: append([]RetrievalEvalCitation(nil), query.Forbidden...),
	}
	if result.Name == "" {
		result.Name = result.Query
	}

	start := time.Now()
	outcome := h.search(ctx, query, topK)
	result.Duration = time.Since(start)
	result.Backend = outcome.backend
	result.FallbackUsed = outcome.fallbackUsed
	result.FallbackReason = outcome.fallbackReason
	if outcome.err != nil {
		result.Error = outcome.err.Error()
		result.addFailure("backend_error")
		return result
	}
	result.Hits = outcome.hits

	result.Missed = missedRetrievalEvalCitations(query.Expected, result.Hits)
	if len(result.Missed) > 0 {
		result.addFailure("missing_citation")
		if len(result.Hits) == 0 {
			result.addFailure("no_results")
		}
	}

	result.Leaks = leakedRetrievalEvalHits(query.Forbidden, result.Hits)
	if len(result.Leaks) > 0 {
		result.addFailure("privacy_leak")
	}

	for _, rule := range query.RequiredContent {
		if strings.TrimSpace(rule.Text) == "" || retrievalEvalHitsContain(result.Hits, rule.Text) {
			continue
		}
		result.MissingContent = append(result.MissingContent, rule.Text)
		result.addFailure(retrievalEvalRuleClass(rule, "content_missing"))
	}
	for _, rule := range query.ForbiddenContent {
		if strings.TrimSpace(rule.Text) == "" {
			continue
		}
		for _, hit := range result.Hits {
			if strings.Contains(strings.ToLower(hit.Content), strings.ToLower(rule.Text)) {
				result.StaleContent = append(result.StaleContent, fmt.Sprintf("%s in %s", rule.Text, hit.String()))
				result.addFailure(retrievalEvalRuleClass(rule, "forbidden_content"))
			}
		}
	}

	if maxLatency > 0 && result.Duration > maxLatency {
		result.addFailure("latency")
	}
	if query.WantFallback && !result.FallbackUsed {
		result.addFailure("fallback")
	}

	result.Precision = retrievalEvalRatio(relevantRetrievalEvalHits(query.Expected, result.Hits), len(result.Hits))
	result.Recall = retrievalEvalRatio(len(query.Expected)-len(result.Missed), len(query.Expected))
	return result
}

func (h *retrievalEvalHarness) search(ctx context.Context, query RetrievalEvalQuery, topK int) retrievalEvalSearchOutcome {
	switch query.Mode {
	case RetrievalEvalModeFTSBM25:
		fallbackUsed := !h.store.ftsAvailable
		outcome := h.searchStore(ctx, query.Query, nil, topK, query.Policy, "fts5/bm25")
		outcome.fallbackUsed = fallbackUsed
		if fallbackUsed {
			outcome.fallbackReason = "fts5 unavailable; used LIKE lexical fallback"
		}
		return outcome
	case RetrievalEvalModeFTSLikeFallback:
		previous := h.store.ftsAvailable
		h.store.ftsAvailable = false
		outcome := h.searchStore(ctx, query.Query, nil, topK, query.Policy, "like lexical fallback")
		h.store.ftsAvailable = previous
		outcome.fallbackUsed = true
		outcome.fallbackReason = "fts5 forced unavailable; used LIKE lexical fallback"
		return outcome
	case RetrievalEvalModeVector:
		return h.searchStore(ctx, "", h.embedder.embed(query.Query), topK, query.Policy, "vector")
	case RetrievalEvalModeHybrid:
		return h.searchStore(ctx, query.Query, h.embedder.embed(query.Query), topK, query.Policy, "hybrid")
	case RetrievalEvalModeVectorUnavailable:
		backend := NewBuiltinBackend(nil, h.store)
		results, err := backend.SearchScoped(ctx, query.Query, topK, false, query.Policy)
		return retrievalEvalSearchOutcome{
			hits:           retrievalEvalHitsFromResults(results),
			backend:        "builtin(vector unavailable)",
			fallbackUsed:   true,
			fallbackReason: "embedding backend unavailable; used lexical retrieval",
			err:            err,
		}
	case RetrievalEvalModeQMDUnavailableFallback:
		primary := retrievalEvalUnavailableBackend{name: "qmd", err: fmt.Errorf("qmd backend unavailable in offline retrieval evaluation")}
		fallback := NewFallbackBackend(primary, NewBuiltinBackend(h.embedder, h.store), time.Minute)
		results, err := fallback.SearchScoped(ctx, query.Query, topK, false, query.Policy)
		return retrievalEvalSearchOutcome{
			hits:           retrievalEvalHitsFromResults(results),
			backend:        fallback.Name(),
			fallbackUsed:   fallback.LastError() != "",
			fallbackReason: fallback.LastError(),
			err:            err,
		}
	default:
		return retrievalEvalSearchOutcome{backend: string(query.Mode), err: fmt.Errorf("unknown retrieval eval mode %q", query.Mode)}
	}
}

func (h *retrievalEvalHarness) searchStore(ctx context.Context, query string, embedding []float32, topK int, policy *RecallPolicy, backend string) retrievalEvalSearchOutcome {
	results, err := h.store.SearchHybridScoped(ctx, query, embedding, topK, policy)
	return retrievalEvalSearchOutcome{hits: retrievalEvalHitsFromResults(results), backend: backend, err: err}
}

func retrievalEvalHitsFromResults(results []MemoryResult) []RetrievalEvalHit {
	hits := make([]RetrievalEvalHit, 0, len(results))
	for _, result := range results {
		hits = append(hits, RetrievalEvalHit{
			SourceFile:   result.SourceFile,
			HeaderPath:   result.HeaderPath,
			ChunkOrdinal: result.ChunkOrdinal,
			Content:      result.Content,
			Score:        result.Similarity,
		})
	}
	return hits
}

func (r *RetrievalEvalReport) addQueryResult(result RetrievalEvalQueryResult) {
	r.TotalQueries++
	r.TotalExpected += len(result.Expected)
	r.ExpectedFound += len(result.Expected) - len(result.Missed)
	r.TotalResults += len(result.Hits)
	r.RelevantResults += relevantRetrievalEvalHits(result.Expected, result.Hits)
	r.PrivacyLeaks += len(result.Leaks)
	r.MissingCitations += len(result.Missed)
	if result.Error != "" {
		r.BackendErrors++
	}

	seen := make(map[string]bool, len(result.Failures))
	for _, class := range result.Failures {
		if seen[class] {
			continue
		}
		seen[class] = true
		r.FailureClasses[class]++
		switch class {
		case "freshness":
			r.FreshnessFailures++
		case "latency":
			r.LatencyFailures++
		case "fallback":
			r.FallbackFailures++
		}
	}

	if len(result.Failures) == 0 {
		r.PassedQueries++
	} else {
		r.FailedQueries++
	}
	r.QueryResults = append(r.QueryResults, result)
}

// Passed reports whether the evaluation met all golden expectations.
func (r RetrievalEvalReport) Passed() bool {
	return r.FailedQueries == 0 && r.PrivacyLeaks == 0 && r.MissingCitations == 0 && r.BackendErrors == 0 && r.FreshnessFailures == 0 && r.LatencyFailures == 0 && r.FallbackFailures == 0
}

// FormatText renders the operator/CI report.
func (r RetrievalEvalReport) FormatText() string {
	var b strings.Builder
	status := "pass"
	if !r.Passed() {
		status = "fail"
	}

	fmt.Fprintf(&b, "Memory Retrieval Evaluation Report\n")
	fmt.Fprintf(&b, "Status: %s\n", status)
	fmt.Fprintf(&b, "Duration: %s\n", retrievalEvalDurationString(r.Duration))
	fmt.Fprintf(&b, "Queries: %d passed=%d failed=%d\n", r.TotalQueries, r.PassedQueries, r.FailedQueries)
	fmt.Fprintf(&b, "Recall: %d/%d (%.2f)\n", r.ExpectedFound, r.TotalExpected, r.Recall)
	fmt.Fprintf(&b, "Precision-ish: %d/%d (%.2f)\n", r.RelevantResults, r.TotalResults, r.Precision)
	fmt.Fprintf(&b, "Privacy leaks: %d\n", r.PrivacyLeaks)
	fmt.Fprintf(&b, "Missing citations: %d\n", r.MissingCitations)
	fmt.Fprintf(&b, "Freshness failures: %d\n", r.FreshnessFailures)
	fmt.Fprintf(&b, "Latency failures: %d\n", r.LatencyFailures)
	fmt.Fprintf(&b, "Fallback failures: %d\n", r.FallbackFailures)
	fmt.Fprintf(&b, "Backend errors: %d\n", r.BackendErrors)
	fmt.Fprintf(&b, "Failure classes: %s\n", retrievalEvalFailureClassSummary(r.FailureClasses))

	for _, result := range r.QueryResults {
		b.WriteByte('\n')
		state := "PASS"
		if len(result.Failures) > 0 {
			state = "FAIL"
		}
		fmt.Fprintf(&b, "%s %s [%s@%d] backend=%s recall=%.2f precision=%.2f latency=%s\n",
			state,
			result.Name,
			result.Mode,
			result.TopK,
			valueOr(result.Backend, "unknown"),
			result.Recall,
			result.Precision,
			retrievalEvalDurationString(result.Duration),
		)
		if result.FallbackUsed || result.FallbackReason != "" {
			fmt.Fprintf(&b, "  fallback: used=%t reason=%s\n", result.FallbackUsed, valueOr(result.FallbackReason, "none"))
		}
		if len(result.Failures) > 0 {
			fmt.Fprintf(&b, "  failure_classes: %s\n", strings.Join(result.Failures, ", "))
		}
		if result.Error != "" {
			fmt.Fprintf(&b, "  error: %s\n", result.Error)
		}
		writeRetrievalEvalCitationList(&b, "expected", result.Expected)
		writeRetrievalEvalCitationList(&b, "missing", result.Missed)
		writeRetrievalEvalCitationList(&b, "forbidden", result.Forbidden)
		writeRetrievalEvalHitList(&b, "privacy_leaks", result.Leaks)
		writeRetrievalEvalStringList(&b, "missing_content", result.MissingContent)
		writeRetrievalEvalStringList(&b, "stale_content", result.StaleContent)
		writeRetrievalEvalHitList(&b, "hits", result.Hits)
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeRetrievalEvalCitationList(b *strings.Builder, label string, citations []RetrievalEvalCitation) {
	if len(citations) == 0 {
		return
	}
	fmt.Fprintf(b, "  %s:\n", label)
	for _, citation := range citations {
		fmt.Fprintf(b, "    - %s\n", citation.String())
	}
}

func writeRetrievalEvalHitList(b *strings.Builder, label string, hits []RetrievalEvalHit) {
	if len(hits) == 0 {
		return
	}
	fmt.Fprintf(b, "  %s:\n", label)
	for idx, hit := range hits {
		fmt.Fprintf(b, "    - %d. %s score=%.4f %q\n", idx+1, hit.String(), hit.Score, summarizeRetrievalEvalContent(hit.Content))
	}
}

func writeRetrievalEvalStringList(b *strings.Builder, label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(b, "  %s:\n", label)
	for _, value := range values {
		fmt.Fprintf(b, "    - %s\n", value)
	}
}

func retrievalEvalFailureClassSummary(classes map[string]int) string {
	if len(classes) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(classes))
	for key := range classes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, classes[key]))
	}
	return strings.Join(parts, ", ")
}

func retrievalEvalDurationString(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	if duration < time.Millisecond {
		micros := duration.Microseconds()
		if micros <= 0 {
			return "0us"
		}
		return fmt.Sprintf("%dus", micros)
	}
	return duration.Round(time.Millisecond).String()
}

func (r *RetrievalEvalQueryResult) addFailure(class string) {
	class = strings.TrimSpace(class)
	if class == "" {
		return
	}
	for _, existing := range r.Failures {
		if existing == class {
			return
		}
	}
	r.Failures = append(r.Failures, class)
}

func missedRetrievalEvalCitations(expected []RetrievalEvalCitation, hits []RetrievalEvalHit) []RetrievalEvalCitation {
	var missed []RetrievalEvalCitation
	for _, want := range expected {
		if !retrievalEvalCitationInHits(want, hits) {
			missed = append(missed, want)
		}
	}
	return missed
}

func leakedRetrievalEvalHits(forbidden []RetrievalEvalCitation, hits []RetrievalEvalHit) []RetrievalEvalHit {
	var leaked []RetrievalEvalHit
	for _, hit := range hits {
		for _, deny := range forbidden {
			if retrievalEvalCitationMatchesHit(deny, hit) {
				leaked = append(leaked, hit)
				break
			}
		}
	}
	return leaked
}

func relevantRetrievalEvalHits(expected []RetrievalEvalCitation, hits []RetrievalEvalHit) int {
	count := 0
	for _, hit := range hits {
		for _, want := range expected {
			if retrievalEvalCitationMatchesHit(want, hit) {
				count++
				break
			}
		}
	}
	return count
}

func retrievalEvalCitationInHits(citation RetrievalEvalCitation, hits []RetrievalEvalHit) bool {
	for _, hit := range hits {
		if retrievalEvalCitationMatchesHit(citation, hit) {
			return true
		}
	}
	return false
}

func retrievalEvalCitationMatchesHit(citation RetrievalEvalCitation, hit RetrievalEvalHit) bool {
	return hit.SourceFile == citation.SourceFile && hit.HeaderPath == citation.HeaderPath && hit.ChunkOrdinal == citation.ChunkOrdinal
}

func retrievalEvalHitsContain(hits []RetrievalEvalHit, text string) bool {
	needle := strings.ToLower(strings.TrimSpace(text))
	if needle == "" {
		return true
	}
	for _, hit := range hits {
		if strings.Contains(strings.ToLower(hit.Content), needle) {
			return true
		}
	}
	return false
}

func retrievalEvalRuleClass(rule RetrievalEvalContentRule, fallback string) string {
	if class := strings.TrimSpace(rule.FailureClass); class != "" {
		return class
	}
	return fallback
}

func retrievalEvalRatio(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func summarizeRetrievalEvalContent(content string) string {
	content = strings.Join(strings.Fields(content), " ")
	if len(content) <= 96 {
		return content
	}
	return content[:93] + "..."
}

// String renders a stable citation key.
func (c RetrievalEvalCitation) String() string {
	return fmt.Sprintf("%s :: %s#%d", c.SourceFile, c.HeaderPath, c.ChunkOrdinal)
}

// String renders a stable citation key for a hit.
func (h RetrievalEvalHit) String() string {
	return fmt.Sprintf("%s :: %s#%d", h.SourceFile, h.HeaderPath, h.ChunkOrdinal)
}

type retrievalEvalEmbedder struct{}

func (e *retrievalEvalEmbedder) GetEmbedding(_ context.Context, text string) ([]float32, error) {
	return e.embed(text), nil
}

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

	var hasWeight bool
	for _, value := range vector {
		if value != 0 {
			hasWeight = true
			break
		}
	}
	if !hasWeight {
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
	retrievalEvalDimPrivacy
	retrievalEvalDimFreshness
	retrievalEvalDimFallback
	retrievalEvalVectorDimensions
)

var retrievalEvalVectorWeights = map[string][]retrievalEvalVectorWeight{
	"111":            {{retrievalEvalDimPrivacy, 1}},
	"118":            {{retrievalEvalDimCodename, 1}},
	"accessibility":  {{retrievalEvalDimAccessibility, 1}},
	"account":        {{retrievalEvalDimBilling, 1}},
	"admin":          {{retrievalEvalDimPrivacy, 0.5}},
	"alex":           {{retrievalEvalDimTranscript, 1}},
	"atlas":          {{retrievalEvalDimPrivacy, 1}},
	"audit":          {{retrievalEvalDimAccessibility, 1}, {retrievalEvalDimPrivacy, 0.5}},
	"auth":           {{retrievalEvalDimAuth, 1}},
	"badger":         {{retrievalEvalDimPrivacy, 1}},
	"bastion":        {{retrievalEvalDimPrivacy, 1}},
	"before":         {{retrievalEvalDimDeployment, 0.25}},
	"billing":        {{retrievalEvalDimBilling, 1}, {retrievalEvalDimFreshness, 0.5}},
	"blocked":        {{retrievalEvalDimTranscript, 1}},
	"blocker":        {{retrievalEvalDimTranscript, 1}},
	"blue":           {{retrievalEvalDimPrivacy, 1}},
	"build":          {{retrievalEvalDimCodename, 1}},
	"caching":        {{retrievalEvalDimCaching, 1}},
	"capybara":       {{retrievalEvalDimCodename, 1}},
	"casey":          {{retrievalEvalDimFreshness, 1}},
	"cdn":            {{retrievalEvalDimCaching, 1}, {retrievalEvalDimDeployment, 0.5}},
	"checks":         {{retrievalEvalDimQMD, 1}, {retrievalEvalDimPrivacy, 0.5}},
	"codename":       {{retrievalEvalDimCodename, 1}},
	"command":        {{retrievalEvalDimTranscript, 1}},
	"compaction":     {{retrievalEvalDimFreshness, 1}},
	"concise":        {{retrievalEvalDimCoffee, 0.5}},
	"contrast":       {{retrievalEvalDimAccessibility, 1}},
	"coverage":       {{retrievalEvalDimQMD, 1}},
	"credential":     {{retrievalEvalDimAuth, 1}},
	"credentials":    {{retrievalEvalDimAuth, 1}},
	"current":        {{retrievalEvalDimFreshness, 1}},
	"customer":       {{retrievalEvalDimBilling, 1}},
	"deploy":         {{retrievalEvalDimDeployment, 1}, {retrievalEvalDimPrivacy, 0.5}},
	"deploying":      {{retrievalEvalDimDeployment, 1}},
	"deployment":     {{retrievalEvalDimDeployment, 1}},
	"deterministic":  {{retrievalEvalDimOffline, 1}},
	"download":       {{retrievalEvalDimOffline, 1}},
	"downloads":      {{retrievalEvalDimOffline, 1}},
	"edge":           {{retrievalEvalDimCaching, 1}},
	"embeddings":     {{retrievalEvalDimOffline, 1}},
	"escalation":     {{retrievalEvalDimPrivacy, 1}},
	"espresso":       {{retrievalEvalDimCoffee, 1}},
	"evaluation":     {{retrievalEvalDimOffline, 0.5}, {retrievalEvalDimQMD, 0.5}},
	"experiments":    {{retrievalEvalDimQMD, 1}},
	"expired":        {{retrievalEvalDimAuth, 1}},
	"failed":         {{retrievalEvalDimDeployment, 0.5}},
	"faster":         {{retrievalEvalDimCaching, 1}},
	"flush":          {{retrievalEvalDimFreshness, 1}},
	"focus":          {{retrievalEvalDimAccessibility, 1}},
	"forwarded":      {{retrievalEvalDimTranscript, 1}},
	"friday":         {{retrievalEvalDimAccessibility, 1}},
	"gobot":          {{retrievalEvalDimCodename, 1}},
	"handoff":        {{retrievalEvalDimPrivacy, 1}},
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
	"ops":            {{retrievalEvalDimPrivacy, 1}},
	"orchid":         {{retrievalEvalDimPrivacy, 1}},
	"outage":         {{retrievalEvalDimAuth, 1}},
	"owner":          {{retrievalEvalDimFreshness, 1}},
	"paid":           {{retrievalEvalDimBilling, 1}},
	"parser":         {{retrievalEvalDimTranscript, 1}},
	"passphrase":     {{retrievalEvalDimPrivacy, 1}},
	"payment":        {{retrievalEvalDimBilling, 1}},
	"phoenix":        {{retrievalEvalDimCaching, 0.5}, {retrievalEvalDimDeployment, 0.5}, {retrievalEvalDimQMD, 0.5}},
	"planning":       {{retrievalEvalDimCoffee, 1}},
	"prefers":        {{retrievalEvalDimCoffee, 1}},
	"previews":       {{retrievalEvalDimCaching, 1}, {retrievalEvalDimDeployment, 0.5}},
	"processor":      {{retrievalEvalDimFreshness, 1}},
	"qmd":            {{retrievalEvalDimQMD, 1}},
	"queue":          {{retrievalEvalDimPrivacy, 1}},
	"quarto":         {{retrievalEvalDimQMD, 1}},
	"recall":         {{retrievalEvalDimQMD, 1}},
	"reconciliation": {{retrievalEvalDimBilling, 1}},
	"redis":          {{retrievalEvalDimDeployment, 1}},
	"release":        {{retrievalEvalDimCodename, 1}},
	"remote":         {{retrievalEvalDimOffline, 1}},
	"retrieval":      {{retrievalEvalDimQMD, 1}},
	"reviews":        {{retrievalEvalDimCoffee, 1}},
	"riley":          {{retrievalEvalDimFreshness, 1}},
	"rollback":       {{retrievalEvalDimDeployment, 1}},
	"root":           {{retrievalEvalDimPrivacy, 1}},
	"rotate":         {{retrievalEvalDimAuth, 1}},
	"route":          {{retrievalEvalDimPrivacy, 1}},
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
	"vault":          {{retrievalEvalDimPrivacy, 1}},
	"warm":           {{retrievalEvalDimDeployment, 1}},
	"webhook":        {{retrievalEvalDimBilling, 1}},
	"webhooks":       {{retrievalEvalDimBilling, 1}},
	"worker":         {{retrievalEvalDimAuth, 1}},
	"zebra":          {{retrievalEvalDimPrivacy, 1}},
	"yellow":         {{retrievalEvalDimPrivacy, 1}},
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

type retrievalEvalUnavailableBackend struct {
	name string
	err  error
}

func (b retrievalEvalUnavailableBackend) Name() string {
	return b.name
}

func (b retrievalEvalUnavailableBackend) Search(context.Context, string, int, bool) ([]MemoryResult, error) {
	return nil, b.err
}
