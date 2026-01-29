package redact

import (
	"regexp"
	"strings"
)

var (
	// API keys (sk-..., sk-or-..., key-...)
	// Capture first 6 chars after prefix, then match remaining 10+ chars
	apiKeyPattern = regexp.MustCompile(`\bsk-([a-zA-Z0-9_-]{6})[a-zA-Z0-9_-]{10,}\b`)
	skOrPattern   = regexp.MustCompile(`\bsk-or-([a-zA-Z0-9_-]{3})[a-zA-Z0-9_-]{10,}\b`)
	keyPattern    = regexp.MustCompile(`\bkey-([a-zA-Z0-9_-]{6})[a-zA-Z0-9_-]{10,}\b`)

	// Bearer tokens
	bearerPattern = regexp.MustCompile(`\bBearer\s+[a-zA-Z0-9_\-\.]+`)

	// Bot tokens (digits:alphanumeric)
	// Capture first 6 digits, then colon, then match remaining chars
	botTokenPattern = regexp.MustCompile(`\b(\d{6})\d*:[a-zA-Z0-9_-]{10,}\b`)

	// Generic long hex/base64 strings that look like secrets
	// Match strings that are 32+ chars of alphanumeric/base64 chars
	// Avoid matching common words or UUIDs
	secretPattern = regexp.MustCompile(`([a-fA-F0-9]{32,}|[a-zA-Z0-9+/]{32,}={0,2})`)
)

// Redact masks sensitive patterns in a string
// Returns the string with sensitive data replaced by masked versions
func Redact(s string) string {
	// API keys (sk-...)
	s = apiKeyPattern.ReplaceAllString(s, "sk-$1***")

	// API keys (sk-or-...)
	s = skOrPattern.ReplaceAllString(s, "sk-or-$1***")

	// API keys (key-...)
	s = keyPattern.ReplaceAllString(s, "key-$1***")

	// Bearer tokens
	s = bearerPattern.ReplaceAllStringFunc(s, func(match string) string {
		return "Bearer ***"
	})

	// Bot tokens
	s = botTokenPattern.ReplaceAllString(s, "$1***")

	// Generic secrets - only if they look like secrets (all one case, no obvious words)
	s = secretPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Skip if it looks like a UUID (has dashes)
		if strings.Contains(match, "-") {
			return match
		}

		// Skip if it's too short after all
		if len(match) < 32 {
			return match
		}

		// Check if it looks like a hex string (only hex chars)
		isHex := true
		for _, c := range match {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				isHex = false
				break
			}
		}

		// Check if it looks like base64 (has + or / or =)
		isBase64 := strings.ContainsAny(match, "+/=")

		// Only redact if it looks like hex or base64
		if isHex || isBase64 {
			if len(match) > 8 {
				return match[:6] + "***"
			}
			return "***"
		}

		return match
	})

	return s
}
