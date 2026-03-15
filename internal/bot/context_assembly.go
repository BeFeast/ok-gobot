package bot

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"unicode"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/storage"
)

const (
	contextAssemblyLoadLimit         = 500
	chatHistoryBudgetFraction        = 0.40
	chatProtectedTailMessages        = 6
	jobContextPackBudgetFraction     = 0.55
	jobFreshTailMessages             = 4
	jobMaxRelevantTurns              = 4
	jobContextExcerptCharsPerMessage = 360
)

var contextKeywordStopwords = map[string]struct{}{
	"about":       {},
	"after":       {},
	"agent":       {},
	"background":  {},
	"before":      {},
	"build":       {},
	"check":       {},
	"continue":    {},
	"debug":       {},
	"file":        {},
	"files":       {},
	"finish":      {},
	"fix":         {},
	"from":        {},
	"help":        {},
	"into":        {},
	"investigate": {},
	"issue":       {},
	"job":         {},
	"look":        {},
	"please":      {},
	"repo":        {},
	"repository":  {},
	"review":      {},
	"task":        {},
	"that":        {},
	"these":       {},
	"this":        {},
	"those":       {},
	"with":        {},
	"work":        {},
}

type transcriptTurn struct {
	index    int
	messages []storage.SessionMessageV2
	score    int
}

func (b *Bot) loadChatHistory(sessionKey agent.SessionKey, model string) []ai.ChatMessage {
	msgs := b.loadTranscriptMessages(sessionKey, contextAssemblyLoadLimit)
	return buildChatHistoryFromTranscript(msgs, model)
}

func (b *Bot) buildJobContextPack(sessionKey agent.SessionKey, task, model string) string {
	msgs := b.loadTranscriptMessages(sessionKey, contextAssemblyLoadLimit)
	return buildJobContextPackFromTranscript(msgs, task, model)
}

func (b *Bot) loadTranscriptMessages(sessionKey agent.SessionKey, limit int) []storage.SessionMessageV2 {
	if b.store == nil {
		return nil
	}

	msgs, err := b.store.GetSessionMessagesV2(string(sessionKey), limit)
	if err != nil {
		log.Printf("[context] failed to load transcript for session %s: %v", sessionKey, err)
		return nil
	}
	return msgs
}

func sessionKeyForChatID(chatID int64) agent.SessionKey {
	if chatID > 0 {
		return agent.NewDMSessionKey(chatID)
	}
	return agent.NewGroupSessionKey(chatID)
}

func buildChatHistoryFromTranscript(msgs []storage.SessionMessageV2, model string) []ai.ChatMessage {
	if len(msgs) == 0 {
		return nil
	}
	return trimHistoryToTokenBudget(transcriptToChatMessages(msgs), model)
}

func buildJobContextPackFromTranscript(msgs []storage.SessionMessageV2, task, model string) string {
	if len(msgs) == 0 {
		return ""
	}

	budget := modelContextBudget(model, jobContextPackBudgetFraction)
	if budget <= 0 {
		return ""
	}

	tail := tailMessages(msgs, jobFreshTailMessages)
	tailSection := renderTurnSection("FRESH RECENT TAIL", groupTranscriptTurns(tail))
	intro := "Context pack for this background run. Older turns below were selected for relevance to the task. The fresh tail is contiguous and recent."

	sections := []string{intro}
	remainingBudget := budget - countTextTokens(intro) - countTextTokens(tailSection)
	if remainingBudget > 0 {
		relevantTurns := selectRelevantTurns(
			groupTranscriptTurns(msgs[:len(msgs)-len(tail)]),
			extractContextKeywords(task),
			remainingBudget,
		)
		if len(relevantTurns) > 0 {
			sections = append(sections, renderTurnSection("RELEVANT OLDER TURNS", relevantTurns))
		}
	}
	if tailSection != "" {
		sections = append(sections, tailSection)
	}

	return strings.TrimSpace(strings.Join(sections, "\n\n"))
}

func trimHistoryToTokenBudget(history []ai.ChatMessage, model string) []ai.ChatMessage {
	budget := modelContextBudget(model, chatHistoryBudgetFraction)
	if len(history) == 0 || budget <= 0 {
		return history
	}

	if len(history) <= chatProtectedTailMessages {
		return trimChatHistoryFront(history, budget)
	}

	protectedStart := len(history) - chatProtectedTailMessages
	protectedTail := append([]ai.ChatMessage(nil), history[protectedStart:]...)
	protectedTokens := countChatMessageTokens(protectedTail)
	if protectedTokens >= budget {
		return protectedTail
	}

	older := trimChatHistoryFront(history[:protectedStart], budget-protectedTokens)
	out := make([]ai.ChatMessage, 0, len(older)+len(protectedTail))
	out = append(out, older...)
	out = append(out, protectedTail...)
	return out
}

func trimChatHistoryFront(history []ai.ChatMessage, budget int) []ai.ChatMessage {
	if len(history) == 0 || budget <= 0 {
		return nil
	}

	trimmed := append([]ai.ChatMessage(nil), history...)
	total := countChatMessageTokens(trimmed)
	if total <= budget {
		return trimmed
	}

	for len(trimmed) > 0 && total > budget {
		dropCount := 1
		if len(trimmed) > 1 {
			dropCount = 2
		}
		for i := 0; i < dropCount && len(trimmed) > 0; i++ {
			total -= countChatMessageTokens(trimmed[:1])
			trimmed = trimmed[1:]
		}
	}

	return append([]ai.ChatMessage(nil), trimmed...)
}

