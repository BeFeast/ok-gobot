package memory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

const (
	// DefaultSearchThreshold is the default minimum cosine score for memory search.
	DefaultSearchThreshold float32 = 0.75
	// DefaultSearchTopK is the default number of snippets returned by memory search.
	DefaultSearchTopK = 5
)

// MemorySnippet is a ranked memory match returned from semantic search.
type MemorySnippet struct {
	File       string  `json:"file"`
	HeaderPath string  `json:"header_path"`
	Text       string  `json:"text"`
	Score      float32 `json:"score"`
}

// SearchOptions configures per-query filtering and result count.
type SearchOptions struct {
	Threshold    float32
	TopK         int
	ExpandBranch bool // When true, expand results to include all chunks from matching branches.
}

// Searcher keeps memory chunk embeddings in RAM and performs cosine search in Go.
type Searcher struct {
	mu sync.RWMutex

	chunks    []indexedChunk
	dimension int
}

type indexedChunk struct {
	File       string
	HeaderPath string
	Ordinal    int
	Text       string
	Embedding  []float32 // Pre-normalized for fast cosine dot products.
}

type memoryChunkColumns struct {
	File      string
	Header    string
	Ordinal   string
	Text      string
	Embedding string
}

// NewSearcher loads all memory_chunks embeddings into RAM on startup.
func NewSearcher(ctx context.Context, db *sql.DB) (*Searcher, error) {
	if db == nil {
		return nil, fmt.Errorf("memory search db is nil")
	}

	s := &Searcher{}
	if err := s.Reload(ctx, db); err != nil {
		return nil, err
	}

	return s, nil
}

// Reload rebuilds the in-memory search index from the memory_chunks table.
func (s *Searcher) Reload(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("memory search db is nil")
	}

	chunks, dimension, err := loadChunksIntoRAM(ctx, db)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.chunks = chunks
	s.dimension = dimension
	s.mu.Unlock()

	return nil
}

// ChunkCount returns the number of chunks currently loaded in RAM.
func (s *Searcher) ChunkCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.chunks)
}

