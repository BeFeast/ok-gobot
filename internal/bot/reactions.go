package bot

import (
	"regexp"
	"strings"
)

var reactPattern = regexp.MustCompile(`\[\[react:([^\]]+)\]\]`)

// parseReactions extracts reaction emoji and cleans the text
func parseReactions(text string) (string, []string) {
	matches := reactPattern.FindAllStringSubmatch(text, -1)
	var reactions []string
	for _, m := range matches {
		if len(m) == 2 {
			reactions = append(reactions, strings.TrimSpace(m[1]))
		}
	}
	clean := reactPattern.ReplaceAllString(text, "")
	clean = strings.TrimSpace(clean)
	return clean, reactions
}
