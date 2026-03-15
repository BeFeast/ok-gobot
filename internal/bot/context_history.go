package bot

import (
	"slices"
	"strings"
	"unicode"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/storage"
)

const compactedSummaryPrefix = "[Compacted conversation summary]"

var contextSearchStopwords = map[string]struct{}{
	"about": {},
	"after": {},
	"and":   {},
	"from":  {},
	"have":  {},
	"into":  {},
	"just":  {},
	"that":  {},
	"the":   {},
	"them":  {},
	"then":  {},
	"they":  {},
	"this":  {},
	"what":  {},
	"when":  {},
	"were":  {},
	"will":  {},
	"with":  {},
	"your":  {},
}

type historyBranch struct {
	summaryIndex int
	rawIndices   []int
}

type branchHit struct {
	branchIndex  int
	messageIndex int
	rawPos       int
	score        int
	isSummary    bool
}

// buildRunHistory assembles history for the current request.
// When the transcript contains compaction summaries, it searches summary anchors
// and raw transcript messages together, then expands only the best-matching
// branch instead of replaying the entire post-compaction history.
func buildRunHistory(v2Msgs []storage.SessionMessageV2, query, model string) []ai.ChatMessage {
	if len(v2Msgs) == 0 {
		return nil
	}

	if targeted := buildSearchExpandedHistory(v2Msgs, query); len(targeted) > 0 {
		return trimHistoryToTokenBudget(targeted, model)
	}

	history := make([]ai.ChatMessage, 0, len(v2Msgs))
	for _, m := range v2Msgs {
		history = append(history, ai.ChatMessage{Role: m.Role, Content: m.Content})
	}
	return trimHistoryToTokenBudget(history, model)
}

func buildSearchExpandedHistory(v2Msgs []storage.SessionMessageV2, query string) []ai.ChatMessage {
	branches, hasSummary := splitHistoryBranches(v2Msgs)
	if !hasSummary {
		return nil
	}

	queryTokens := tokenizeContextSearchTerms(query)
	if len(queryTokens) == 0 {
		return nil
	}

	queryPhrase := strings.Join(queryTokens, " ")
	var (
		bestHit      branchHit
		hasHit       bool
		hitsByBranch = make(map[int][]branchHit)
	)

	for branchIndex, branch := range branches {
		if branch.summaryIndex >= 0 {
			score := scoreContextCandidate(queryTokens, queryPhrase, summarySearchText(v2Msgs[branch.summaryIndex].Content))
			if score > 0 {
				score++
				hit := branchHit{
					branchIndex:  branchIndex,
					messageIndex: branch.summaryIndex,
					rawPos:       -1,
					score:        score,
					isSummary:    true,
				}
				hitsByBranch[branchIndex] = append(hitsByBranch[branchIndex], hit)
				if !hasHit || betterBranchHit(hit, bestHit) {
					bestHit = hit
					hasHit = true
				}
			}
		}

		for rawPos, msgIndex := range branch.rawIndices {
			score := scoreContextCandidate(queryTokens, queryPhrase, v2Msgs[msgIndex].Content)
			if score <= 0 {
				continue
			}
			hit := branchHit{
				branchIndex:  branchIndex,
				messageIndex: msgIndex,
				rawPos:       rawPos,
				score:        score,
			}
			hitsByBranch[branchIndex] = append(hitsByBranch[branchIndex], hit)
			if !hasHit || betterBranchHit(hit, bestHit) {
				bestHit = hit
				hasHit = true
			}
		}
	}

	if !hasHit {
		return nil
	}

	branch := branches[bestHit.branchIndex]
	hits := hitsByBranch[bestHit.branchIndex]
	slices.SortFunc(hits, func(a, b branchHit) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return b.messageIndex - a.messageIndex
	})

	selected := make(map[int]struct{})
	if branch.summaryIndex >= 0 {
		selected[branch.summaryIndex] = struct{}{}
	}

	const maxBranchHits = 2
	selectedHits := 0
	for _, hit := range hits {
		if hit.isSummary {
			continue
		}
		start, end := rawWindowBounds(branch.rawIndices, hit.rawPos, v2Msgs)
		for _, idx := range branch.rawIndices[start : end+1] {
			selected[idx] = struct{}{}
		}
		selectedHits++
		if selectedHits >= maxBranchHits {
			break
		}
	}

	if len(selected) == 0 {
		return nil
	}

	return collapseSelectedHistory(v2Msgs, selected)
}

