package memory

import (
	"testing"
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
