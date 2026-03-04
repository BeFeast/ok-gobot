package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ok-gobot/internal/ai"
)

// LLMMetadataExtractor uses a lightweight chat model to extract structured metadata.
type LLMMetadataExtractor struct {
	client ai.Client
}

// NewLLMMetadataExtractor creates a metadata extractor backed by an AI client.
func NewLLMMetadataExtractor(client ai.Client) *LLMMetadataExtractor {
	return &LLMMetadataExtractor{client: client}
}

// Extract returns normalized metadata for a memory chunk.
func (e *LLMMetadataExtractor) Extract(ctx context.Context, content string) (ChunkMetadata, error) {
	text := strings.TrimSpace(content)
	if text == "" {
		return ChunkMetadata{}, nil
	}

	resp, err := e.client.Complete(ctx, []ai.Message{
		{
			Role: "system",
			Content: "Extract structured metadata from the user text. " +
				"Return only valid JSON with this exact shape: " +
				`{"people":[],"topics":[],"action_items":[],"type":"decision|note|todo|question|update|other"}. ` +
				"Use short phrases. If unknown, use empty arrays and type=note. Do not include markdown.",
		},
		{
			Role:    "user",
			Content: text,
		},
	})
	if err != nil {
		return ChunkMetadata{}, err
	}

	jsonPayload := extractJSONObject(resp)
	if jsonPayload == "" {
		return ChunkMetadata{}, fmt.Errorf("metadata extractor returned no JSON object")
	}

	var metadata ChunkMetadata
	if err := json.Unmarshal([]byte(jsonPayload), &metadata); err != nil {
		return ChunkMetadata{}, fmt.Errorf("failed to parse metadata JSON: %w", err)
	}
	return metadata.normalize(), nil
}

func extractJSONObject(input string) string {
	start := strings.Index(input, "{")
	end := strings.LastIndex(input, "}")
	if start == -1 || end == -1 || end < start {
		return ""
	}
	return strings.TrimSpace(input[start : end+1])
}
