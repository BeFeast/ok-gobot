package agent

import (
	"strings"
)

// Safety implements safety rules and stop phrases
type Safety struct {
	// Stop phrases that immediately halt all actions
	StopPhrases []string
}

// NewSafety creates a new safety manager
func NewSafety() *Safety {
	return &Safety{
		StopPhrases: []string{
			"стоп",
			"stop",
			"остановись",
			"halt",
			"pause",
		},
	}
}

// IsStopPhrase checks if the message contains a stop phrase
func (s *Safety) IsStopPhrase(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))

	for _, phrase := range s.StopPhrases {
		if lower == phrase {
			return true
		}
	}

	// Also check for Russian variations
	if strings.Contains(lower, "стоп") ||
		strings.Contains(lower, "остановись") {
		return true
	}

	return false
}

// ShouldAskBeforeAction determines if an action requires explicit approval
func (s *Safety) ShouldAskBeforeAction(actionType string) bool {
	// Actions that should always ask first
	dangerous := []string{
		"email",
		"send_email",
		"tweet",
		"post",
		"delete",
		"rm",
		"remove",
	}

	for _, d := range dangerous {
		if strings.Contains(strings.ToLower(actionType), d) {
			return true
		}
	}

	return false
}

// IsSafeToDoFreely checks if an action is safe without asking
func (s *Safety) IsSafeToDoFreely(actionType string) bool {
	safe := []string{
		"read",
		"explore",
		"search",
		"organize",
		"learn",
		"analyze",
	}

	for _, s := range safe {
		if strings.Contains(strings.ToLower(actionType), s) {
			return true
		}
	}

	return false
}

// CommunicationRules defines how to communicate in different contexts
type CommunicationRules struct{}

// ShouldRespondInGroup determines if agent should respond in group chat
func (c *CommunicationRules) ShouldRespondInGroup(
	isMentioned bool,
	isQuestion bool,
	addsValue bool,
	isCasualBanter bool,
) bool {
	// Always respond when directly mentioned or asked a question
	if isMentioned || isQuestion {
		return true
	}

	// Respond if adding genuine value
	if addsValue && !isCasualBanter {
		return true
	}

	// Stay silent during casual banter
	return false
}

// FormatForPlatform formats text for specific platforms
func (c *CommunicationRules) FormatForPlatform(platform, text string) string {
	switch platform {
	case "discord", "whatsapp":
		// No markdown tables - use bullet lists
		return c.convertTablesToLists(text)
	case "telegram":
		// Telegram supports markdown
		return text
	default:
		return text
	}
}

// convertTablesToLists converts markdown tables to bullet lists
func (c *CommunicationRules) convertTablesToLists(text string) string {
	// Simple conversion - in practice, use proper markdown parsing
	if !strings.Contains(text, "|") {
		return text
	}

	lines := strings.Split(text, "\n")
	var result []string
	inTable := false

	for _, line := range lines {
		if strings.Contains(line, "|") && !strings.Contains(line, "http") {
			// This is likely a table row
			if !inTable {
				inTable = true
			}
			// Skip separator lines
			if strings.Contains(line, "---") {
				continue
			}
			// Convert to bullet
			parts := strings.Split(line, "|")
			if len(parts) >= 3 {
				item := strings.TrimSpace(parts[1]) + ": " + strings.TrimSpace(parts[2])
				result = append(result, "- "+item)
			}
		} else {
			inTable = false
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n")
}

// GetStopPhraseResponse returns the standard stop phrase response
func GetStopPhraseResponse() string {
	return "Ок, жду"
}
