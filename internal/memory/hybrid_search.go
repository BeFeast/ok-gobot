package memory

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	memoryLexicalCandidateMultiplier = 20
	memoryVectorCandidateMultiplier  = 50
	memoryMaxLexicalCandidates       = 200
	memoryMaxVectorCandidates        = 1000
	memoryHybridLexicalWeight        = 0.45
	memoryHybridVectorWeight         = 0.55
)

var memorySearchTokenRegexp = regexp.MustCompile(`[\p{L}\p{N}_]+`)

type memorySearchCandidate struct {
	result MemoryResult

	lexicalRank  int
	lexicalScore float32
	bm25         float64
	hasLexical   bool

	vectorScore float32
	hasVector   bool
}

type memoryVectorCandidate struct {
	result       MemoryResult
	embeddingRaw []byte
}

// SearchText searches memory chunks using the lexical index only.
func (s *MemoryStore) SearchText(ctx context.Context, query string, topK int) ([]MemoryResult, error) {
	return s.SearchHybrid(ctx, query, nil, topK)
}

// SearchHybrid combines FTS5/BM25 lexical candidates with bounded vector scoring.
func (s *MemoryStore) SearchHybrid(ctx context.Context, query string, queryEmbedding []float32, topK int) ([]MemoryResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("memory store is not configured")
	}

	topK = normalizeMemoryTopK(topK)
	query = strings.TrimSpace(query)
	queryVector, hasQueryVector := normalizeEmbedding(queryEmbedding)

	candidates := make(map[int64]*memorySearchCandidate)
	lexicalCount := 0

	if query != "" {
		lexicalLimit := memoryCandidateLimit(topK, memoryLexicalCandidateMultiplier, memoryMaxLexicalCandidates)
		lexical, err := s.searchLexicalCandidates(ctx, query, lexicalLimit)
		if err != nil {
			fallback, fallbackErr := s.searchLikeCandidates(ctx, query, lexicalLimit)
			if fallbackErr != nil {
				if !hasQueryVector {
					return nil, fallbackErr
				}
			} else {
				lexical = fallback
				err = nil
			}
		}
		if err != nil && !hasQueryVector {
			return nil, err
		}
		for rank, candidate := range lexical {
			entry := ensureMemorySearchCandidate(candidates, candidate.result)
			entry.hasLexical = true
			entry.lexicalRank = rank
			entry.lexicalScore = lexicalRankScore(rank)
			entry.bm25 = candidate.bm25
			lexicalCount++
		}
	}

	if hasQueryVector {
		vectorLimit := memoryCandidateLimit(topK, memoryVectorCandidateMultiplier, memoryMaxVectorCandidates)
		vectorRows, err := s.searchVectorCandidateRows(ctx, memoryCandidateIDs(candidates), vectorLimit)
		if err != nil {
			return nil, err
		}
		for _, row := range vectorRows {
			embedding, err := decodeChunkEmbedding(row.embeddingRaw)
			if err != nil {
				continue
			}
			normalized, ok := normalizeEmbedding(embedding)
			if !ok || len(normalized) != len(queryVector) {
				continue
			}

			score := dotProduct(queryVector, normalized)
			entry := ensureMemorySearchCandidate(candidates, row.result)
			entry.hasVector = true
			entry.vectorScore = score
		}
	}

	return rankMemoryCandidates(candidates, topK, hasQueryVector && lexicalCount > 0), nil
}

