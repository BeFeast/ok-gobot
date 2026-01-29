package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// MessageSender interface for sending messages (implemented by bot)
type MessageSender interface {
	SendToChat(chatID int64, text string) error
}

// MessageTool allows the agent to send messages to other chats
type MessageTool struct {
	sender    MessageSender
	allowlist map[int64]string // chatID -> alias mapping
}

// NewMessageTool creates a new message tool
func NewMessageTool(sender MessageSender) *MessageTool {
	return &MessageTool{
		sender:    sender,
		allowlist: make(map[int64]string),
	}
}

// AddAllowedChat adds a chat to the allowlist
func (m *MessageTool) AddAllowedChat(chatID int64, alias string) {
	m.allowlist[chatID] = alias
}

func (m *MessageTool) Name() string {
	return "message"
}

func (m *MessageTool) Description() string {
	var allowed []string
	for id, alias := range m.allowlist {
		allowed = append(allowed, fmt.Sprintf("%s (%d)", alias, id))
	}
	if len(allowed) == 0 {
		return "Send messages to other chats (no chats configured)"
	}
	return fmt.Sprintf("Send messages to: %s", strings.Join(allowed, ", "))
}

func (m *MessageTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("usage: message <to> <text>\nAllowed targets: %s", m.listTargets())
	}

	to := args[0]
	text := strings.Join(args[1:], " ")

	// Resolve target
	chatID, err := m.resolveTarget(to)
	if err != nil {
		return "", err
	}

	// Check allowlist
	if _, ok := m.allowlist[chatID]; !ok && len(m.allowlist) > 0 {
		return "", fmt.Errorf("chat %d is not in allowlist", chatID)
	}

	// Send message
	if m.sender == nil {
		return "", fmt.Errorf("message sender not configured")
	}

	if err := m.sender.SendToChat(chatID, text); err != nil {
		return "", fmt.Errorf("failed to send message: %w", err)
	}

	alias := m.allowlist[chatID]
	if alias != "" {
		return fmt.Sprintf("✅ Message sent to %s", alias), nil
	}
	return fmt.Sprintf("✅ Message sent to chat %d", chatID), nil
}

// resolveTarget converts a target string to a chat ID
func (m *MessageTool) resolveTarget(target string) (int64, error) {
	// Try direct chat ID
	if chatID, err := strconv.ParseInt(target, 10, 64); err == nil {
		return chatID, nil
	}

	// Try alias lookup
	for id, alias := range m.allowlist {
		if strings.EqualFold(alias, target) {
			return id, nil
		}
	}

	return 0, fmt.Errorf("unknown target: %s. Use chat ID or one of: %s", target, m.listTargets())
}

// listTargets returns a list of allowed targets
func (m *MessageTool) listTargets() string {
	var targets []string
	for id, alias := range m.allowlist {
		if alias != "" {
			targets = append(targets, alias)
		} else {
			targets = append(targets, fmt.Sprintf("%d", id))
		}
	}
	if len(targets) == 0 {
		return "(none configured)"
	}
	return strings.Join(targets, ", ")
}
