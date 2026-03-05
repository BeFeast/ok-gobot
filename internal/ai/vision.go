package ai

import "strings"

// visionCapable is implemented by clients that can accept image blocks
// in user messages.
type visionCapable interface {
	SupportsVision() bool
}

// SupportsVision reports whether the client accepts multimodal user content.
func SupportsVision(client Client) bool {
	if client == nil {
		return false
	}
	vc, ok := client.(visionCapable)
	return ok && vc.SupportsVision()
}

func anthropicModelSupportsVision(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 && idx+1 < len(normalized) {
		normalized = normalized[idx+1:]
	}
	return strings.HasPrefix(normalized, "claude-")
}
