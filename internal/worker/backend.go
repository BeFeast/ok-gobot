package worker

import (
	"path/filepath"
	"strings"
)

// DetectBackend normalizes a CLI binary path into the backend identity used in
// health/status output.
func DetectBackend(binaryPath string) string {
	name := strings.ToLower(strings.TrimSpace(filepath.Base(binaryPath)))
	name = strings.TrimSuffix(name, filepath.Ext(name))
	switch name {
	case "", "droid":
		return "droid"
	case "claude", "claude-code":
		return "claude"
	case "codex", "openai-codex":
		return "codex"
	case "opencode", "open-code":
		return "opencode"
	case "gemini":
		return "gemini"
	default:
		return name
	}
}
