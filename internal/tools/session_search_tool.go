package tools

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"ok-gobot/internal/memory"
)

// SessionSearchTool performs semantic search restricted to indexed session
// transcripts. It is a thin wrapper around memory.Searcher that defaults
// the source filter to "session" and includes session metadata in the
// rendered output.
type SessionSearchTool struct {
	embedder memory.EmbeddingQueryClient
	store    *memory.MemoryStore
	source   memory.SessionTranscriptSource
}

// NewSessionSearchTool wires the agent-facing /session_search tool.
// Any of the dependencies may be nil — Execute returns a clear error in
// that case rather than panicking, which keeps the tool registry safe to
// expose even when session memory is disabled.
func NewSessionSearchTool(embedder memory.EmbeddingQueryClient, store *memory.MemoryStore, db *sql.DB) *SessionSearchTool {
	var source memory.SessionTranscriptSource
	if db != nil {
		source = memory.NewSQLiteSessionTranscriptSource(db)
	}
	return &SessionSearchTool{
		embedder: embedder,
		store:    store,
		source:   source,
	}
}

func (t *SessionSearchTool) Name() string { return "session_search" }

func (t *SessionSearchTool) Description() string {
	return "Search past session transcripts by meaning. Returns matches with the canonical session key and message id; pair with session_get to read the surrounding span."
}

func (t *SessionSearchTool) Execute(ctx context.Context, args ...string) (string, error) {
	if t == nil || t.store == nil || t.embedder == nil {
		return "", fmt.Errorf("session memory is not configured")
	}
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("usage: session_search <query> [limit]")
	}

	query := strings.TrimSpace(args[0])
	limit := memory.DefaultSearchTopK
	if len(args) > 1 {
		if n, err := strconv.Atoi(strings.TrimSpace(args[1])); err == nil && n > 0 {
			limit = n
		}
	}
	return t.run(ctx, query, limit)
}

// ExecuteJSON implements JSONExecutor so the tool-calling agent passes
// named parameters directly without going through positional adaptation.
func (t *SessionSearchTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	if t == nil || t.store == nil || t.embedder == nil {
		return "", fmt.Errorf("session memory is not configured")
	}
	query := strings.TrimSpace(params["query"])
	if query == "" {
		return "", fmt.Errorf("'query' is required")
	}
	limit := memory.DefaultSearchTopK
	if raw := strings.TrimSpace(params["limit"]); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	return t.run(ctx, query, limit)
}

func (t *SessionSearchTool) run(ctx context.Context, query string, limit int) (string, error) {
	embedding, err := t.embedder.GetEmbedding(ctx, query)
	if err != nil {
		return "", fmt.Errorf("embed query: %w", err)
	}

	searcher, err := memory.NewSearcher(ctx, t.store.DB())
	if err != nil {
		return "", fmt.Errorf("open searcher: %w", err)
	}

	hits := searcher.Search(embedding, memory.SearchOptions{
		TopK:    limit,
		Sources: []memory.SourceType{memory.SourceSession},
	})
	if len(hits) == 0 {
		return "No matching session transcripts.", nil
	}

	var out strings.Builder
	out.WriteString(fmt.Sprintf("Found %d session transcript snippet(s):\n\n", len(hits)))
	for i, hit := range hits {
		out.WriteString(fmt.Sprintf("%d. %s  (score=%.3f)\n", i+1, memory.FormatSnippetCitation(hit), hit.Score))
		// Expose the canonical session key and message id so the agent can
		// pass them to session_get. The fingerprint citation alone is a
		// one-way hash and cannot be used to address the source session.
		if hit.SessionKey != "" {
			msgID, role, ok := memory.ParseSessionChunkHeader(hit.HeaderPath)
			if ok {
				out.WriteString(fmt.Sprintf("   session_key=%s  message_id=%d  role=%s\n", hit.SessionKey, msgID, role))
			} else {
				out.WriteString(fmt.Sprintf("   session_key=%s\n", hit.SessionKey))
			}
		}
		out.WriteString(fmt.Sprintf("   %s\n\n", memory.ClipForOutput(hit.Text, 320)))
	}
	out.WriteString("Use session_get with session_key (and optional message_id) to read the surrounding span.")
	return out.String(), nil
}

