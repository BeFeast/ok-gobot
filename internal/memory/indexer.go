package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	defaultEmbeddingBatchSize = 32
	defaultChunkTokenLimit    = 512
	defaultChunkTokenOverlap  = 64
)

var markdownHeaderLineRegexp = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

// EmbeddingBatchClient produces embeddings for multiple inputs.
type EmbeddingBatchClient interface {
	GetEmbeddings(ctx context.Context, texts []string) ([][]float32, error)
}

type chunkKey struct {
	headerPath string
	ordinal    int
}

type indexedChunkRecord struct {
	HeaderPath   string
	ChunkOrdinal int
	Content      string
	ContentHash  string
	Embedding    []float32
}

type textSection struct {
	HeaderPath string
	Content    string
}

// IndexerOption configures Indexer behavior.
type IndexerOption func(*Indexer)

// WithIndexerBatchSize overrides the embedding batch size.
func WithIndexerBatchSize(size int) IndexerOption {
	return func(i *Indexer) {
		if size > 0 {
			i.batchSize = size
		}
	}
}

// WithIndexerChunking overrides chunk size and overlap.
func WithIndexerChunking(maxTokens, overlap int) IndexerOption {
	return func(i *Indexer) {
		if maxTokens > 0 {
			i.chunkTokens = maxTokens
		}
		if overlap >= 0 {
			i.chunkOverlap = overlap
		}
	}
}

// Indexer consumes file-change events and keeps memory_chunks synchronized.
type Indexer struct {
	rootPath string
	store    *MemoryStore
	embedder EmbeddingBatchClient

	batchSize    int
	chunkTokens  int
	chunkOverlap int
}

// NewIndexer creates an indexer rooted at a workspace path.
func NewIndexer(rootPath string, store *MemoryStore, embedder EmbeddingBatchClient, opts ...IndexerOption) *Indexer {
	absRoot := strings.TrimSpace(rootPath)
	if absRoot != "" {
		if resolved, err := filepath.Abs(absRoot); err == nil {
			absRoot = resolved
		}
		absRoot = filepath.Clean(absRoot)
	}

	indexer := &Indexer{
		rootPath:     absRoot,
		store:        store,
		embedder:     embedder,
		batchSize:    defaultEmbeddingBatchSize,
		chunkTokens:  defaultChunkTokenLimit,
		chunkOverlap: defaultChunkTokenOverlap,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(indexer)
		}
	}
	if indexer.chunkOverlap >= indexer.chunkTokens {
		indexer.chunkOverlap = 0
	}
	return indexer
}

// Consume reads file-change events until context cancellation or channel close.
func (i *Indexer) Consume(ctx context.Context, events <-chan FileChangedEvent) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := i.HandleEvent(ctx, event); err != nil {
				return err
			}
		}
	}
}

// HandleEvent processes a single file-change event.
func (i *Indexer) HandleEvent(ctx context.Context, event FileChangedEvent) error {
	return i.IndexFile(ctx, event.Path, event.RelativePath)
}

// IndexFile indexes a single file into memory_chunks.
func (i *Indexer) IndexFile(ctx context.Context, absPath, relativePath string) error {
	if i == nil || i.store == nil || i.embedder == nil {
		return fmt.Errorf("indexer is not fully configured")
	}

	sourceFile := normalizeSourceFile(i.rootPath, absPath, relativePath)
	if sourceFile == "" {
		return fmt.Errorf("source file is empty")
	}

	contentBytes, err := os.ReadFile(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return i.deleteBySourceFile(ctx, sourceFile)
		}
		return fmt.Errorf("read changed file %s: %w", absPath, err)
	}

	chunks := i.chunkFile(sourceFile, string(contentBytes))
	if len(chunks) == 0 {
		return i.deleteBySourceFile(ctx, sourceFile)
	}

	existingHashes, err := i.loadChunkHashes(ctx, sourceFile)
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

	if len(changedIndexes) > 0 {
		embeddings, err := i.embedChangedChunks(ctx, changedTexts)
		if err != nil {
			return err
		}
		for pos, chunkIndex := range changedIndexes {
			chunks[chunkIndex].Embedding = embeddings[pos]
		}
	}

	return i.persistChunks(ctx, sourceFile, chunks, changedIndexes, existingHashes)
}

