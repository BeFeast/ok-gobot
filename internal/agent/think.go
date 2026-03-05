package agent

import (
	"regexp"
	"strings"
)

// thinkTagRe matches <think>...</think> blocks (including multiline, non-greedy).
var thinkTagRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

// StripThinkTags removes all <think>...</think> blocks from a response string.
// These are internal reasoning traces that should never be shown to the user.
func StripThinkTags(s string) string {
	cleaned := thinkTagRe.ReplaceAllString(s, "")
	// Trim any leading/trailing whitespace left behind
	return strings.TrimSpace(cleaned)
}