func (s *MemoryStore) searchLexicalCandidates(ctx context.Context, query string, limit int) ([]memorySearchCandidate, error) {
	if !s.ftsAvailable {
		return nil, errMemoryFTSUnavailable
	}

	ftsQuery := buildMemoryFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT c.id, c.source_file, c.header_path, c.chunk_ordinal, c.content, c.content_hash, c.indexed_at,
		       bm25(%s) AS rank
		FROM %s
		JOIN %s AS c ON c.id = %s.rowid
		WHERE %s MATCH ?
		ORDER BY rank ASC, c.indexed_at DESC, c.id DESC
		LIMIT ?
	`, memoryChunksFTSTable, memoryChunksFTSTable, memoryChunksTable, memoryChunksFTSTable, memoryChunksFTSTable), ftsQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanLexicalRows(rows)
}

func (s *MemoryStore) searchLikeCandidates(ctx context.Context, query string, limit int) ([]memorySearchCandidate, error) {
	terms := memorySearchTerms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	if len(terms) > 5 {
		terms = terms[:5]
	}

	conditions := make([]string, 0, len(terms))
	args := make([]interface{}, 0, len(terms)+1)
	for _, term := range terms {
		conditions = append(conditions, `content LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLikeTerm(term)+"%")
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, source_file, header_path, chunk_ordinal, content, content_hash, indexed_at
		FROM %s
		WHERE %s
		ORDER BY indexed_at DESC, id DESC
		LIMIT ?
	`, memoryChunksTable, strings.Join(conditions, " OR ")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := make([]memorySearchCandidate, 0, limit)
	for rows.Next() {
		result, err := scanMemoryResult(rows)
		if err != nil {
			continue
		}
		candidates = append(candidates, memorySearchCandidate{result: result})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

func (s *MemoryStore) searchVectorCandidateRows(ctx context.Context, ids []int64, limit int) ([]memoryVectorCandidate, error) {
	seen := make(map[int64]bool)
	candidates := make([]memoryVectorCandidate, 0, limit+len(ids))

	if len(ids) > 0 {
		placeholders := make([]string, len(ids))
		args := make([]interface{}, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT id, source_file, header_path, chunk_ordinal, content, content_hash, indexed_at, embedding
			FROM %s
			WHERE id IN (%s)
		`, memoryChunksTable, strings.Join(placeholders, ",")), args...)
		if err != nil {
			return nil, err
		}
		loaded, err := scanVectorRows(rows)
		if err != nil {
			return nil, err
		}
		for _, row := range loaded {
			seen[row.result.ID] = true
			candidates = append(candidates, row)
		}
	}

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, source_file, header_path, chunk_ordinal, content, content_hash, indexed_at, embedding
		FROM %s
		ORDER BY indexed_at DESC, id DESC
		LIMIT ?
	`, memoryChunksTable), limit)
	if err != nil {
		return nil, err
	}
	loaded, err := scanVectorRows(rows)
	if err != nil {
		return nil, err
	}
	for _, row := range loaded {
		if seen[row.result.ID] {
			continue
		}
		seen[row.result.ID] = true
		candidates = append(candidates, row)
	}

	return candidates, nil
}

func scanLexicalRows(rows *sql.Rows) ([]memorySearchCandidate, error) {
	candidates := make([]memorySearchCandidate, 0)
	for rows.Next() {
		var bm25 float64
		result, err := scanMemoryResultWithExtra(rows, &bm25)
		if err != nil {
			continue
		}
		candidates = append(candidates, memorySearchCandidate{result: result, bm25: bm25})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

func scanVectorRows(rows *sql.Rows) ([]memoryVectorCandidate, error) {
	defer rows.Close()

	candidates := make([]memoryVectorCandidate, 0)
	for rows.Next() {
		var embeddingRaw []byte
		result, err := scanMemoryResultWithExtra(rows, &embeddingRaw)
		if err != nil {
			continue
		}
		candidates = append(candidates, memoryVectorCandidate{result: result, embeddingRaw: embeddingRaw})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

func scanMemoryResult(rows interface {
	Scan(dest ...interface{}) error
}) (MemoryResult, error) {
	return scanMemoryResultWithExtra(rows)
}

func scanMemoryResultWithExtra(rows interface {
	Scan(dest ...interface{}) error
}, extra ...interface{}) (MemoryResult, error) {
	var (
		id           int64
		sourceFile   string
		headerPath   string
		chunkOrdinal int
		content      string
		contentHash  string
		indexedAt    time.Time
	)

	dest := []interface{}{
		&id,
		&sourceFile,
		&headerPath,
		&chunkOrdinal,
		&content,
		&contentHash,
		&indexedAt,
	}
	dest = append(dest, extra...)
	if err := rows.Scan(dest...); err != nil {
		return MemoryResult{}, err
	}

	return MemoryResult{
		ID:           id,
		Source:       sourceFile,
		SourceFile:   sourceFile,
		HeaderPath:   headerPath,
		StartLine:    chunkOrdinal,
		EndLine:      chunkOrdinal,
		ChunkOrdinal: chunkOrdinal,
		Content:      content,
		ContentHash:  contentHash,
		UpdatedAt:    indexedAt,
		IndexedAt:    indexedAt,
	}, nil
}

func ensureMemorySearchCandidate(candidates map[int64]*memorySearchCandidate, result MemoryResult) *memorySearchCandidate {
	if entry, ok := candidates[result.ID]; ok {
		return entry
	}
	entry := &memorySearchCandidate{result: result, lexicalRank: int(^uint(0) >> 1)}
	candidates[result.ID] = entry
	return entry
}

func rankMemoryCandidates(candidates map[int64]*memorySearchCandidate, topK int, useHybridWeights bool) []MemoryResult {
	if len(candidates) == 0 {
		return nil
	}

	ordered := make([]*memorySearchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if !candidate.hasLexical && !candidate.hasVector {
			continue
		}
		score := candidate.combinedScore(useHybridWeights)
		candidate.result.Score = score
		candidate.result.Similarity = score
		candidate.result.HybridScore = score
		if candidate.hasLexical {
			candidate.result.LexicalScore = candidate.lexicalScore
			candidate.result.BM25 = candidate.bm25
		}
		if candidate.hasVector {
			candidate.result.VectorScore = candidate.vectorScore
		}
		ordered = append(ordered, candidate)
	}

	sort.SliceStable(ordered, func(i, j int) bool {
		left := ordered[i]
		right := ordered[j]
		if left.result.Score != right.result.Score {
			return left.result.Score > right.result.Score
		}
		if left.vectorScore != right.vectorScore {
			return left.vectorScore > right.vectorScore
		}
		if left.lexicalRank != right.lexicalRank {
			return left.lexicalRank < right.lexicalRank
		}
		if !left.result.IndexedAt.Equal(right.result.IndexedAt) {
			return left.result.IndexedAt.After(right.result.IndexedAt)
		}
		return left.result.ID < right.result.ID
	})

	if len(ordered) > topK {
		ordered = ordered[:topK]
	}
	results := make([]MemoryResult, len(ordered))
	for i, candidate := range ordered {
		results[i] = candidate.result
	}
	return results
}

func (c *memorySearchCandidate) combinedScore(useHybridWeights bool) float32 {
	if !useHybridWeights {
		if c.hasVector {
			return c.vectorScore
		}
		return c.lexicalScore
	}

	var score float32
	if c.hasLexical {
		score += memoryHybridLexicalWeight * c.lexicalScore
	}
	if c.hasVector && c.vectorScore > 0 {
		score += memoryHybridVectorWeight * c.vectorScore
	}
	return score
}

func normalizeMemoryTopK(topK int) int {
	if topK <= 0 {
		return DefaultSearchTopK
	}
	return topK
}

func memoryCandidateLimit(topK, multiplier, maxLimit int) int {
	limit := topK * multiplier
	if limit < topK {
		limit = topK
	}
	if limit > maxLimit {
		if topK > maxLimit {
			return topK
		}
		return maxLimit
	}
	return limit
}

func memoryCandidateIDs(candidates map[int64]*memorySearchCandidate) []int64 {
	ids := make([]int64, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func lexicalRankScore(rank int) float32 {
	return 1 / float32(rank+1)
}

func buildMemoryFTSQuery(query string) string {
	terms := memorySearchTerms(query)
	if len(terms) == 0 {
		return ""
	}

	quoted := make([]string, len(terms))
	for i, term := range terms {
		quoted[i] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " OR ")
}

func memorySearchTerms(query string) []string {
	raw := memorySearchTokenRegexp.FindAllString(query, -1)
	terms := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, term := range raw {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		key := strings.ToLower(term)
		if seen[key] {
			continue
		}
		seen[key] = true
		terms = append(terms, term)
	}
	return terms
}

func escapeLikeTerm(term string) string {
	term = strings.ReplaceAll(term, `\`, `\\`)
	term = strings.ReplaceAll(term, `%`, `\%`)
	term = strings.ReplaceAll(term, `_`, `\_`)
	return term
}