func (t *SessionSearchTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Natural-language query to search past session transcripts",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of session snippets to return (default 5)",
			},
		},
		"required": []string{"query"},
	}
}

// SessionGetTool reads a span of messages from a single session transcript
// without loading the full history. It complements session_search.
type SessionGetTool struct {
	source memory.SessionTranscriptSource
}

// NewSessionGetTool builds a session_get tool backed by db. When db is nil
// the tool reports a clear configuration error instead of panicking.
func NewSessionGetTool(db *sql.DB) *SessionGetTool {
	var source memory.SessionTranscriptSource
	if db != nil {
		source = memory.NewSQLiteSessionTranscriptSource(db)
	}
	return &SessionGetTool{source: source}
}

func (t *SessionGetTool) Name() string { return "session_get" }

func (t *SessionGetTool) Description() string {
	return "Read a span of messages from a session transcript around an anchor message id. Use session_search to discover candidate session keys and message ids first."
}

func (t *SessionGetTool) Execute(ctx context.Context, args ...string) (string, error) {
	if t == nil || t.source == nil {
		return "", fmt.Errorf("session transcript source is not configured")
	}
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("usage: session_get <session-key> [message-id] [span]")
	}
	sessionKey := strings.TrimSpace(args[0])
	var anchor int64
	if len(args) > 1 {
		if n, err := strconv.ParseInt(strings.TrimSpace(args[1]), 10, 64); err == nil {
			anchor = n
		}
	}
	span := 2
	if len(args) > 2 {
		if n, err := strconv.Atoi(strings.TrimSpace(args[2])); err == nil && n >= 0 {
			span = n
		}
	}
	return t.run(ctx, sessionKey, anchor, span)
}

// ExecuteJSON implements JSONExecutor so message_id and span are read by
// name, not by Go map iteration order. Without this, the generic positional
// fallback can swap fields when the model supplies both numeric values.
func (t *SessionGetTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	if t == nil || t.source == nil {
		return "", fmt.Errorf("session transcript source is not configured")
	}
	sessionKey := strings.TrimSpace(params["session_key"])
	if sessionKey == "" {
		return "", fmt.Errorf("'session_key' is required")
	}
	var anchor int64
	if raw := strings.TrimSpace(params["message_id"]); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			anchor = n
		}
	}
	span := 2
	if raw := strings.TrimSpace(params["span"]); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			span = n
		}
	}
	return t.run(ctx, sessionKey, anchor, span)
}

func (t *SessionGetTool) run(ctx context.Context, sessionKey string, anchor int64, span int) (string, error) {
	result, err := memory.LoadSessionSpan(ctx, t.source, sessionKey, anchor, span)
	if err != nil {
		return "", fmt.Errorf("load session span: %w", err)
	}
	if len(result.Messages) == 0 {
		return "No messages found.", nil
	}

	var out strings.Builder
	out.WriteString(fmt.Sprintf("Session: %s (fingerprint=%s)\n", sessionKey, memory.SessionKeyFingerprint(sessionKey)))
	if anchor > 0 {
		out.WriteString(fmt.Sprintf("Anchor: msg %d, span ±%d\n", anchor, span))
	}
	out.WriteString(fmt.Sprintf("Messages: %d\n\n", len(result.Messages)))
	for _, msg := range result.Messages {
		marker := " "
		if msg.ID == anchor {
			marker = ">"
		}
		clean := memory.SanitizeMessageContent(msg.Content)
		out.WriteString(fmt.Sprintf("%s msg %d [%s] @ %s\n", marker, msg.ID, msg.Role, msg.CreatedAt))
		out.WriteString(fmt.Sprintf("  %s\n\n", memory.ClipForOutput(clean, 800)))
	}
	return out.String(), nil
}

func (t *SessionGetTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"session_key": map[string]interface{}{
				"type":        "string",
				"description": "Canonical session key (e.g. agent:default:telegram:dm:42)",
			},
			"message_id": map[string]interface{}{
				"type":        "integer",
				"description": "Anchor message id; the surrounding window is centered on it",
			},
			"span": map[string]interface{}{
				"type":        "integer",
				"description": "Number of messages before and after the anchor (default 2)",
			},
		},
		"required": []string{"session_key"},
	}
}
