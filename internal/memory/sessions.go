package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"ok-gobot/internal/redact"
	"ok-gobot/internal/sanitize"
)

// SessionMessage is one row of a stored session transcript that the indexer
// consumes. It mirrors the shape of storage.SessionMessageV2 but lives in
// the memory package to keep the dependency direction one-way.
type SessionMessage struct {
	ID         int64
	SessionKey string
	Role       string
	Content    string
	RunID      string
	CreatedAt  string
}

// SessionTranscriptSource enumerates the sessions that should be indexed.
// Implementations are typically backed by storage.Store.
type SessionTranscriptSource interface {
	// ListSessionKeysForIndexing returns canonical session keys eligible
	// for transcript indexing. Privacy filtering (e.g. group sessions)
	// is applied by the indexer, not by the source.
	ListSessionKeysForIndexing(ctx context.Context) ([]string, error)
	// LoadSessionMessages returns transcript messages for sessionKey in
	// chronological order.
	LoadSessionMessages(ctx context.Context, sessionKey string) ([]SessionMessage, error)
}

// SessionIndexOptions configures session transcript indexing behavior.
type SessionIndexOptions struct {
	// IncludeGroups, when true, indexes group-keyed sessions. By default
	// only DM and main sessions are indexed for privacy.
	IncludeGroups bool
	// MaxMessagesPerSession limits how many messages from a session are
	// indexed. 0 means no limit. The most recent N are kept.
	MaxMessagesPerSession int
	// OnlyKeys, when non-empty, restricts indexing to the listed session
	// keys. Useful for ad-hoc reindexing of a single session.
	OnlyKeys []string
}

// SessionIndexStats describes a single index pass over session transcripts.
type SessionIndexStats struct {
	SessionsConsidered int
	SessionsIndexed    int
	SessionsSkipped    int
	MessagesIndexed    int
}

// SessionRoleHeader is the exact regular expression that matches a session
// chunk's header_path. It is exposed so that callers (e.g. the CLI) can
// parse search results back into (message_id, role) pairs.
var SessionRoleHeader = regexp.MustCompile(`^msg\s+(\d+)\s+\[([^\]]+)\]$`)

// SessionRoles enumerates the roles that are eligible for indexing. Other
// roles (system, tool, etc.) are skipped because they are either machine-
// generated or contain prompt scaffolding rather than user-meaningful turns.
var SessionRoles = map[string]struct{}{
	"user":      {},
	"assistant": {},
}

// IsGroupSessionKey reports whether sessionKey targets a shared group chat.
// Group sessions hold messages from multiple users and are excluded from
// the default index for privacy.
func IsGroupSessionKey(sessionKey string) bool {
	return strings.Contains(sessionKey, ":telegram:group:")
}

// SanitizeMessageContent prepares raw message content for indexing.
// It strips control characters and redacts well-known secrets, but keeps
// the textual meaning intact so semantic search remains useful.
func SanitizeMessageContent(content string) string {
	if content == "" {
		return ""
	}
	content = sanitize.StripControlChars(content)
	content = redact.Redact(content)
	return strings.TrimSpace(content)
}

// SessionChunkHeader returns the canonical header_path for a session chunk.
// Pairing message_id and role makes it possible to reconstruct the original
// span on demand and to display search hits as `msg <id> [<role>]`.
func SessionChunkHeader(messageID int64, role string) string {
	return fmt.Sprintf("msg %d [%s]", messageID, strings.ToLower(strings.TrimSpace(role)))
}

// ParseSessionChunkHeader extracts the message id and role from a header
// previously produced by SessionChunkHeader.
func ParseSessionChunkHeader(header string) (messageID int64, role string, ok bool) {
	matches := SessionRoleHeader.FindStringSubmatch(strings.TrimSpace(header))
	if len(matches) != 3 {
		return 0, "", false
	}
	id, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return id, matches[2], true
}

// SessionIndexer persists session transcript chunks into memory_chunks.
// It shares the underlying embedding pipeline with the file-based Indexer
// but reads from a SessionTranscriptSource instead of the filesystem.
type SessionIndexer struct {
	store    *MemoryStore
	embedder EmbeddingBatchClient
	source   SessionTranscriptSource

	chunkTokens  int
	chunkOverlap int
	batchSize    int
}

