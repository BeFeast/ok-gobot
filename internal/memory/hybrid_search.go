package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	defaultMemoryStoreTopK      = 5
	memorySearchCandidateMin    = 50
	memorySearchCandidateMax    = 500
	memorySearchCandidateFactor = 20
	hybridLexicalWeight         = 0.45
	hybridVectorWeight          = 0.55
)

type memorySearchCandidate struct {
	result MemoryResult

	embeddingRaw     []byte
	vectorNormalized float32
	vectorScored     bool
}

type lexicalCandidate struct {
	candidate memorySearchCandidate
	bm25      float64
}

// SearchChunksHybrid combines FTS5/BM25 lexical candidates with bounded vector
// scoring. When either backend is unavailable, it falls back to the other.
func (s *MemoryStore) SearchChunksHybrid(ctx context.Context, query string, queryEmbedding []float32, topK int) ([]MemoryResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("memory store is not configured")
	}
	if topK <= 0 {
		topK = defaultMemoryStoreTopK
	}
	candidateLimit := memorySearchCandidateLimit(topK)

	candidates := make(map[int64]*memorySearchCandidate)
	lexicalUsed := false
	lexicalTokens := tokenizeSearchQuery(query)
	if len(lexicalTokens) > 0 {
		lexical, err := s.searchLexicalCandidates(ctx, buildFTS5Query(lexicalTokens), candidateLimit)
		if err != nil || !s.ftsAvailable {
			lexical, err = s.searchLikeCandidates(ctx, lexicalTokens, candidateLimit)
		}
		if err == nil {
			lexicalUsed = len(lexical) > 0
			for i := range lexical {
				candidate := lexical[i]
				candidates[candidate.result.ID] = &candidate
			}
		}
	}

	queryVector, vectorUsed := normalizeEmbedding(queryEmbedding)
	if vectorUsed {
		for _, candidate := range candidates {
			scoreCandidateVector(candidate, queryVector)
		}

		if len(candidates) < candidateLimit {
			recent, err := s.loadRecentVectorCandidates(ctx, candidateLimit-len(candidates), candidates)
			if err != nil {
				return nil, fmt.Errorf("failed to query memory vector candidates: %w", err)
			}
			for i := range recent {
				candidate := recent[i]
				scoreCandidateVector(&candidate, queryVector)
				if candidate.vectorScored {
					candidates[candidate.result.ID] = &candidate
				}
			}
		}
	}

	return rankMemoryCandidates(candidates, lexicalUsed, vectorUsed, topK), nil
}

// SearchChunks is kept as the semantic-only compatibility API. It uses the same
// bounded vector candidate path as hybrid search instead of scanning every row.
func (s *MemoryStore) SearchChunks(ctx context.Context, queryEmbedding []float32, topK int) ([]MemoryResult, error) {
	return s.SearchChunksHybrid(ctx, "", queryEmbedding, topK)
}

// Search is kept as a compatibility alias for callers that still use the old name.
func (s *MemoryStore) Search(ctx context.Context, queryEmbedding []float32, topK int) ([]MemoryResult, error) {
	return s.SearchChunks(ctx, queryEmbedding, topK)
}

