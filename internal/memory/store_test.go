package memory

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float32
		delta    float32
	}{
		{
			name:     "identical vectors",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{1.0, 0.0, 0.0},
			expected: 1.0,
			delta:    0.001,
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{0.0, 1.0, 0.0},
			expected: 0.0,
			delta:    0.001,
		},
		{
			name:     "opposite vectors",
			a:        []float32{1.0, 0.0, 0.0},
			b:        []float32{-1.0, 0.0, 0.0},
			expected: -1.0,
			delta:    0.001,
		},
		{
			name:     "similar vectors",
			a:        []float32{1.0, 1.0, 0.0},
			b:        []float32{1.0, 0.5, 0.0},
			expected: 0.948, // approximately
			delta:    0.01,
		},
		{
			name:     "different length vectors",
			a:        []float32{1.0, 0.0},
			b:        []float32{1.0, 0.0, 0.0},
			expected: 0.0, // should return 0 for mismatched lengths
			delta:    0.001,
		},
		{
			name:     "zero vectors",
			a:        []float32{0.0, 0.0, 0.0},
			b:        []float32{1.0, 0.0, 0.0},
			expected: 0.0, // should return 0 when one vector is zero
			delta:    0.001,
		},
		{
			name:     "normalized vectors",
			a:        []float32{0.6, 0.8},
			b:        []float32{0.8, 0.6},
			expected: 0.96, // 0.6*0.8 + 0.8*0.6 = 0.96
			delta:    0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cosineSimilarity(tt.a, tt.b)
			diff := result - tt.expected
			if diff < 0 {
				diff = -diff
			}
			if diff > tt.delta {
				t.Errorf("cosineSimilarity(%v, %v) = %f, want %f (delta %f)",
					tt.a, tt.b, result, tt.expected, tt.delta)
			}
		})
	}
}

func TestEncodeDecodeEmbedding(t *testing.T) {
	tests := []struct {
		name      string
		embedding []float32
	}{
		{
			name:      "simple vector",
			embedding: []float32{1.0, 2.0, 3.0},
		},
		{
			name:      "negative values",
			embedding: []float32{-1.5, 2.7, -3.2, 0.5},
		},
		{
			name:      "typical embedding dimensions",
			embedding: make([]float32, 1536), // OpenAI embedding size
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Fill with test data for large vectors
			for i := range tt.embedding {
				tt.embedding[i] = float32(i) * 0.01
			}

			// Encode
			encoded, err := encodeEmbedding(tt.embedding)
			if err != nil {
				t.Fatalf("encodeEmbedding failed: %v", err)
			}

			// Decode
			decoded, err := decodeEmbedding(encoded)
			if err != nil {
				t.Fatalf("decodeEmbedding failed: %v", err)
			}

			// Compare
			if len(decoded) != len(tt.embedding) {
				t.Fatalf("length mismatch: got %d, want %d", len(decoded), len(tt.embedding))
			}

			for i := range tt.embedding {
				if decoded[i] != tt.embedding[i] {
					t.Errorf("value mismatch at index %d: got %f, want %f",
						i, decoded[i], tt.embedding[i])
				}
			}
		})
	}
}

func TestMemoryStoreMigratesLegacyTableAndAddsMetadata(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, err := db.Exec(`
		CREATE TABLE memories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT NOT NULL,
			embedding BLOB NOT NULL,
			category TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("failed to create legacy table: %v", err)
	}

	embeddingBytes, err := encodeEmbedding([]float32{0.2, 0.8})
	if err != nil {
		t.Fatalf("failed to encode test embedding: %v", err)
	}

	_, err = db.Exec("INSERT INTO memories (content, embedding, category) VALUES (?, ?, ?)", "legacy entry", embeddingBytes, "facts")
	if err != nil {
		t.Fatalf("failed to insert legacy row: %v", err)
	}

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	var metadata string
	err = db.QueryRow("SELECT metadata FROM memory_chunks WHERE content = ?", "legacy entry").Scan(&metadata)
	if err != nil {
		t.Fatalf("failed to read migrated row: %v", err)
	}
	if metadata != "{}" {
		t.Fatalf("expected metadata default '{}', got %q", metadata)
	}

	results, err := store.List(5)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if len(results[0].Metadata.People) != 0 || results[0].Metadata.Type != "" {
		t.Fatalf("expected empty metadata after migration, got %+v", results[0].Metadata)
	}
}

func TestMemoryStoreSearchWithPersonFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	ctx := context.Background()
	err = store.SaveWithMetadata(ctx, "Discussed release blockers with Anton", "meetings", []float32{1, 0}, ChunkMetadata{
		People: []string{"Anton"},
		Topics: []string{"release"},
		Type:   "decision",
	})
	if err != nil {
		t.Fatalf("SaveWithMetadata failed (Anton): %v", err)
	}

	err = store.SaveWithMetadata(ctx, "Planning session with Maria", "meetings", []float32{1, 0}, ChunkMetadata{
		People: []string{"Maria"},
		Topics: []string{"planning"},
		Type:   "note",
	})
	if err != nil {
		t.Fatalf("SaveWithMetadata failed (Maria): %v", err)
	}

	results, err := store.SearchWithFilter(ctx, []float32{1, 0}, 10, MemorySearchFilter{Person: "anton"})
	if err != nil {
		t.Fatalf("SearchWithFilter failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for person filter, got %d", len(results))
	}
	if results[0].Content != "Discussed release blockers with Anton" {
		t.Fatalf("unexpected filtered result: %q", results[0].Content)
	}
	if len(results[0].Metadata.People) != 1 || results[0].Metadata.People[0] != "Anton" {
		t.Fatalf("expected metadata people to be preserved, got %+v", results[0].Metadata)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	return db
}
