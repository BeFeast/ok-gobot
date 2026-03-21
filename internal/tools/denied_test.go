package tools

import (
	"fmt"
	"strings"
	"testing"
)

func TestToolDeniedError_Error(t *testing.T) {
	t.Parallel()

	tde := &ToolDeniedError{
		Tool:     "local",
		Family:   "local",
		Reason:   "estop active",
		ReEnable: "/estop off",
	}

	msg := tde.Error()
	if !strings.Contains(msg, `"local"`) {
		t.Errorf("Error() should contain tool name, got: %s", msg)
	}
	if !strings.Contains(msg, "denied") {
		t.Errorf("Error() should contain 'denied', got: %s", msg)
	}
}

func TestToolDeniedError_UserMessage(t *testing.T) {
	t.Parallel()

	tde := &ToolDeniedError{
		Tool:     "local",
		Family:   "local",
		Reason:   "estop active",
		ReEnable: "/estop off",
	}

	msg := tde.UserMessage()
	if !strings.Contains(msg, "local") {
		t.Errorf("UserMessage() should contain tool name, got: %s", msg)
	}
	if !strings.Contains(msg, "estop active") {
		t.Errorf("UserMessage() should contain reason, got: %s", msg)
	}
	if !strings.Contains(msg, "/estop off") {
		t.Errorf("UserMessage() should contain re-enable instruction, got: %s", msg)
	}
	if !strings.Contains(msg, "\U0001F6AB") {
		t.Errorf("UserMessage() should contain 🚫 emoji, got: %s", msg)
	}
}

func TestToolDeniedError_Markdown(t *testing.T) {
	t.Parallel()

	tde := &ToolDeniedError{
		Tool:     "browser",
		Family:   "browser",
		Reason:   "estop active",
		ReEnable: "/estop off",
	}

	md := tde.Markdown()
	if !strings.Contains(md, "`browser`") {
		t.Errorf("Markdown() should contain backtick-wrapped tool name, got: %s", md)
	}
	if !strings.Contains(md, "`/estop off`") {
		t.Errorf("Markdown() should contain backtick-wrapped re-enable, got: %s", md)
	}
	if !strings.Contains(md, `\(estop active\)`) {
		t.Errorf("Markdown() should escape parentheses for MarkdownV2, got: %s", md)
	}
}

func TestIsToolDenied(t *testing.T) {
	t.Parallel()

	tde := &ToolDeniedError{Tool: "ssh", Family: "ssh", Reason: "estop active", ReEnable: "/estop off"}

	got, ok := IsToolDenied(tde)
	if !ok || got != tde {
		t.Fatal("IsToolDenied should return true for *ToolDeniedError")
	}

	_, ok = IsToolDenied(fmt.Errorf("some other error"))
	if ok {
		t.Fatal("IsToolDenied should return false for non-ToolDeniedError")
	}

	_, ok = IsToolDenied(nil)
	if ok {
		t.Fatal("IsToolDenied should return false for nil")
	}

	wrapped := fmt.Errorf("executing tool: %w", tde)
	got2, ok := IsToolDenied(wrapped)
	if !ok || got2 != tde {
		t.Fatal("IsToolDenied should unwrap and return true for wrapped *ToolDeniedError")
	}
}