func transcriptToChatMessages(msgs []storage.SessionMessageV2) []ai.ChatMessage {
	history := make([]ai.ChatMessage, 0, len(msgs))
	for _, msg := range msgs {
		history = append(history, ai.ChatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	return history
}

func tailMessages(msgs []storage.SessionMessageV2, count int) []storage.SessionMessageV2 {
	if count <= 0 || len(msgs) == 0 {
		return nil
	}
	if count >= len(msgs) {
		return append([]storage.SessionMessageV2(nil), msgs...)
	}
	return append([]storage.SessionMessageV2(nil), msgs[len(msgs)-count:]...)
}

func groupTranscriptTurns(msgs []storage.SessionMessageV2) []transcriptTurn {
	if len(msgs) == 0 {
		return nil
	}

	turns := make([]transcriptTurn, 0, (len(msgs)+1)/2)
	for i := 0; i < len(msgs); {
		turn := transcriptTurn{
			index: len(turns),
			messages: []storage.SessionMessageV2{
				msgs[i],
			},
		}
		i++
		if i < len(msgs) && turn.messages[0].Role == ai.RoleUser && msgs[i].Role == ai.RoleAssistant {
			turn.messages = append(turn.messages, msgs[i])
			i++
		}
		turns = append(turns, turn)
	}

	return turns
}

func selectRelevantTurns(turns []transcriptTurn, keywords []string, budget int) []transcriptTurn {
	if len(turns) == 0 || len(keywords) == 0 || budget <= 0 {
		return nil
	}

	candidates := make([]transcriptTurn, 0, len(turns))
	for _, turn := range turns {
		turn.score = scoreTranscriptTurn(turn, keywords)
		if turn.score > 0 {
			candidates = append(candidates, turn)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].index > candidates[j].index
	})

	selected := make([]transcriptTurn, 0, min(jobMaxRelevantTurns, len(candidates)))
	tc := agent.NewTokenCounter()
	for _, candidate := range candidates {
		if len(selected) >= jobMaxRelevantTurns {
			break
		}
		tentative := append(append([]transcriptTurn(nil), selected...), candidate)
		sort.Slice(tentative, func(i, j int) bool { return tentative[i].index < tentative[j].index })
		if countTextTokensWithCounter(tc, renderTurnSection("RELEVANT OLDER TURNS", tentative)) > budget {
			continue
		}
		selected = tentative
	}

	return selected
}

func scoreTranscriptTurn(turn transcriptTurn, keywords []string) int {
	if len(turn.messages) == 0 {
		return 0
	}

	var textParts []string
	hasRunID := false
	for _, msg := range turn.messages {
		textParts = append(textParts, normalizeContextText(msg.Content))
		if strings.TrimSpace(msg.RunID) != "" {
			hasRunID = true
		}
	}
	body := strings.Join(textParts, " ")
	if body == "" {
		return 0
	}

	score := 0
	for _, keyword := range keywords {
		if strings.Contains(body, keyword) {
			score += 10
		}
	}
	if score > 0 && hasRunID {
		score++
	}
	return score
}

func renderTurnSection(title string, turns []transcriptTurn) string {
	if len(turns) == 0 {
		return ""
	}

	var out strings.Builder
	out.WriteString(title)
	out.WriteString(":\n")
	for i, turn := range turns {
		if i > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(renderTranscriptTurn(turn))
	}
	return out.String()
}

func renderTranscriptTurn(turn transcriptTurn) string {
	lines := make([]string, 0, len(turn.messages))
	for _, msg := range turn.messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "message"
		}
		if strings.TrimSpace(msg.RunID) != "" {
			role += " job"
		}
		lines = append(lines, fmt.Sprintf("[%s] %s", role, contextExcerpt(msg.Content, jobContextExcerptCharsPerMessage)))
	}
	return strings.Join(lines, "\n")
}

func contextExcerpt(content string, maxChars int) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "(empty)"
	}
	content = normalizeWhitespace(content)
	if maxChars <= 0 {
		return content
	}

	runes := []rune(content)
	if len(runes) <= maxChars {
		return content
	}
	if maxChars <= 3 {
		return string(runes[:maxChars])
	}
	return strings.TrimSpace(string(runes[:maxChars-3])) + "..."
}

func extractContextKeywords(task string) []string {
	tokens := tokenizeContextText(task)
	seen := make(map[string]struct{}, len(tokens))
	keywords := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if len(token) < 3 {
			continue
		}
		if _, skip := contextKeywordStopwords[token]; skip {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		keywords = append(keywords, token)
	}
	return keywords
}

func tokenizeContextText(input string) []string {
	var normalized strings.Builder
	normalized.Grow(len(input))
	for _, r := range strings.ToLower(input) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			normalized.WriteRune(r)
		default:
			normalized.WriteByte(' ')
		}
	}
	return strings.Fields(normalized.String())
}

func normalizeContextText(input string) string {
	return strings.ToLower(normalizeWhitespace(input))
}

func normalizeWhitespace(input string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(input)), " ")
}

func countChatMessageTokens(msgs []ai.ChatMessage) int {
	if len(msgs) == 0 {
		return 0
	}

	tc := agent.NewTokenCounter()
	tokenMsgs := make([]agent.Message, 0, len(msgs))
	for _, msg := range msgs {
		tokenMsgs = append(tokenMsgs, agent.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	return tc.CountMessages(tokenMsgs)
}

func countTextTokens(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	return countTextTokensWithCounter(agent.NewTokenCounter(), text)
}

func countTextTokensWithCounter(tc *agent.TokenCounter, text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	if tc == nil {
		tc = agent.NewTokenCounter()
	}
	return tc.CountTokens(text)
}

func modelContextBudget(model string, fraction float64) int {
	if fraction <= 0 {
		return 0
	}
	return int(float64(agent.ModelLimits(model)) * fraction)
}
