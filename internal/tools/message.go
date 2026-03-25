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

// MediaSender interface for sending photos/files (optionally implemented by bot)
type MediaSender interface {
	SendPhotoToChat(chatID int64, filePath, caption string) error
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

func (m *MessageTool) IsMutation(args ...string) bool {
	return true
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

// GetSchema returns the JSON Schema for message tool parameters
func (m *MessageTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"to": map[string]interface{}{
				"type":        "string",
				"description": "Target chat: alias name or numeric chat ID",
			},
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Message text to send (optional if photo is set)",
			},
			"photo": map[string]interface{}{
				"type":        "string",
				"description": "Absolute path to a local image file to send as a photo",
			},
		},
		"required": []string{"to"},
	}
}

func (m *MessageTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("usage: message <to> <text>\nAllowed targets: %s", m.listTargets())
	}
	to := args[0]
	text := strings.Join(args[1:], " ")
	return m.send(ctx, to, text, "")
}

// ExecuteJSON implements JSONExecutor for structured calls with photo support.
func (m *MessageTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	to := params["to"]
	if to == "" {
		return "", fmt.Errorf("'to' is required")
	}
	text := params["text"]
	photo := params["photo"]
	if text == "" && photo == "" {
		return "", fmt.Errorf("either 'text' or 'photo' is required")
	}
	res, err := m.send(ctx, to, text, photo)
	if err == nil {
		out := ToolResult{
			Message: res,
			Evidence: &Evidence{
				Output: res,
			},
		}
		return out.String(), nil
	}
	return res, err
}

func (m *MessageTool) send(ctx context.Context, to, text, photo string) (string, error) {
	chatID, err := m.resolveTarget(to)
	if err != nil {
		return "", err
	}

	if _, ok := m.allowlist[chatID]; !ok && len(m.allowlist) > 0 {
		return "", fmt.Errorf("chat %d is not in allowlist", chatID)
	}

	if m.sender == nil {
		return "", fmt.Errorf("message sender not configured")
	}

	alias := m.allowlist[chatID]
	label := fmt.Sprintf("chat %d", chatID)
	if alias != "" {
		label = alias
	}

	// Send photo if provided
	if photo != "" {
		ms, ok := m.sender.(MediaSender)
		if !ok {
			return "", fmt.Errorf("photo sending not supported by this bot instance")
		}
		if err := ms.SendPhotoToChat(chatID, photo, text); err != nil {
			return "", fmt.Errorf("failed to send photo: %w", err)
		}
		return fmt.Sprintf("✅ Photo sent to %s", label), nil
	}

	// Text message
	if err := m.sender.SendToChat(chatID, text); err != nil {
		return "", fmt.Errorf("failed to send message: %w", err)
	}
	return fmt.Sprintf("✅ Message sent to %s", label), nil
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
