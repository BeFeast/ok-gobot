//go:build sqlite_fts5

package memory

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// When the sqlite_fts5 tag is set the FTS5 module must actually be present.
// This guards the build configuration itself: the tag is easy to drop from a
// Makefile or workflow, and the only symptom is that lexical search quietly
// gets worse. The test file is tag-gated so untagged runs stay green.
func TestFTS5IsCompiledInWhenTagIsSet(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	if !store.LexicalIndexAvailable() {
		t.Fatal("built with -tags sqlite_fts5 but the FTS5 module is missing; lexical search would degrade to LIKE")
	}
}