// NewSessionIndexer constructs a SessionIndexer. The chunker reuses the
// same word-level chunking as the file indexer to keep retrieval behavior
// consistent across source types.
func NewSessionIndexer(store *MemoryStore, embedder EmbeddingBatchClient, source SessionTranscriptSource, opts ...IndexerOption) *SessionIndexer {
	idx := &SessionIndexer{
		store:        store,
		embedder:     embedder,
		source:       source,
		chunkTokens:  defaultChunkTokenLimit,
		chunkOverlap: defaultChunkTokenOverlap,
		batchSize:    defaultEmbeddingBatchSize,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		stub := &Indexer{
			batchSize:    idx.batchSize,
			chunkTokens:  idx.chunkTokens,
			chunkOverlap: idx.chunkOverlap,
		}
		opt(stub)
		idx.batchSize = stub.batchSize
		idx.chunkTokens = stub.chunkTokens
		idx.chunkOverlap = stub.chunkOverlap
	}
	if idx.chunkOverlap >= idx.chunkTokens {
		idx.chunkOverlap = 0
	}
	return idx
}

// IndexSessions runs a single indexing pass according to opts. Sessions
// excluded by privacy rules contribute to SessionsSkipped but never produce
// chunks.
func (s *SessionIndexer) IndexSessions(ctx context.Context, opts SessionIndexOptions) (SessionIndexStats, error) {
	if s == nil || s.store == nil || s.embedder == nil || s.source == nil {
		return SessionIndexStats{}, fmt.Errorf("session indexer is not fully configured")
	}

	keys := opts.OnlyKeys
	if len(keys) == 0 {
		listed, err := s.source.ListSessionKeysForIndexing(ctx)
		if err != nil {
			return SessionIndexStats{}, fmt.Errorf("list session keys: %w", err)
		}
		keys = listed
	}

	stats := SessionIndexStats{}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		stats.SessionsConsidered++
		if !opts.IncludeGroups && IsGroupSessionKey(key) {
			stats.SessionsSkipped++
			continue
		}

		messages, err := s.source.LoadSessionMessages(ctx, key)
		if err != nil {
			return stats, fmt.Errorf("load messages for %s: %w", key, err)
		}
		if opts.MaxMessagesPerSession > 0 && len(messages) > opts.MaxMessagesPerSession {
			messages = messages[len(messages)-opts.MaxMessagesPerSession:]
		}

		messagesIndexed, err := s.indexSession(ctx, key, messages)
		if err != nil {
			return stats, err
		}
		if messagesIndexed > 0 {
			stats.SessionsIndexed++
			stats.MessagesIndexed += messagesIndexed
		} else {
			stats.SessionsSkipped++
		}
	}
	return stats, nil
}