func (i *Indexer) embedChangedChunks(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	allEmbeddings := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += i.batchSize {
		end := start + i.batchSize
		if end > len(texts) {
			end = len(texts)
		}

		embeddings, err := i.embedder.GetEmbeddings(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("embed batch %d-%d: %w", start, end, err)
		}
		if len(embeddings) != end-start {
			return nil, fmt.Errorf("embedding count mismatch: got %d, want %d", len(embeddings), end-start)
		}
		allEmbeddings = append(allEmbeddings, embeddings...)
	}
	return allEmbeddings, nil
}

func (i *Indexer) loadChunkHashes(ctx context.Context, sourceFile string) (map[chunkKey]string, error) {
	rows, err := i.store.db.QueryContext(
		ctx,
		`SELECT header_path, chunk_ordinal, content_hash
		 FROM memory_chunks
		 WHERE source_file = ?`,
		sourceFile,
	)
	if err != nil {
		return nil, fmt.Errorf("query existing chunk hashes for %s: %w", sourceFile, err)
	}
	defer rows.Close()

	hashes := make(map[chunkKey]string)
	for rows.Next() {
		var (
			headerPath string
			ordinal    int
			hash       string
		)
		if err := rows.Scan(&headerPath, &ordinal, &hash); err != nil {
			return nil, fmt.Errorf("scan chunk hash for %s: %w", sourceFile, err)
		}
		hashes[chunkKey{headerPath: headerPath, ordinal: ordinal}] = hash
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunk hashes for %s: %w", sourceFile, err)
	}
	return hashes, nil
}

