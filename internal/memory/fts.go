package memory

import (
	"errors"
	"fmt"
	"strings"
)

var errMemoryFTSUnavailable = errors.New("memory fts5 unavailable")

func (s *MemoryStore) ensureChunksFTS() (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("memory store is not configured")
	}

	if _, err := s.db.Exec(fmt.Sprintf(`
		CREATE VIRTUAL TABLE IF NOT EXISTS %s USING fts5(
			content,
			content='%s',
			content_rowid='id',
			tokenize='unicode61'
		);
	`, memoryChunksFTSTable, memoryChunksTable)); err != nil {
		if isFTSUnavailable(err) {
			dropMemoryFTSTriggers(s)
			return false, nil
		}
		return false, fmt.Errorf("create memory FTS index: %w", err)
	}

	statements := []string{
		fmt.Sprintf(`
			CREATE TRIGGER IF NOT EXISTS memory_chunks_fts_ai AFTER INSERT ON %s BEGIN
				INSERT INTO %s(rowid, content) VALUES (new.id, new.content);
			END;
		`, memoryChunksTable, memoryChunksFTSTable),
		fmt.Sprintf(`
			CREATE TRIGGER IF NOT EXISTS memory_chunks_fts_ad AFTER DELETE ON %s BEGIN
				INSERT INTO %s(%s, rowid, content) VALUES('delete', old.id, old.content);
			END;
		`, memoryChunksTable, memoryChunksFTSTable, memoryChunksFTSTable),
		fmt.Sprintf(`
			CREATE TRIGGER IF NOT EXISTS memory_chunks_fts_au AFTER UPDATE OF content ON %s BEGIN
				INSERT INTO %s(%s, rowid, content) VALUES('delete', old.id, old.content);
				INSERT INTO %s(rowid, content) VALUES (new.id, new.content);
			END;
		`, memoryChunksTable, memoryChunksFTSTable, memoryChunksFTSTable, memoryChunksFTSTable),
	}

	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			if isFTSUnavailable(err) {
				dropMemoryFTSTriggers(s)
				return false, nil
			}
			return false, fmt.Errorf("create memory FTS trigger: %w", err)
		}
	}

	if _, err := s.db.Exec(fmt.Sprintf(`INSERT INTO %s(%s) VALUES('rebuild')`, memoryChunksFTSTable, memoryChunksFTSTable)); err != nil {
		if isFTSUnavailable(err) {
			dropMemoryFTSTriggers(s)
			return false, nil
		}
		return false, fmt.Errorf("rebuild memory FTS index: %w", err)
	}

	return true, nil
}

func dropMemoryFTSTriggers(s *MemoryStore) {
	if s == nil || s.db == nil {
		return
	}
	for _, name := range []string{"memory_chunks_fts_ai", "memory_chunks_fts_ad", "memory_chunks_fts_au"} {
		_, _ = s.db.Exec("DROP TRIGGER IF EXISTS " + name)
	}
}

func isFTSUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errMemoryFTSUnavailable) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such module: fts5") ||
		strings.Contains(msg, "no such table: "+memoryChunksFTSTable) ||
		strings.Contains(msg, "no such module") && strings.Contains(msg, "fts5")
}
