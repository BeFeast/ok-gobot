package bot

import (
	"strings"
	"unicode"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/logger"
)

// compactionPrefix is the marker prepended to compaction summaries stored in
// the v2 transcript by handleCompactCommand.
const compactionPrefix = "[Compacted conversation summary]"

// contextWindowRadius is the number of raw messages before and after the
// best-matching message to include when expanding a branch.
const contextWindowRadius = 2

// historyBranch groups a compaction summary anchor with the raw messages that
// follow it until the next compaction or end of history.
type historyBranch struct {
	// summary is the compaction summary message that anchors this branch.
	// nil for the first branch if history starts with raw messages.
	summary *ai.ChatMessage
	// messages are the raw user/assistant messages in this branch.
	messages []ai.ChatMessage
}

// buildRunHistory assembles the conversation history for a new agent run.
// When compaction summaries exist it searches both summaries and raw transcript
// together, then expands only the best-matching branch instead of replaying
// the full history. Falls back to simple token-budget trimming otherwise.
func buildRunHistory(history []ai.ChatMessage, userMessage string, model string) []ai.ChatMessage {
	if len(history) == 0 {
		return nil
	}

	if !hasCompactions(history) {
		return trimHistoryToTokenBudget(history, model)
	}

	expanded := buildSearchExpandedHistory(history, userMessage, model)
	if expanded != nil {
		return expanded
	}

	// Fallback: nothing scored well, use simple trim.
	return trimHistoryToTokenBudget(history, model)
}

// hasCompactions returns true if any message in history is a compaction summary.
func hasCompactions(history []ai.ChatMessage) bool {
	for i := range history {
		if isCompactionSummary(&history[i]) {
			return true
		}
	}
	return false
}

// isCompactionSummary reports whether msg is a compaction summary anchor.
func isCompactionSummary(msg *ai.ChatMessage) bool {
	return msg.Role == "assistant" && strings.HasPrefix(msg.Content, compactionPrefix)
}

// buildSearchExpandedHistory scores all branches and individual messages
// against the current user turn, selects the best-matching branch, and
// returns a compact history containing:
//  1. The selected branch's summary anchor (if any)
//  2. A local window of raw messages around the best match
//  3. The tail of the most recent branch (to keep recency)
//
// The result is trimmed to the token budget before being returned.
func buildSearchExpandedHistory(history []ai.ChatMessage, userMessage string, model string) []ai.ChatMessage {
	branches := splitHistoryBranches(history)
	if len(branches) == 0 {
		return nil
	}

	queryTerms := tokenizeSearchTerms(userMessage)
	if len(queryTerms) == 0 {
		return nil
	}

	// Score every candidate: summaries and individual raw messages.
	bestBranchIdx := -1
	bestMsgIdx := -1 // index within branch.messages
	bestScore := 0

	for bi, br := range branches {
		// Score the summary anchor.
		if br.summary != nil {
			s := scoreContextCandidate(br.summary.Content, userMessage, queryTerms)
			if s >= bestScore && s > 0 {
				bestScore = s
				bestBranchIdx = bi
				bestMsgIdx = -1 // summary itself, not a raw message
			}
		}

		// Score each raw message. Use >= to prefer the most recent match
		// when scores tie (we iterate oldest-to-newest).
		for mi, msg := range br.messages {
			s := scoreContextCandidate(msg.Content, userMessage, queryTerms)
			if s >= bestScore && s > 0 {
				bestScore = s
				bestBranchIdx = bi
				bestMsgIdx = mi
			}
		}
	}

	if bestScore == 0 {
		return nil
	}

	logger.Debugf("context_history: best match branch=%d msg=%d score=%d", bestBranchIdx, bestMsgIdx, bestScore)

	// Assemble the expanded history.
	var result []ai.ChatMessage

	// Include the best branch's summary anchor.
	bestBranch := branches[bestBranchIdx]
	if bestBranch.summary != nil {
		result = append(result, *bestBranch.summary)
	}

	// Include a local window of raw messages around the match point.
	var windowHi int
	if len(bestBranch.messages) > 0 {
		anchor := bestMsgIdx
		if anchor < 0 {
			anchor = 0 // summary matched; start window from first raw message
		}
		lo, hi := rawWindowBounds(anchor, len(bestBranch.messages), contextWindowRadius)
		windowHi = hi
		result = append(result, bestBranch.messages[lo:hi]...)
	}

	// Append recency context. Cap the tail so the token trimmer (which
	// drops from the front) doesn't evict the matched-branch content.
	const maxTailMessages = 20
	lastIdx := len(branches) - 1

	if bestBranchIdx != lastIdx {
		// Matched an older branch — include capped tail from the latest branch.
		lastBranch := branches[lastIdx]
		if lastBranch.summary != nil {
			result = append(result, *lastBranch.summary)
		}
		tail := lastBranch.messages
		if len(tail) > maxTailMessages {
			tail = tail[len(tail)-maxTailMessages:]
		}
		result = append(result, tail...)
	} else if windowHi < len(bestBranch.messages) {
		// Matched the latest branch — append remaining recent turns beyond
		// the expansion window so follow-up context isn't lost.
		tail := bestBranch.messages[windowHi:]
		if len(tail) > maxTailMessages {
			tail = tail[len(tail)-maxTailMessages:]
		}
		result = append(result, tail...)
	}

	result = collapseConsecutiveSameRole(result)
	return trimHistoryToTokenBudget(result, model)
}