func (i *Indexer) persistChunks(
	ctx context.Context,
	sourceFile string,
	chunks []indexedChunkRecord,
	changedIndexes []int,
	stale map[chunkKey]string,
) error {
	if len(changedIndexes) == 0 && len(stale) == 0 {
		return nil
	}

	tx, err := i.store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin memory index transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, idx := range changedIndexes {
		embeddingBytes, err := encodeEmbedding(chunks[idx].Embedding)
		if err != nil {
			return fmt.Errorf("encode embedding: %w", err)
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO memory_chunks (
				source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at
			) VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(source_file, header_path, chunk_ordinal) DO UPDATE SET
				content = excluded.content,
				content_hash = excluded.content_hash,
				embedding = excluded.embedding,
				indexed_at = CURRENT_TIMESTAMP
		`,
			sourceFile,
			chunks[idx].HeaderPath,
			chunks[idx].ChunkOrdinal,
			chunks[idx].Content,
			chunks[idx].ContentHash,
			embeddingBytes,
		)
		if err != nil {
			return fmt.Errorf("upsert chunk (%s, %s, %d): %w",
				sourceFile, chunks[idx].HeaderPath, chunks[idx].ChunkOrdinal, err)
		}
	}

	for key := range stale {
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM memory_chunks
			 WHERE source_file = ? AND header_path = ? AND chunk_ordinal = ?`,
			sourceFile,
			key.headerPath,
			key.ordinal,
		); err != nil {
			return fmt.Errorf("delete stale chunk (%s, %s, %d): %w",
				sourceFile, key.headerPath, key.ordinal, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit memory index transaction: %w", err)
	}
	return nil
}

func (i *Indexer) deleteBySourceFile(ctx context.Context, sourceFile string) error {
	_, err := i.store.db.ExecContext(
		ctx,
		`DELETE FROM memory_chunks WHERE source_file = ?`,
		sourceFile,
	)
	if err != nil {
		return fmt.Errorf("delete chunks for %s: %w", sourceFile, err)
	}
	return nil
}

func (i *Indexer) chunkFile(sourceFile, content string) []indexedChunkRecord {
	sections := splitIntoSections(sourceFile, content)
	if len(sections) == 0 {
		return nil
	}

	ordinals := make(map[string]int)
	out := make([]indexedChunkRecord, 0, len(sections))
	for _, section := range sections {
		pieces := splitChunkText(section.Content, i.chunkTokens, i.chunkOverlap)
		for _, piece := range pieces {
			trimmed := strings.TrimSpace(piece)
			if trimmed == "" {
				continue
			}

			headerPath := strings.TrimSpace(section.HeaderPath)
			if headerPath == "" {
				headerPath = "root"
			}

			ordinal := ordinals[headerPath]
			ordinals[headerPath] = ordinal + 1

			out = append(out, indexedChunkRecord{
				HeaderPath:   headerPath,
				ChunkOrdinal: ordinal,
				Content:      trimmed,
				ContentHash:  hashChunkContent(trimmed),
			})
		}
	}
	return out
}

func splitIntoSections(sourceFile, content string) []textSection {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}

	switch strings.ToLower(filepath.Ext(sourceFile)) {
	case ".md":
		return splitMarkdownSections(content)
	case ".txt", ".yaml":
		return []textSection{
			{
				HeaderPath: "root",
				Content:    trimmed,
			},
		}
	default:
		return nil
	}
}

func splitMarkdownSections(content string) []textSection {
	lines := strings.Split(content, "\n")
	stack := make([]string, 0, 6)
	currentHeader := "root"
	var buffer []string
	sections := make([]textSection, 0)

	flush := func() {
		text := strings.TrimSpace(strings.Join(buffer, "\n"))
		if text != "" {
			sections = append(sections, textSection{
				HeaderPath: currentHeader,
				Content:    text,
			})
		}
		buffer = nil
	}

	for _, line := range lines {
		matches := markdownHeaderLineRegexp.FindStringSubmatch(line)
		if len(matches) == 3 {
			flush()

			level := len(matches[1])
			title := cleanMarkdownHeader(matches[2])
			if level-1 < len(stack) {
				stack = stack[:level-1]
			}
			stack = append(stack, title)

			currentHeader = strings.Join(stack, " > ")
			if currentHeader == "" {
				currentHeader = "root"
			}

			buffer = append(buffer, strings.TrimSpace(line))
			continue
		}
		buffer = append(buffer, line)
	}

	flush()
	if len(sections) == 0 {
		return []textSection{
			{
				HeaderPath: "root",
				Content:    strings.TrimSpace(content),
			},
		}
	}
	return sections
}

func splitChunkText(text string, maxTokens, overlap int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	if maxTokens <= 0 {
		return []string{strings.Join(words, " ")}
	}
	if overlap < 0 || overlap >= maxTokens {
		overlap = 0
	}

	if len(words) <= maxTokens {
		return []string{strings.Join(words, " ")}
	}

	step := maxTokens - overlap
	chunks := make([]string, 0, len(words)/step+1)
	for start := 0; start < len(words); start += step {
		end := start + maxTokens
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, strings.Join(words[start:end], " "))
		if end == len(words) {
			break
		}
	}
	return chunks
}

func hashChunkContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func cleanMarkdownHeader(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "#")
	raw = strings.TrimSpace(raw)
	return strings.Join(strings.Fields(raw), " ")
}

func normalizeSourceFile(rootPath, absPath, relativePath string) string {
	trimmedRelative := strings.TrimSpace(relativePath)
	if trimmedRelative != "" {
		return filepath.ToSlash(filepath.Clean(trimmedRelative))
	}

	trimmedPath := strings.TrimSpace(absPath)
	if trimmedPath == "" {
		return ""
	}

	cleanPath := filepath.Clean(trimmedPath)
	if rootPath == "" {
		return filepath.ToSlash(cleanPath)
	}

	rel, err := filepath.Rel(rootPath, cleanPath)
	if err != nil {
		return filepath.ToSlash(cleanPath)
	}
	rel = filepath.Clean(rel)
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(cleanPath)
	}
	return filepath.ToSlash(rel)
}
