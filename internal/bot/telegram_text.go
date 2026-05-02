package bot

import (
	"regexp"
	"strings"
)

var (
	telegramInlineCodeRE = regexp.MustCompile("`([^`\n]+)`")
	telegramBulletRE     = regexp.MustCompile(`(?m)^[ \t]*\*[ \t]+`)
)

func sanitizeTelegramModelReply(text string) string {
	text = telegramBulletRE.ReplaceAllString(text, "• ")
	text = telegramInlineCodeRE.ReplaceAllStringFunc(text, func(match string) string {
		inner := strings.Trim(match, "`")
		if looksLikeTelegramPlainToken(inner) {
			return inner
		}
		return match
	})
	text = strings.ReplaceAll(text, "**", "")
	return strings.TrimSpace(text)
}

func looksLikeTelegramPlainToken(text string) bool {
	return strings.HasPrefix(text, "/") ||
		strings.Contains(text, "://") ||
		strings.Contains(text, "/") ||
		strings.Contains(text, "_") ||
		strings.Contains(text, ".")
}