// splitHistoryBranches splits history into branches separated by compaction
// summaries. Each branch starts at a compaction summary (or the beginning of
// history) and extends until the next compaction summary.
func splitHistoryBranches(history []ai.ChatMessage) []historyBranch {
	var branches []historyBranch
	var current historyBranch

	for i := range history {
		msg := &history[i]
		if isCompactionSummary(msg) {
			// Flush the current branch if it has any content.
			if current.summary != nil || len(current.messages) > 0 {
				branches = append(branches, current)
			}
			current = historyBranch{summary: msg}
		} else {
			current.messages = append(current.messages, *msg)
		}
	}

	// Flush the last branch.
	if current.summary != nil || len(current.messages) > 0 {
		branches = append(branches, current)
	}

	return branches
}

// scoreContextCandidate scores how well candidateText matches the user's
// query using keyword-based heuristics. Higher scores indicate stronger matches.
//
// Scoring rules:
//   - +6 for an exact phrase match (case-insensitive substring)
//   - +3 for each unique query token found in the candidate
//   - +1 bonus when a token appears more than once in the candidate
func scoreContextCandidate(candidateText, userMessage string, queryTerms []string) int {
	if candidateText == "" || len(queryTerms) == 0 {
		return 0
	}

	candidateLower := strings.ToLower(candidateText)
	queryLower := strings.ToLower(userMessage)

	score := 0

	// Exact phrase match bonus.
	if len(queryLower) >= 4 && strings.Contains(candidateLower, queryLower) {
		score += 6
	}

	// Per-token scoring.
	for _, term := range queryTerms {
		idx := strings.Index(candidateLower, term)
		if idx < 0 {
			continue
		}
		score += 3

		// Bonus for multiple occurrences.
		if strings.Count(candidateLower, term) > 1 {
			score += 1
		}
	}

	return score
}

// stopWords is a set of common English words filtered from search queries.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "shall": true, "can": true,
	"not": true, "no": true, "nor": true,
	"and": true, "but": true, "or": true, "so": true, "yet": true,
	"for": true, "of": true, "at": true, "by": true, "to": true,
	"in": true, "on": true, "with": true, "from": true, "up": true,
	"about": true, "into": true, "through": true, "during": true,
	"before": true, "after": true, "above": true, "below": true,
	"between": true, "out": true, "off": true, "over": true, "under": true,
	"again": true, "further": true, "then": true, "once": true,
	"i": true, "me": true, "my": true, "we": true, "our": true,
	"you": true, "your": true, "he": true, "him": true, "his": true,
	"she": true, "her": true, "it": true, "its": true, "they": true,
	"them": true, "their": true, "what": true, "which": true, "who": true,
	"whom": true, "this": true, "that": true, "these": true, "those": true,
	"am": true, "if": true, "as": true, "also": true, "just": true,
	"how": true, "all": true, "each": true, "every": true, "both": true,
	"few": true, "more": true, "most": true, "other": true, "some": true,
	"such": true, "only": true, "own": true, "same": true, "than": true,
	"too": true, "very": true, "here": true, "there": true, "when": true,
	"where": true, "why": true, "any": true,
}

// tokenizeSearchTerms splits text into lowercase tokens, filtering stop words
// and tokens shorter than 3 characters.
func tokenizeSearchTerms(text string) []string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	var terms []string
	seen := make(map[string]bool, len(words))
	for _, w := range words {
		if len(w) < 3 || stopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, w)
	}
	return terms
}

// rawWindowBounds returns the [lo, hi) slice indices for a window of radius
// messages around anchor, clamped to [0, length).
func rawWindowBounds(anchor, length, radius int) (int, int) {
	lo := anchor - radius
	if lo < 0 {
		lo = 0
	}
	hi := anchor + radius + 1
	if hi > length {
		hi = length
	}
	return lo, hi
}

// collapseConsecutiveSameRole merges adjacent messages with the same role into
// a single message. This can happen when branch assembly places two assistant
// messages next to each other (e.g. a summary followed by a raw assistant
// message). Most providers reject consecutive same-role messages.
func collapseConsecutiveSameRole(msgs []ai.ChatMessage) []ai.ChatMessage {
	if len(msgs) <= 1 {
		return msgs
	}

	result := make([]ai.ChatMessage, 0, len(msgs))
	result = append(result, msgs[0])

	for i := 1; i < len(msgs); i++ {
		last := &result[len(result)-1]
		if msgs[i].Role == last.Role {
			last.Content += "\n\n" + msgs[i].Content
		} else {
			result = append(result, msgs[i])
		}
	}

	return result
}
