package tools

import "fmt"

// ToolDenial is a structured error returned when a tool call is blocked by
// policy (e.g. estop). It carries enough context for every rendering surface
// (Telegram, TUI, API, AI model) to produce a clear, actionable message.
type ToolDenial struct {
	ToolName string // the tool that was called
	Family   string // the dangerous-tool family (e.g. "local", "browser")
	Reason   string // human-readable reason (e.g. "estop active")
	Hint     string // how to re-enable (e.g. `/estop off`)
}

func (d *ToolDenial) Error() string {
	return fmt.Sprintf("tool %q denied: %s", d.ToolName, d.Reason)
}

// FormatTelegram returns the denial formatted as a Telegram-friendly message.
func (d *ToolDenial) FormatTelegram() string {
	msg := fmt.Sprintf("🚫 Tool %q is disabled (%s)", d.ToolName, d.Reason)
	if d.Hint != "" {
		msg += "\n" + d.Hint
	}
	return msg
}

// FormatPlain returns a plain-text rendering suitable for the AI model tool
// result so the model can explain the denial to the user.
func (d *ToolDenial) FormatPlain() string {
	msg := fmt.Sprintf("DENIED: Tool %q is disabled (%s).", d.ToolName, d.Reason)
	if d.Family != "" {
		msg += fmt.Sprintf(" Tool family: %s.", d.Family)
	}
	if d.Hint != "" {
		msg += " " + d.Hint
	}
	return msg
}

// IsToolDenial extracts a *ToolDenial from an error, returning nil if the
// error is not a denial.
func IsToolDenial(err error) *ToolDenial {
	if err == nil {
		return nil
	}
	if d, ok := err.(*ToolDenial); ok {
		return d
	}
	return nil
}
