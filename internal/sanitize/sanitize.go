package sanitize

import (
	"strings"
	"unicode"
)

// SanitizeShellArg escapes shell metacharacters for safe use in commands.
// This prevents command injection by escaping special shell characters.
func SanitizeShellArg(s string) string {
	// Characters that have special meaning in shell
	shellMeta := []string{
		"$", "`", "\"", "\\", "!", "\n", "&", ";", "|", "<", ">", "(", ")", "{", "}", "[", "]", "*", "?", "~", "#", "%",
	}

	result := s
	for _, char := range shellMeta {
		result = strings.ReplaceAll(result, char, "\\"+char)
	}

	return result
}

// SanitizeTelegramMarkdown escapes special Telegram MarkdownV2 characters.
// MarkdownV2 requires escaping of: _ * [ ] ( ) ~ ` > # + - = | { } . !
func SanitizeTelegramMarkdown(s string) string {
	// Characters that need escaping in Telegram MarkdownV2
	markdownChars := []rune{
		'_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!',
	}

	var builder strings.Builder
	builder.Grow(len(s) * 2) // Pre-allocate to reduce allocations

	for _, r := range s {
		needsEscape := false
		for _, special := range markdownChars {
			if r == special {
				needsEscape = true
				break
			}
		}

		if needsEscape {
			builder.WriteRune('\\')
		}
		builder.WriteRune(r)
	}

	return builder.String()
}

// StripControlChars removes non-printable control characters except newline and tab.
// This prevents issues with terminal escape sequences and other control characters.
func StripControlChars(s string) string {
	var builder strings.Builder
	builder.Grow(len(s))

	for _, r := range s {
		// Keep newline (0x0A), tab (0x09), and carriage return (0x0D)
		// Remove other control characters (0x00-0x1F and 0x7F-0x9F)
		if r == '\n' || r == '\t' || r == '\r' {
			builder.WriteRune(r)
			continue
		}

		// Check if it's a control character
		if unicode.IsControl(r) {
			continue
		}

		builder.WriteRune(r)
	}

	return builder.String()
}

// SanitizeForDisplay combines control character stripping with markdown escaping.
// Use this when displaying user-controlled content in Telegram messages.
func SanitizeForDisplay(s string) string {
	// First strip control characters
	cleaned := StripControlChars(s)
	// Then escape markdown
	return SanitizeTelegramMarkdown(cleaned)
}