// indexSession upserts every eligible message of a single session.
func (s *SessionIndexer) indexSession(ctx context.Context, sessionKey string, messages []SessionMessage) (int, error) {
	sourceFile := SessionSourceFile(sessionKey)

	existing, err := s.loadExistingHeaders(ctx, sourceFile)
	if err != nil {
		return 0, err
	}

	type pending struct {
		header  string
		ordinal int
		content string
		hash    string
	}

	var (
		toEmbed   []pending
		seenKeys  = make(map[chunkKey]struct{})
		processed int
	)

	for _, msg := range messages {
		if _, ok := SessionRoles[strings.ToLower(strings.TrimSpace(msg.Role))]; !ok {
			continue
		}
		clean := SanitizeMessageContent(msg.Content)
		if clean == "" {
			continue
		}
		header := SessionChunkHeader(msg.ID, msg.Role)
		pieces := splitChunkText(clean, s.chunkTokens, s.chunkOverlap)
		if len(pieces) == 0 {
			continue
		}
		processed++
		for ordinal, piece := range pieces {
			text := strings.TrimSpace(piece)
			if text == "" {
				continue
			}
			key := chunkKey{headerPath: header, ordinal: ordinal}
			seenKeys[key] = struct{}{}
			hash := hashChunkContent(text)
			if prev, ok := existing[key]; ok && prev == hash {
				continue
			}
			toEmbed = append(toEmbed, pending{
				header:  header,
				ordinal: ordinal,
				content: text,
				hash:    hash,
			})
		}
	}

	// Anything in `existing` that we did not see is now stale.
	stale := make(map[chunkKey]string)
	for k, v := range existing {
		if _, ok := seenKeys[k]; !ok {
			stale[k] = v
		}
	}

	if len(toEmbed) == 0 && len(stale) == 0 {
		return processed, nil
	}

	embeddings := make([][]float32, 0, len(toEmbed))
	if len(toEmbed) > 0 {
		texts := make([]string, len(toEmbed))
		for i, p := range toEmbed {
			texts[i] = p.content
		}
		for start := 0; start < len(texts); start += s.batchSize {
			end := start + s.batchSize
			if end > len(texts) {
				end = len(texts)
			}
			batch, err := s.embedder.GetEmbeddings(ctx, texts[start:end])
			if err != nil {
				return processed, fmt.Errorf("embed session batch: %w", err)
			}
			if len(batch) != end-start {
				return processed, fmt.Errorf("embedding count mismatch for session: got %d want %d", len(batch), end-start)
			}
			embeddings = append(embeddings, batch...)
		}
	}

	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return processed, fmt.Errorf("begin session index tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for i, p := range toEmbed {
		blob, err := encodeEmbedding(embeddings[i])
		if err != nil {
			return processed, fmt.Errorf("encode session embedding: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO memory_chunks (
				source_file, header_path, chunk_ordinal, content, content_hash, embedding, indexed_at
			) VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(source_file, header_path, chunk_ordinal) DO UPDATE SET
				content = excluded.content,
				content_hash = excluded.content_hash,
				embedding = excluded.embedding,
				indexed_at = CURRENT_TIMESTAMP
		`, sourceFile, p.header, p.ordinal, p.content, p.hash, blob); err != nil {
			return processed, fmt.Errorf("upsert session chunk: %w", err)
		}
	}

	for key := range stale {
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM memory_chunks WHERE source_file = ? AND header_path = ? AND chunk_ordinal = ?`,
			sourceFile, key.headerPath, key.ordinal,
		); err != nil {
			return processed, fmt.Errorf("delete stale session chunk: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return processed, fmt.Errorf("commit session index tx: %w", err)
	}
	return processed, nil
}

func (s *SessionIndexer) loadExistingHeaders(ctx context.Context, sourceFile string) (map[chunkKey]string, error) {
	rows, err := s.store.db.QueryContext(
		ctx,
		`SELECT header_path, chunk_ordinal, content_hash FROM memory_chunks WHERE source_file = ?`,
		sourceFile,
	)
	if err != nil {
		return nil, fmt.Errorf("query existing session chunks: %w", err)
	}
	defer rows.Close()

	out := make(map[chunkKey]string)
	for rows.Next() {
		var (
			header  string
			ordinal int
			hash    string
		)
		if err := rows.Scan(&header, &ordinal, &hash); err != nil {
			return nil, err
		}
		out[chunkKey{headerPath: header, ordinal: ordinal}] = hash
	}
	return out, rows.Err()
}

// ClearSessionChunks removes every indexed transcript chunk. Useful when
// disabling session memory and wanting a clean slate.
func (s *MemoryStore) ClearSessionChunks(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("memory store is not configured")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM memory_chunks WHERE source_file LIKE ?`, SessionSourceFilePrefix+"%")
	if err != nil {
		return fmt.Errorf("clear session chunks: %w", err)
	}
	return nil
}

// ClearSessionChunksForKey removes indexed transcript chunks for a single
// session, e.g. when the user invokes /new and wants the session forgotten.
func (s *MemoryStore) ClearSessionChunksForKey(ctx context.Context, sessionKey string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("memory store is not configured")
	}
	if strings.TrimSpace(sessionKey) == "" {
		return fmt.Errorf("session key is empty")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM memory_chunks WHERE source_file = ?`, SessionSourceFile(sessionKey))
	return err
}

// SessionSpan is a contiguous slice of session messages returned when
// expanding a search hit back into full transcript context.
type SessionSpan struct {
	SessionKey string
	Anchor     int64
	Messages   []SessionMessage
}

// LoadSessionSpan returns up to (1+2*span) messages centered on anchorID
// from the given session. If anchorID is 0 the most recent span+1 messages
// are returned. Sources implementing this method should sort messages by
// id ascending.
func LoadSessionSpan(ctx context.Context, source SessionTranscriptSource, sessionKey string, anchorID int64, span int) (SessionSpan, error) {
	if source == nil {
		return SessionSpan{}, fmt.Errorf("session source is nil")
	}
	msgs, err := source.LoadSessionMessages(ctx, sessionKey)
	if err != nil {
		return SessionSpan{}, fmt.Errorf("load messages: %w", err)
	}
	if span < 0 {
		span = 0
	}
	if anchorID <= 0 || len(msgs) == 0 {
		// Tail-most slice when no anchor is provided.
		size := span*2 + 1
		if size <= 0 || size > len(msgs) {
			size = len(msgs)
		}
		return SessionSpan{
			SessionKey: sessionKey,
			Messages:   msgs[len(msgs)-size:],
		}, nil
	}
	idx := -1
	for i, m := range msgs {
		if m.ID == anchorID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return SessionSpan{SessionKey: sessionKey, Anchor: anchorID}, nil
	}
	start := idx - span
	if start < 0 {
		start = 0
	}
	end := idx + span + 1
	if end > len(msgs) {
		end = len(msgs)
	}
	return SessionSpan{
		SessionKey: sessionKey,
		Anchor:     anchorID,
		Messages:   msgs[start:end],
	}, nil
}

// FormatSnippetCitation renders a one-line citation describing a memory
// snippet. The citation always identifies the source type, and for session
// snippets it includes the session key fingerprint and message id so users
// can locate the original turn without exposing raw transcript content.
func FormatSnippetCitation(snippet MemorySnippet) string {
	source := snippet.SourceType
	if source == "" {
		source = DeriveSourceType(snippet.File)
	}
	switch source {
	case SourceSession:
		key := snippet.SessionKey
		if key == "" {
			key, _ = SessionKeyFromSourceFile(snippet.File)
		}
		fingerprint := SessionKeyFingerprint(key)
		msgID, role, ok := ParseSessionChunkHeader(snippet.HeaderPath)
		if ok {
			return fmt.Sprintf("[session %s] msg %d [%s]", fingerprint, msgID, role)
		}
		return fmt.Sprintf("[session %s] %s", fingerprint, snippet.HeaderPath)
	case SourceWorkspace:
		return fmt.Sprintf("[workspace] %s :: %s", snippet.File, snippet.HeaderPath)
	case SourceDaily:
		return fmt.Sprintf("[daily] %s :: %s", snippet.File, snippet.HeaderPath)
	default:
		return fmt.Sprintf("[%s] %s :: %s", source, snippet.File, snippet.HeaderPath)
	}
}

// SessionKeyFingerprint returns a short, deterministic identifier suitable
// for displaying a session key in logs or telegram output without exposing
// the full per-user identifier.
func SessionKeyFingerprint(sessionKey string) string {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionKey))
	return hex.EncodeToString(sum[:4])
}

// ListSessionKeys returns every session_key that currently has at least one
// indexed chunk. Useful for diagnostics and reindex flows.
func (s *MemoryStore) ListSessionKeys(ctx context.Context) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("memory store is not configured")
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT DISTINCT source_file FROM memory_chunks WHERE source_file LIKE ? ORDER BY source_file ASC`,
		SessionSourceFilePrefix+"%",
	)
	if err != nil {
		return nil, fmt.Errorf("list session source files: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var sourceFile string
		if err := rows.Scan(&sourceFile); err != nil {
			return nil, err
		}
		key, ok := SessionKeyFromSourceFile(sourceFile)
		if !ok {
			continue
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// Compile-time assertion that *sql.DB satisfies the queries we rely on.
var _ = (*sql.DB)(nil)
