// Package deepthink holds the deterministic pieces of the deep-think feature:
// the trigger-phrase matcher that promotes a single chat turn to a stronger
// tier, and the escalation policy that guards the deep_think tool. Neither
// piece calls a model; both are pure functions over configuration and text so
// the bot and the tool registry can share them without importing each other.
package deepthink

import (
	"strings"
	"unicode"
)

// Matcher recognises a configured trigger phrase at the start or at the end
// of a message. The same phrase inside a sentence ("я не подумал хорошо о …")
// never matches: a trigger is a deliberate prefix or suffix, not a topic.
type Matcher struct {
	phrases [][]rune // lower-cased, longest first
}

// Match describes a recognised trigger.
type Match struct {
	Phrase string // the configured phrase that matched (as configured)
	Rest   string // the message without the phrase; the original text when nothing else remains
	AtEnd  bool   // true when the phrase closed the message instead of opening it
}

// NewMatcher builds a matcher from configured phrases. Empty phrases are
// dropped; an empty list yields a matcher that never matches.
func NewMatcher(phrases []string) *Matcher {
	m := &Matcher{}
	seen := map[string]struct{}{}
	for _, phrase := range phrases {
		normalized := normalizePhrase(phrase)
		if len(normalized) == 0 {
			continue
		}
		key := string(normalized)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		m.phrases = append(m.phrases, normalized)
	}
	// Longest first so "подумай как следует" wins over a shorter prefix of it.
	for i := 1; i < len(m.phrases); i++ {
		for j := i; j > 0 && len(m.phrases[j]) > len(m.phrases[j-1]); j-- {
			m.phrases[j], m.phrases[j-1] = m.phrases[j-1], m.phrases[j]
		}
	}
	return m
}

// Enabled reports whether at least one phrase is configured.
func (m *Matcher) Enabled() bool { return m != nil && len(m.phrases) > 0 }

// Phrases returns the configured phrases in matching order.
func (m *Matcher) Phrases() []string {
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m.phrases))
	for _, p := range m.phrases {
		out = append(out, string(p))
	}
	return out
}

// Match checks text for a trigger phrase at its start or end. Case and
// surrounding punctuation are ignored; the phrase must be delimited by a
// non-letter on the message side, so "think harder" does not match "think hard".
func (m *Matcher) Match(text string) (Match, bool) {
	if !m.Enabled() {
		return Match{}, false
	}
	runes := []rune(text)
	lower := make([]rune, len(runes))
	for i, r := range runes {
		lower[i] = unicode.ToLower(r)
	}

	start := 0
	for start < len(lower) && (unicode.IsSpace(lower[start]) || isOpeningPunct(lower[start])) {
		start++
	}
	end := len(lower)
	for end > start && (unicode.IsSpace(lower[end-1]) || isClosingPunct(lower[end-1])) {
		end--
	}
	if start >= end {
		return Match{}, false
	}

	for _, phrase := range m.phrases {
		n := len(phrase)
		if n > end-start {
			continue
		}
		// Prefix: phrase followed by end-of-text or a non-letter boundary.
		if equalRunes(lower[start:start+n], phrase) && (start+n == end || !isWordRune(lower[start+n])) {
			// The request keeps its own closing punctuation ("…2+2?").
			rest := strings.TrimSpace(strings.TrimLeftFunc(string(runes[start+n:]), isLeadingSeparator))
			return Match{Phrase: string(phrase), Rest: restOrOriginal(rest, text)}, true
		}
		// Suffix: phrase preceded by start-of-text or a non-letter boundary.
		if equalRunes(lower[end-n:end], phrase) && (end-n == start || !isWordRune(lower[end-n-1])) {
			rest := strings.TrimRightFunc(string(runes[start:end-n]), isTrailingSeparator)
			return Match{Phrase: string(phrase), Rest: restOrOriginal(rest, text), AtEnd: true}, true
		}
	}
	return Match{}, false
}

func restOrOriginal(rest, original string) string {
	if strings.TrimSpace(rest) == "" {
		return strings.TrimSpace(original)
	}
	return rest
}

func normalizePhrase(phrase string) []rune {
	fields := strings.Fields(strings.ToLower(phrase))
	if len(fields) == 0 {
		return nil
	}
	return []rune(strings.Join(fields, " "))
}

func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// Punctuation that may wrap the phrase without breaking the match.
func isOpeningPunct(r rune) bool { return strings.ContainsRune("«\"'([", r) }
func isClosingPunct(r rune) bool { return strings.ContainsRune("»\"')].!?…", r) }

// Separators removed between the phrase and the request it introduces.
func isLeadingSeparator(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(":,;.!—–-»\"'", r)
}

// Separators removed between the request and a phrase that closes it. A
// question mark or full stop belongs to the request and stays.
func isTrailingSeparator(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(",;:—–-«\"'(", r)
}