// Search performs a linear cosine search over in-memory chunk vectors.
func (s *Searcher) Search(queryEmbedding []float32, opts SearchOptions) []MemorySnippet {
	threshold := opts.Threshold
	if threshold == 0 {
		threshold = DefaultSearchThreshold
	}
	if threshold < -1 {
		threshold = -1
	}
	if threshold > 1 {
		threshold = 1
	}

	topK := opts.TopK
	if topK <= 0 {
		topK = DefaultSearchTopK
	}

	query, ok := normalizeEmbedding(queryEmbedding)
	if !ok {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(query) != s.dimension || s.dimension == 0 || topK == 0 {
		return nil
	}

	best := make([]MemorySnippet, 0, topK)
	for _, chunk := range s.chunks {
		score := dotProduct(query, chunk.Embedding)
		if score < threshold {
			continue
		}

		snippet := MemorySnippet{
			File:       chunk.File,
			HeaderPath: chunk.HeaderPath,
			Text:       chunk.Text,
			Score:      score,
		}

		if len(best) < topK {
			best = append(best, snippet)
			bubbleUpByScore(best, len(best)-1)
			continue
		}

		if score <= best[len(best)-1].Score {
			continue
		}

		best[len(best)-1] = snippet
		bubbleUpByScore(best, len(best)-1)
	}

	if opts.ExpandBranch && len(best) > 0 {
		best = s.expandBranches(best)
	}

	return best
}

// expandBranches groups matching snippets by (File, HeaderPath), collects all
// chunks from each branch, and returns one combined snippet per branch.
func (s *Searcher) expandBranches(hits []MemorySnippet) []MemorySnippet {
	type branchKey struct {
		file, headerPath string
	}

	// Collect unique branches preserving first-seen order, tracking best score.
	var orderedKeys []branchKey
	bestScores := make(map[branchKey]float32)
	for _, h := range hits {
		key := branchKey{h.File, h.HeaderPath}
		prev, seen := bestScores[key]
		if !seen {
			orderedKeys = append(orderedKeys, key)
		}
		if !seen || h.Score > prev {
			bestScores[key] = h.Score
		}
	}

	// For each branch, collect all chunks from the in-RAM index.
	type orderedText struct {
		ordinal int
		text    string
	}

	expanded := make([]MemorySnippet, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		var parts []orderedText
		for _, chunk := range s.chunks {
			if chunk.File == key.file && chunk.HeaderPath == key.headerPath {
				parts = append(parts, orderedText{ordinal: chunk.Ordinal, text: chunk.Text})
			}
		}

		sort.Slice(parts, func(i, j int) bool {
			return parts[i].ordinal < parts[j].ordinal
		})

		texts := make([]string, len(parts))
		for i, p := range parts {
			texts[i] = p.text
		}

		expanded = append(expanded, MemorySnippet{
			File:       key.file,
			HeaderPath: key.headerPath,
			Text:       strings.Join(texts, "\n\n"),
			Score:      bestScores[key],
		})
	}

	sort.Slice(expanded, func(i, j int) bool {
		return expanded[i].Score > expanded[j].Score
	})

	return expanded
}

func bubbleUpByScore(snippets []MemorySnippet, idx int) {
	for idx > 0 && snippets[idx].Score > snippets[idx-1].Score {
		snippets[idx], snippets[idx-1] = snippets[idx-1], snippets[idx]
		idx--
	}
}

func loadChunksIntoRAM(ctx context.Context, db *sql.DB) ([]indexedChunk, int, error) {
	columns, err := resolveMemoryChunkColumns(ctx, db)
	if err != nil {
		return nil, 0, err
	}

	headerExpr := "''"
	if columns.Header != "" {
		headerExpr = quoteIdentifier(columns.Header)
	}

	ordinalExpr := "0"
	if columns.Ordinal != "" {
		ordinalExpr = quoteIdentifier(columns.Ordinal)
	}

	query := fmt.Sprintf(
		"SELECT %s, %s, %s, %s, %s FROM memory_chunks",
		quoteIdentifier(columns.File),
		headerExpr,
		ordinalExpr,
		quoteIdentifier(columns.Text),
		quoteIdentifier(columns.Embedding),
	)

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, 0, fmt.Errorf("failed loading memory chunks: %w", err)
	}
	defer rows.Close()

	var (
		chunks    []indexedChunk
		dimension int
	)

	for rows.Next() {
		var (
			file       sql.NullString
			headerPath sql.NullString
			ordinal    int
			text       sql.NullString
			rawVector  []byte
		)

		if err := rows.Scan(&file, &headerPath, &ordinal, &text, &rawVector); err != nil {
			return nil, 0, fmt.Errorf("failed scanning memory chunk: %w", err)
		}

		embedding, err := decodeChunkEmbedding(rawVector)
		if err != nil {
			continue
		}

		normalized, ok := normalizeEmbedding(embedding)
		if !ok {
			continue
		}

		if dimension == 0 {
			dimension = len(normalized)
		}
		if len(normalized) != dimension {
			continue
		}

		chunks = append(chunks, indexedChunk{
			File:       file.String,
			HeaderPath: headerPath.String,
			Ordinal:    ordinal,
			Text:       text.String,
			Embedding:  normalized,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed reading memory chunks: %w", err)
	}

	return chunks, dimension, nil
}

func resolveMemoryChunkColumns(ctx context.Context, db *sql.DB) (memoryChunkColumns, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(memory_chunks)")
	if err != nil {
		return memoryChunkColumns{}, fmt.Errorf("failed inspecting memory_chunks schema: %w", err)
	}
	defer rows.Close()

	available := make(map[string]string)
	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			defaultVal interface{}
			primaryKey int
		)

		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultVal, &primaryKey); err != nil {
			return memoryChunkColumns{}, fmt.Errorf("failed inspecting memory_chunks schema: %w", err)
		}
		available[strings.ToLower(name)] = name
	}
	if err := rows.Err(); err != nil {
		return memoryChunkColumns{}, fmt.Errorf("failed inspecting memory_chunks schema: %w", err)
	}
	if len(available) == 0 {
		return memoryChunkColumns{}, fmt.Errorf("memory_chunks table not found")
	}

	columns := memoryChunkColumns{
		File:      pickColumn(available, "file", "file_path", "filepath", "path"),
		Header:    pickColumn(available, "header_path", "heading_path", "section_path", "header"),
		Ordinal:   pickColumn(available, "chunk_ordinal", "ordinal", "chunk_order"),
		Text:      pickColumn(available, "text", "chunk_text", "content"),
		Embedding: pickColumn(available, "embedding", "vector", "embedding_blob"),
	}

	if columns.File == "" || columns.Text == "" || columns.Embedding == "" {
		return memoryChunkColumns{}, fmt.Errorf(
			"memory_chunks schema missing required columns (need file, text, embedding)",
		)
	}

	return columns, nil
}

func pickColumn(available map[string]string, candidates ...string) string {
	for _, candidate := range candidates {
		if name, ok := available[strings.ToLower(candidate)]; ok {
			return name
		}
	}
	return ""
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func decodeChunkEmbedding(raw []byte) ([]float32, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty embedding")
	}

	// Accept JSON-encoded embeddings for compatibility with older importers.
	if raw[0] == '[' {
		var values []float32
		if err := json.Unmarshal(raw, &values); err == nil {
			return values, nil
		}

		var values64 []float64
		if err := json.Unmarshal(raw, &values64); err != nil {
			return nil, fmt.Errorf("invalid json embedding: %w", err)
		}

		values = make([]float32, len(values64))
		for i := range values64 {
			values[i] = float32(values64[i])
		}
		return values, nil
	}

	if len(raw)%4 != 0 {
		return nil, fmt.Errorf("invalid embedding size %d (must be multiple of 4)", len(raw))
	}

	embedding := make([]float32, len(raw)/4)
	if err := binary.Read(bytes.NewReader(raw), binary.LittleEndian, &embedding); err != nil {
		return nil, fmt.Errorf("invalid binary embedding: %w", err)
	}

	return embedding, nil
}

func normalizeEmbedding(embedding []float32) ([]float32, bool) {
	if len(embedding) == 0 {
		return nil, false
	}

	var normSquared float64
	for _, value := range embedding {
		normSquared += float64(value * value)
	}
	if normSquared == 0 {
		return nil, false
	}

	invNorm := float32(1 / math.Sqrt(normSquared))
	normalized := make([]float32, len(embedding))
	for i, value := range embedding {
		normalized[i] = value * invNorm
	}

	return normalized, true
}

func dotProduct(a, b []float32) float32 {
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}
