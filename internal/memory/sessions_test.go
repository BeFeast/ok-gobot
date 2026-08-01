package memory

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
)

func TestClipForOutputRespectsRuneBoundary(t *testing.T) {
	// Each rocket is 4 bytes; max=10 means we can fit 2 runes (8 bytes) +
	// ellipsis. The cut must never land mid-codepoint.
	in := strings.Repeat("🚀", 5)
	got := ClipForOutput(in, 10)
	if !utf8.ValidString(got) {
		t.Fatalf("ClipForOutput produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
	// Strip the ellipsis and confirm only whole runes remain.
	body := strings.TrimSuffix(got, "…")
	if len(body) != 8 {
		t.Errorf("expected 2 emojis (8 bytes) before ellipsis, got %d bytes: %q", len(body), body)
	}
}

func TestClipForOutputBelowLimitReturnsInput(t *testing.T) {
	if got := ClipForOutput("hello", 100); got != "hello" {
		t.Errorf("short input mutated: %q", got)
	}
	if got := ClipForOutput("  hello  ", 100); got != "hello" {
		t.Errorf("expected trim, got %q", got)
	}
}

type stubSessionSource struct {
	keys     []string
	messages map[string][]SessionMessage
}

func (s *stubSessionSource) ListSessionKeysForIndexing(_ context.Context) ([]string, error) {
	return append([]string(nil), s.keys...), nil
}

func (s *stubSessionSource) LoadSessionMessages(_ context.Context, sessionKey string) ([]SessionMessage, error) {
	msgs := s.messages[sessionKey]
	out := make([]SessionMessage, len(msgs))
	copy(out, msgs)
	return out, nil
}

func newStubSessionSource(keys ...string) *stubSessionSource {
	return &stubSessionSource{
		keys:     keys,
		messages: make(map[string][]SessionMessage),
	}
}

func TestIsGroupSessionKey(t *testing.T) {
	cases := map[string]bool{
		"agent:default:main":                        false,
		"agent:default:telegram:dm:42":              false,
		"agent:default:telegram:group:101":          true,
		"agent:default:telegram:group:101:thread:7": true,
		"agent:default:subagent:run-1":              false,
	}
	for key, want := range cases {
		if got := IsGroupSessionKey(key); got != want {
			t.Errorf("IsGroupSessionKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestSanitizeMessageContentRedactsSecretsAndStripsControlChars(t *testing.T) {
	raw := "remember sk-secretAPIKEYvalue1234567890\x07 ok? `Bearer abc.def.ghi`"
	clean := SanitizeMessageContent(raw)
	if strings.Contains(clean, "sk-secretAPIKEYvalue1234567890") {
		t.Fatalf("expected api key to be redacted, got: %q", clean)
	}
	if strings.Contains(clean, "\x07") {
		t.Fatalf("expected control characters to be stripped, got: %q", clean)
	}
	if strings.Contains(clean, "Bearer abc.def.ghi") {
		t.Fatalf("expected bearer token to be redacted, got: %q", clean)
	}
}

func TestSessionChunkHeaderRoundTrip(t *testing.T) {
	header := SessionChunkHeader(42, "User")
	if header != "msg 42 [user]" {
		t.Fatalf("unexpected header: %q", header)
	}
	id, role, ok := ParseSessionChunkHeader(header)
	if !ok || id != 42 || role != "user" {
		t.Fatalf("ParseSessionChunkHeader = (%d, %q, %v)", id, role, ok)
	}
	if _, _, ok := ParseSessionChunkHeader("garbage"); ok {
		t.Fatalf("expected ParseSessionChunkHeader to fail on bad input")
	}
}

func TestDeriveSourceTypeAndSessionFile(t *testing.T) {
	if got := DeriveSourceType("MEMORY.md"); got != SourceWorkspace {
		t.Errorf("workspace: got %q", got)
	}
	if got := DeriveSourceType("memory/2026-04-29.md"); got != SourceDaily {
		t.Errorf("daily: got %q", got)
	}
	if got := DeriveSourceType("notes/random.md"); got != SourceExtra {
		t.Errorf("extra: got %q", got)
	}
	source := SessionSourceFile("agent:default:telegram:dm:1")
	if !strings.HasPrefix(source, SessionSourceFilePrefix) {
		t.Fatalf("session source file missing prefix: %q", source)
	}
	if got := DeriveSourceType(source); got != SourceSession {
		t.Errorf("session: got %q", got)
	}
	got, ok := SessionKeyFromSourceFile(source)
	if !ok || got != "agent:default:telegram:dm:1" {
		t.Errorf("SessionKeyFromSourceFile = (%q, %v)", got, ok)
	}
}

func TestNormalizeSourceTypes(t *testing.T) {
	got := NormalizeSourceTypes([]string{"workspace", "Workspace", "Sessions", "memory", "garbage", ""})
	want := []SourceType{SourceWorkspace, SourceSession}
	if len(got) != len(want) {
		t.Fatalf("normalized = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("normalized[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got := NormalizeSourceTypes(nil); got != nil {
		t.Errorf("nil input should return nil, got %v", got)
	}
}

func TestSessionIndexerSkipsGroupSessionsByDefault(t *testing.T) {
	db := openIndexerTestDB(t)
	defer db.Close() //nolint:errcheck

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	dmKey := "agent:default:telegram:dm:1"
	groupKey := "agent:default:telegram:group:99"
	source := newStubSessionSource(dmKey, groupKey)
	source.messages[dmKey] = []SessionMessage{
		{ID: 1, SessionKey: dmKey, Role: "user", Content: "hello"},
		{ID: 2, SessionKey: dmKey, Role: "assistant", Content: "hi there"},
	}
	source.messages[groupKey] = []SessionMessage{
		{ID: 3, SessionKey: groupKey, Role: "user", Content: "do not index me"},
	}

	embedder := &stubBatchEmbedder{}
	indexer := NewSessionIndexer(store, embedder, source, WithIndexerChunking(64, 0))
	stats, err := indexer.IndexSessions(context.Background(), SessionIndexOptions{})
	if err != nil {
		t.Fatalf("IndexSessions failed: %v", err)
	}

	if stats.SessionsIndexed != 1 {
		t.Errorf("SessionsIndexed = %d, want 1", stats.SessionsIndexed)
	}
	if stats.SessionsSkipped < 1 {
		t.Errorf("expected group session to be skipped, stats=%+v", stats)
	}

	if count := countSourceChunks(t, db, SessionSourceFile(dmKey)); count == 0 {
		t.Fatalf("expected dm session chunks, got %d", count)
	}
	if count := countSourceChunks(t, db, SessionSourceFile(groupKey)); count != 0 {
		t.Fatalf("group session must not be indexed by default, got %d", count)
	}
}

func TestSessionIndexerIncludesGroupsWhenAllowed(t *testing.T) {
	db := openIndexerTestDB(t)
	defer db.Close() //nolint:errcheck

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	groupKey := "agent:default:telegram:group:99"
	source := newStubSessionSource(groupKey)
	source.messages[groupKey] = []SessionMessage{
		{ID: 1, SessionKey: groupKey, Role: "user", Content: "shared question"},
		{ID: 2, SessionKey: groupKey, Role: "assistant", Content: "shared answer"},
	}

	embedder := &stubBatchEmbedder{}
	indexer := NewSessionIndexer(store, embedder, source, WithIndexerChunking(64, 0))
	stats, err := indexer.IndexSessions(context.Background(), SessionIndexOptions{IncludeGroups: true})
	if err != nil {
		t.Fatalf("IndexSessions failed: %v", err)
	}
	if stats.SessionsIndexed != 1 {
		t.Errorf("SessionsIndexed = %d, want 1", stats.SessionsIndexed)
	}
	if count := countSourceChunks(t, db, SessionSourceFile(groupKey)); count != 2 {
		t.Fatalf("group session chunks = %d, want 2", count)
	}
}

func TestSessionIndexerSanitizesContentBeforeIndexing(t *testing.T) {
	db := openIndexerTestDB(t)
	defer db.Close() //nolint:errcheck

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	dmKey := "agent:default:telegram:dm:7"
	source := newStubSessionSource(dmKey)
	source.messages[dmKey] = []SessionMessage{
		{ID: 1, SessionKey: dmKey, Role: "user", Content: "use sk-supersecretvalue1234567890 to login"},
	}

	embedder := &stubBatchEmbedder{}
	indexer := NewSessionIndexer(store, embedder, source, WithIndexerChunking(64, 0))
	if _, err := indexer.IndexSessions(context.Background(), SessionIndexOptions{}); err != nil {
		t.Fatalf("IndexSessions failed: %v", err)
	}

	rows, err := db.Query(`SELECT content FROM memory_chunks WHERE source_file = ?`, SessionSourceFile(dmKey))
	if err != nil {
		t.Fatalf("query stored chunks: %v", err)
	}
	defer rows.Close()

	var stored []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		stored = append(stored, c)
	}
	if len(stored) == 0 {
		t.Fatalf("no chunks stored")
	}
	for _, content := range stored {
		if strings.Contains(content, "sk-supersecretvalue1234567890") {
			t.Errorf("indexed chunk leaked secret: %q", content)
		}
	}
}

func TestSessionIndexerAppliesPerSessionLimit(t *testing.T) {
	db := openIndexerTestDB(t)
	defer db.Close() //nolint:errcheck

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	dmKey := "agent:default:telegram:dm:9"
	source := newStubSessionSource(dmKey)
	for i := 0; i < 6; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		source.messages[dmKey] = append(source.messages[dmKey], SessionMessage{
			ID:         int64(i + 1),
			SessionKey: dmKey,
			Role:       role,
			Content:    "msg-" + strings.Repeat("x", 8),
		})
	}

	embedder := &stubBatchEmbedder{}
	indexer := NewSessionIndexer(store, embedder, source, WithIndexerChunking(64, 0))
	if _, err := indexer.IndexSessions(context.Background(), SessionIndexOptions{MaxMessagesPerSession: 3}); err != nil {
		t.Fatalf("IndexSessions failed: %v", err)
	}

	got := countSourceChunks(t, db, SessionSourceFile(dmKey))
	if got != 3 {
		t.Fatalf("chunks under message cap = %d, want 3", got)
	}
}

func TestLoadSessionSpanCentersOnAnchor(t *testing.T) {
	dmKey := "agent:default:telegram:dm:1"
	source := newStubSessionSource(dmKey)
	for i := 0; i < 5; i++ {
		source.messages[dmKey] = append(source.messages[dmKey], SessionMessage{
			ID:         int64(i + 1),
			SessionKey: dmKey,
			Role:       "user",
			Content:    "m",
		})
	}

	span, err := LoadSessionSpan(context.Background(), source, dmKey, 3, 1)
	if err != nil {
		t.Fatalf("LoadSessionSpan: %v", err)
	}
	if len(span.Messages) != 3 {
		t.Fatalf("len(span) = %d, want 3", len(span.Messages))
	}
	if span.Messages[0].ID != 2 || span.Messages[2].ID != 4 {
		t.Fatalf("expected messages [2,3,4], got %v", spanIDs(span.Messages))
	}
}

func TestLoadSessionSpanFallsBackToTail(t *testing.T) {
	dmKey := "agent:default:telegram:dm:1"
	source := newStubSessionSource(dmKey)
	for i := 0; i < 4; i++ {
		source.messages[dmKey] = append(source.messages[dmKey], SessionMessage{
			ID: int64(i + 1), SessionKey: dmKey, Role: "user", Content: "m",
		})
	}

	span, err := LoadSessionSpan(context.Background(), source, dmKey, 0, 1)
	if err != nil {
		t.Fatalf("LoadSessionSpan: %v", err)
	}
	want := []int64{2, 3, 4}
	got := spanIDs(span.Messages)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestFormatSnippetCitationIdentifiesSessionsByFingerprint(t *testing.T) {
	sessionKey := "agent:default:telegram:dm:1"
	hit := MemorySnippet{
		File:       SessionSourceFile(sessionKey),
		HeaderPath: "msg 12 [user]",
		Score:      0.81,
		SourceType: SourceSession,
		SessionKey: sessionKey,
	}
	citation := FormatSnippetCitation(hit)
	if !strings.Contains(citation, "[session ") {
		t.Errorf("missing session prefix: %q", citation)
	}
	if !strings.Contains(citation, SessionKeyFingerprint(sessionKey)) {
		t.Errorf("missing session fingerprint: %q", citation)
	}
	if !strings.Contains(citation, "msg 12 [user]") {
		t.Errorf("missing message identifier: %q", citation)
	}
	// Workspace citation does not leak fingerprint.
	workspace := MemorySnippet{File: "MEMORY.md", HeaderPath: "Topics > Goals", SourceType: SourceWorkspace}
	if cite := FormatSnippetCitation(workspace); !strings.HasPrefix(cite, "[workspace]") {
		t.Errorf("workspace citation = %q", cite)
	}
}

func TestSearcherSourceFilterRestrictsResults(t *testing.T) {
	db := newMemoryChunksTestDBWithOrdinal(t)

	insertChunkWithOrdinal(t, db, "MEMORY.md", "root", 0, "workspace fact", []float32{1, 0, 0})
	insertChunkWithOrdinal(t, db, "memory/2026-04-29.md", "root", 0, "daily fact", []float32{0.99, 0, 0})
	insertChunkWithOrdinal(t, db, SessionSourceFile("k1"), "msg 1 [user]", 0, "session fact", []float32{0.98, 0, 0})

	searcher, err := NewSearcher(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSearcher: %v", err)
	}

	all := searcher.Search([]float32{1, 0, 0}, SearchOptions{Threshold: 0.5})
	if len(all) != 3 {
		t.Fatalf("expected 3 hits without filter, got %d", len(all))
	}

	sessionsOnly := searcher.Search([]float32{1, 0, 0}, SearchOptions{
		Threshold: 0.5,
		Sources:   []SourceType{SourceSession},
	})
	if len(sessionsOnly) != 1 {
		t.Fatalf("expected 1 session-only hit, got %d", len(sessionsOnly))
	}
	if sessionsOnly[0].SourceType != SourceSession {
		t.Fatalf("hit source = %q, want %q", sessionsOnly[0].SourceType, SourceSession)
	}
	if sessionsOnly[0].SessionKey != "k1" {
		t.Fatalf("hit session key = %q, want k1", sessionsOnly[0].SessionKey)
	}
}

func spanIDs(msgs []SessionMessage) []int64 {
	out := make([]int64, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}