func (s *MemoryStore) searchLexicalCandidates(ctx context.Context, ftsQuery string, limit int) ([]memorySearchCandidate, error) {
	if !s.ftsAvailable || ftsQuery == "" {
		return nil, fmt.Errorf("memory FTS index is unavailable")
	}

	query := fmt.Sprintf(`
		SELECT c.id, c.source_file, c.header_path, c.chunk_ordinal, c.content, c.content_hash, c.embedding, c.indexed_at,
		       bm25(%s) AS bm25_score
		FROM %s
		JOIN memory_chunks c ON c.id = %s.rowid
		WHERE %s MATCH ?
		ORDER BY bm25(%s) ASC, c.id DESC
		LIMIT ?
	`, memoryChunksFTSTable, memoryChunksFTSTable, memoryChunksFTSTable, memoryChunksFTSTable, memoryChunksFTSTable)

	rows, err := s.db.QueryContext(ctx, query, ftsQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	raw := make([]lexicalCandidate, 0, limit)
	for rows.Next() {
		candidate, bm25, err := scanLexicalCandidate(rows)
		if err != nil {
			continue
		}
		raw = append(raw, lexicalCandidate{candidate: candidate, bm25: bm25})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return normalizeLexicalCandidates(raw), nil
}

func (s *MemoryStore) searchLikeCandidates(ctx context.Context, tokens []string, limit int) ([]memorySearchCandidate, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	if len(tokens) > 8 {
		tokens = tokens[:8]
	}

	clauses := make([]string, 0, len(tokens))
	args := make([]interface{}, 0, len(tokens)+1)
	for _, token := range tokens {
		clauses = append(clauses, `LOWER(content) LIKE ?`)
		args = append(args, "%"+strings.ToLower(token)+"%")
	}
	args = append(args, limit)

	query := fmt.Sprintf(`
		SELECT id, source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at
		FROM memory_chunks
		WHERE %s
		ORDER BY indexed_at DESC, id DESC
		LIMIT ?
	`, strings.Join(clauses, " OR "))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := make([]memorySearchCandidate, 0, limit)
	for rows.Next() {
		candidate, err := scanMemorySearchCandidate(rows)
		if err != nil {
			continue
		}
		candidate.result.LexicalScore = likeLexicalScore(candidate.result.Content, tokens)
		if candidate.result.LexicalScore > 0 {
			candidates = append(candidates, candidate)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].result.LexicalScore != candidates[j].result.LexicalScore {
			return candidates[i].result.LexicalScore > candidates[j].result.LexicalScore
		}
		return candidates[i].result.ID > candidates[j].result.ID
	})
	return candidates, nil
}

func scanLexicalCandidate(scanner memoryCandidateScanner) (memorySearchCandidate, float64, error) {
	var (
		id           int64
		sourceFile   string
		headerPath   string
		chunkOrdinal int
		content      string
		contentHash  string
		embeddingRaw []byte
		indexedAt    time.Time
		bm25         float64
	)

	if err := scanner.Scan(&id, &sourceFile, &headerPath, &chunkOrdinal, &content, &contentHash, &embeddingRaw, &indexedAt, &bm25); err != nil {
		return memorySearchCandidate{}, 0, err
	}

	return newMemorySearchCandidate(id, sourceFile, headerPath, chunkOrdinal, content, contentHash, embeddingRaw, indexedAt), bm25, nil
}

func (s *MemoryStore) loadRecentVectorCandidates(ctx context.Context, limit int, existing map[int64]*memorySearchCandidate) ([]memorySearchCandidate, error) {
	if limit <= 0 {
		return nil, nil
	}
	fetchLimit := limit + len(existing)
	if fetchLimit < limit {
		fetchLimit = limit
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at
		FROM memory_chunks
		ORDER BY indexed_at DESC, id DESC
		LIMIT ?
	`, fetchLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := make([]memorySearchCandidate, 0, limit)
	for rows.Next() {
		candidate, err := scanMemorySearchCandidate(rows)
		if err != nil {
			continue
		}
		if _, ok := existing[candidate.result.ID]; ok {
			continue
		}
		candidates = append(candidates, candidate)
		if len(candidates) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return candidates, nil
}

type memoryCandidateScanner interface {
	Scan(dest ...interface{}) error
}

func scanMemorySearchCandidate(scanner memoryCandidateScanner) (memorySearchCandidate, error) {
	var (
		id           int64
		sourceFile   string
		headerPath   string
		chunkOrdinal int
		content      string
		contentHash  string
		embeddingRaw []byte
		indexedAt    time.Time
	)

	if err := scanner.Scan(&id, &sourceFile, &headerPath, &chunkOrdinal, &content, &contentHash, &embeddingRaw, &indexedAt); err != nil {
		return memorySearchCandidate{}, err
	}

	return newMemorySearchCandidate(id, sourceFile, headerPath, chunkOrdinal, content, contentHash, embeddingRaw, indexedAt), nil
}

func newMemorySearchCandidate(id int64, sourceFile, headerPath string, chunkOrdinal int, content, contentHash string, embeddingRaw []byte, indexedAt time.Time) memorySearchCandidate {
	return memorySearchCandidate{
		result: MemoryResult{
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
		},
		embeddingRaw: embeddingRaw,
	}
}

func normalizeLexicalCandidates(raw []lexicalCandidate) []memorySearchCandidate {
	if len(raw) == 0 {
		return nil
	}

	best := raw[0].bm25
	worst := raw[0].bm25
	for _, candidate := range raw[1:] {
		if candidate.bm25 < best {
			best = candidate.bm25
		}
		if candidate.bm25 > worst {
			worst = candidate.bm25
		}
	}

	out := make([]memorySearchCandidate, 0, len(raw))
	for _, item := range raw {
		score := float32(1)
		if worst > best && !math.IsNaN(item.bm25) {
			score = float32(1 - ((item.bm25 - best) / (worst - best)))
			if score < 0 {
				score = 0
			}
		}
		item.candidate.result.LexicalScore = score
		out = append(out, item.candidate)
	}
	return out
}

func scoreCandidateVector(candidate *memorySearchCandidate, queryVector []float32) {
	embedding, err := decodeChunkEmbedding(candidate.embeddingRaw)
	if err != nil {
		return
	}
	normalized, ok := normalizeEmbedding(embedding)
	if !ok || len(normalized) != len(queryVector) {
		return
	}

	cosine := dotProduct(queryVector, normalized)
	candidate.result.VectorScore = cosine
	candidate.vectorNormalized = normalizeCosineScore(cosine)
	candidate.vectorScored = true
}

func normalizeCosineScore(score float32) float32 {
	if score < -1 {
		score = -1
	}
	if score > 1 {
		score = 1
	}
	return (score + 1) / 2
}

func rankMemoryCandidates(candidates map[int64]*memorySearchCandidate, lexicalUsed, vectorUsed bool, topK int) []MemoryResult {
	if len(candidates) == 0 || topK == 0 {
		return nil
	}

	results := make([]MemoryResult, 0, len(candidates))
	for _, candidate := range candidates {
		result := candidate.result
		if lexicalUsed && vectorUsed {
			result.HybridScore = hybridLexicalWeight*result.LexicalScore + hybridVectorWeight*candidate.vectorNormalized
		} else if lexicalUsed {
			result.HybridScore = result.LexicalScore
		} else if vectorUsed && candidate.vectorScored {
			result.HybridScore = candidate.vectorNormalized
		} else {
			continue
		}

		result.Score = result.HybridScore
		result.Similarity = result.HybridScore
		if result.Score > 0 || result.LexicalScore > 0 || candidate.vectorScored {
			results = append(results, result)
		}
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if results[i].VectorScore != results[j].VectorScore {
			return results[i].VectorScore > results[j].VectorScore
		}
		if results[i].LexicalScore != results[j].LexicalScore {
			return results[i].LexicalScore > results[j].LexicalScore
		}
		if results[i].SourceFile != results[j].SourceFile {
			return results[i].SourceFile < results[j].SourceFile
		}
		if results[i].HeaderPath != results[j].HeaderPath {
			return results[i].HeaderPath < results[j].HeaderPath
		}
		return results[i].ChunkOrdinal < results[j].ChunkOrdinal
	})

	if len(results) > topK {
		results = results[:topK]
	}
	return results
}

func memorySearchCandidateLimit(topK int) int {
	if topK <= 0 {
		topK = defaultMemoryStoreTopK
	}
	limit := topK * memorySearchCandidateFactor
	if limit < memorySearchCandidateMin {
		limit = memorySearchCandidateMin
	}
	if limit > memorySearchCandidateMax {
		limit = memorySearchCandidateMax
	}
	return limit
}

func tokenizeSearchQuery(query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}

	var (
		tokens  []string
		current strings.Builder
	)
	flush := func() {
		if current.Len() == 0 {
			return
		}
		token := current.String()
		current.Reset()
		tokens = append(tokens, token)
	}

	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()

	return tokens
}

func buildFTS5Query(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(tokens))
	for _, token := range tokens {
		quoted = append(quoted, `"`+strings.ReplaceAll(token, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}

func likeLexicalScore(content string, tokens []string) float32 {
	if len(tokens) == 0 {
		return 0
	}
	content = strings.ToLower(content)
	matched := 0
	totalCount := 0
	for _, token := range tokens {
		count := strings.Count(content, strings.ToLower(token))
		if count == 0 {
			continue
		}
		matched++
		totalCount += count
	}
	if matched == 0 {
		return 0
	}

	score := float32(matched) / float32(len(tokens))
	if totalCount > matched {
		score += float32(totalCount-matched) * 0.05
	}
	if score > 1 {
		return 1
	}
	return score
}
