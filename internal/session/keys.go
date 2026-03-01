package session

import "fmt"

// Key formats:
//
//	agent:<agentId>:main
//	agent:<agentId>:telegram:dm:<userId>
//	agent:<agentId>:telegram:group:<chatId>
//	agent:<agentId>:telegram:group:<chatId>:thread:<topicId>
//	agent:<agentId>:subagent:<runSlug>

// AgentMain returns the canonical key for an agent's primary (non-transport) session.
func AgentMain(agentID string) string {
	return fmt.Sprintf("agent:%s:main", agentID)
}

// TelegramDM returns the canonical key for a per-user DM session.
// Use when dm_scope=per_user is configured.
func TelegramDM(agentID string, userID int64) string {
	return fmt.Sprintf("agent:%s:telegram:dm:%d", agentID, userID)
}

// TelegramGroup returns the canonical key for a group chat session.
func TelegramGroup(agentID string, chatID int64) string {
	return fmt.Sprintf("agent:%s:telegram:group:%d", agentID, chatID)
}

// TelegramGroupThread returns the canonical key for a group topic/thread session.
func TelegramGroupThread(agentID string, chatID int64, topicID int) string {
	return fmt.Sprintf("agent:%s:telegram:group:%d:thread:%d", agentID, chatID, topicID)
}

// Subagent returns the canonical key for a sub-agent run session.
func Subagent(agentID, runSlug string) string {
	return fmt.Sprintf("agent:%s:subagent:%s", agentID, runSlug)
}
