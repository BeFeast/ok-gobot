package memory

import "fmt"

func (s *MemoryStore) ensureChunksFTS() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("memory store is not configured")
	}

	statements := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_chunks_fts USING fts5(
			content,
			source_file UNINDEXED,
			header_path UNINDEXED,
			chunk_ordinal UNINDEXED,
			content='memory_chunks',
			content_rowid='id',
			tokenize='unicode61'
		);`,
		`CREATE TRIGGER IF NOT EXISTS memory_chunks_fts_ai AFTER INSERT ON memory_chunks BEGIN
			INSERT INTO memory_chunks_fts(rowid, content, source_file, header_path, chunk_ordinal)
			VALUES (new.id, new.content, new.source_file, new.header_path, new.chunk_ordinal);
		END;`,
		`CREATE TRIGGER IF NOT EXISTS memory_chunks_fts_ad AFTER DELETE ON memory_chunks BEGIN
			INSERT INTO memory_chunks_fts(memory_chunks_fts, rowid, content, source_file, header_path, chunk_ordinal)
			VALUES ('delete', old.id, old.content, old.source_file, old.header_path, old.chunk_ordinal);
		END;`,
		`CREATE TRIGGER IF NOT EXISTS memory_chunks_fts_au AFTER UPDATE ON memory_chunks BEGIN
			INSERT INTO memory_chunks_fts(memory_chunks_fts, rowid, content, source_file, header_path, chunk_ordinal)
			VALUES ('delete', old.id, old.content, old.source_file, old.header_path, old.chunk_ordinal);
			INSERT INTO memory_chunks_fts(rowid, content, source_file, header_path, chunk_ordinal)
			VALUES (new.id, new.content, new.source_file, new.header_path, new.chunk_ordinal);
		END;`,
	}

	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}

	if _, err := s.db.Exec(`INSERT INTO memory_chunks_fts(memory_chunks_fts) VALUES ('rebuild')`); err != nil {
		return err
	}

	return nil
}
