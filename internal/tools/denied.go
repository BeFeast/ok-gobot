package tools

import (
	"errors"
	"fmt"
)

// ToolDeniedError is returned when a tool call is blocked by policy (e.g. estop).
// It carries structured information so every surface (Telegram, TUI, API, AI model)
// can render a clear, actionable denial message.
type ToolDeniedError struct {
	Tool     string // tool name that was called
	Family   string // dangerous tool family (e.g. "local", "browser")
	Reason   string // human-readable reason (e.g. "estop active")
	ReEnable string // instruction to re-enable (e.g. "/estop off")
}

func (e *ToolDeniedError) Error() string {
	return fmt.Sprintf("tool %q denied: %s (family %q)", e.Tool, e.Reason, e.Family)
}

// Markdown returns a Telegram-friendly markdown-formatted denial message.
func (e *ToolDeniedError) Markdown() string {
	return fmt.Sprintf(
		"\U0001F6AB Tool `%s` is disabled \\(%s\\)\nRun `%s` to re\\-enable\\.",
		e.Tool, e.Reason, e.ReEnable,
	)
}

// UserMessage returns a plain-text denial message suitable for the AI model
// to relay to the user.
func (e *ToolDeniedError) UserMessage() string {
	return fmt.Sprintf(
		"\U0001F6AB Tool \"%s\" is disabled (%s)\nRun `%s` to re-enable.",
		e.Tool, e.Reason, e.ReEnable,
	)
}

// IsToolDenied reports whether err is a *ToolDeniedError, returning it if so.
func IsToolDenied(err error) (*ToolDeniedError, bool) {
	if err == nil {
		return nil, false
	}
	var tde *ToolDeniedError
	if errors.As(err, &tde) {
		return tde, true
	}
	return nil, false
}
