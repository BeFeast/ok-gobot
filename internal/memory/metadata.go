package memory

import (
	"encoding/json"
	"strings"
)

// ChunkMetadata stores structured memory attributes extracted from text.
type ChunkMetadata struct {
	People      []string `json:"people"`
	Topics      []string `json:"topics"`
	ActionItems []string `json:"action_items"`
	Type        string   `json:"type"`
}

// MemorySearchFilter narrows memory recalls to specific metadata values.
type MemorySearchFilter struct {
	Person string
}

func (m ChunkMetadata) normalize() ChunkMetadata {
	m.People = dedupeAndTrim(m.People)
	m.Topics = dedupeAndTrim(m.Topics)
	m.ActionItems = dedupeAndTrim(m.ActionItems)
	m.Type = strings.TrimSpace(strings.ToLower(m.Type))
	return m
}

func (m ChunkMetadata) toJSON() string {
	normalized := m.normalize()
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func parseChunkMetadata(raw string) ChunkMetadata {
	if strings.TrimSpace(raw) == "" {
		return ChunkMetadata{}
	}
	var metadata ChunkMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return ChunkMetadata{}
	}
	return metadata.normalize()
}

func dedupeAndTrim(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func containsFold(values []string, want string) bool {
	needle := strings.TrimSpace(want)
	if needle == "" {
		return true
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), needle) {
			return true
		}
	}
	return false
}