func splitHistoryBranches(v2Msgs []storage.SessionMessageV2) ([]historyBranch, bool) {
	branches := make([]historyBranch, 0, 4)
	current := historyBranch{summaryIndex: -1}
	hasSummary := false

	for idx, msg := range v2Msgs {
		if isCompactionSummary(msg.Content) {
			if current.summaryIndex >= 0 || len(current.rawIndices) > 0 {
				branches = append(branches, current)
			}
			current = historyBranch{summaryIndex: idx}
			hasSummary = true
			continue
		}
		current.rawIndices = append(current.rawIndices, idx)
	}

	if current.summaryIndex >= 0 || len(current.rawIndices) > 0 {
		branches = append(branches, current)
	}

	return branches, hasSummary
}

func rawWindowBounds(rawIndices []int, rawPos int, msgs []storage.SessionMessageV2) (int, int) {
	const radius = 2

	start := rawPos - radius
	if start < 0 {
		start = 0
	}
	end := rawPos + radius
	if end >= len(rawIndices) {
		end = len(rawIndices) - 1
	}

	if start > 0 && msgs[rawIndices[start]].Role == ai.RoleAssistant {
		start--
	}
	if end < len(rawIndices)-1 && msgs[rawIndices[end]].Role == ai.RoleUser {
		end++
	}

	return start, end
}

func collapseSelectedHistory(v2Msgs []storage.SessionMessageV2, selected map[int]struct{}) []ai.ChatMessage {
	indices := make([]int, 0, len(selected))
	for idx := range selected {
		indices = append(indices, idx)
	}
	slices.Sort(indices)

	history := make([]ai.ChatMessage, 0, len(indices))
	for _, idx := range indices {
		msg := ai.ChatMessage{Role: v2Msgs[idx].Role, Content: v2Msgs[idx].Content}
		if len(history) > 0 && history[len(history)-1].Role == msg.Role {
			if history[len(history)-1].Content != "" && msg.Content != "" {
				history[len(history)-1].Content += "\n\n"
			}
			history[len(history)-1].Content += msg.Content
			continue
		}
		history = append(history, msg)
	}
	return history
}

func betterBranchHit(candidate, current branchHit) bool {
	if candidate.score != current.score {
		return candidate.score > current.score
	}
	return candidate.messageIndex > current.messageIndex
}

func scoreContextCandidate(queryTokens []string, queryPhrase, text string) int {
	lower := strings.ToLower(text)
	score := 0
	if queryPhrase != "" && strings.Contains(lower, queryPhrase) {
		score += 6
	}

	for _, token := range queryTokens {
		count := strings.Count(lower, token)
		if count == 0 {
			continue
		}
		score += 3
		if count > 1 {
			score += min(count-1, 2)
		}
	}

	return score
}

func tokenizeContextSearchTerms(text string) []string {
	var (
		buf    []rune
		tokens []string
		seen   = make(map[string]struct{})
	)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		token := string(buf)
		buf = buf[:0]
		if len([]rune(token)) < 3 {
			return
		}
		if _, ok := contextSearchStopwords[token]; ok {
			return
		}
		if _, ok := seen[token]; ok {
			return
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}

	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			buf = append(buf, r)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

func isCompactionSummary(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), compactedSummaryPrefix)
}

func summarySearchText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, compactedSummaryPrefix)
	return strings.TrimSpace(text)
}
