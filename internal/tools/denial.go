package tools

import "fmt"

// ToolDenial is a structured error returned when a tool call is blocked by
// policy (e.g. estop). It carries enough context for every rendering layer
// (Telegram, TUI, API, AI model) to produce a clear, actionable message.
type ToolDenial struct {
	ToolName    string // e.g. "local"
	Family      string // e.g. "local", "browser"
	Reason      string // human-readable reason, e.g. "estop active"
	Remediation string // how to re-enable, e.g. "Run `/estop off` to re-enable."
}

func (d *ToolDenial) Error() string {
	return fmt.Sprintf("tool %q denied: %s", d.ToolName, d.Reason)
}

// FormatMarkdown returns a Telegram-friendly markdown string.
func (d *ToolDenial) FormatMarkdown() string {
	msg := fmt.Sprintf("🚫 Tool %q is disabled (%s)", d.ToolName, d.Reason)
	if d.Remediation != "" {
		msg += "\n" + d.Remediation
	}
	return msg
}

// FormatPlain returns a plain-text version suitable as a tool result for the AI model.
func (d *ToolDenial) FormatPlain() string {
	msg := fmt.Sprintf("DENIED: tool %q is disabled (%s).", d.ToolName, d.Reason)
	if d.Remediation != "" {
		msg += " " + d.Remediation
	}
	return msg
}

// IsToolDenial checks whether err (or its chain) is a *ToolDenial
// and returns it if so.
func IsToolDenial(err error) (*ToolDenial, bool) {
	if err == nil {
		return nil, false
	}
	if d, ok := err.(*ToolDenial); ok {
		return d, true
	}
	return nil, false
}
