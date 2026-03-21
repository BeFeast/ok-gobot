package control

import (
	"strings"
	"testing"
)

func TestLegacyEventToTUI_ToolDenied(t *testing.T) {
	t.Parallel()

	payload := ToolDeniedPayload{
		ChatID:   42,
		ToolName: "local",
		Family:   "local",
		Reason:   "estop active",
		ReEnable: "/estop off",
	}

	msgs := legacyEventToTUI(EvtToolDenied, payload)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	msg := msgs[0]
	if msg.Kind != KindToolEnd {
		t.Errorf("expected kind %q, got %q", KindToolEnd, msg.Kind)
	}
	if msg.ToolName != "local" {
		t.Errorf("expected tool_name %q, got %q", "local", msg.ToolName)
	}
	if !strings.Contains(msg.ToolError, "local") {
		t.Errorf("ToolError should contain tool name, got: %s", msg.ToolError)
	}
	if !strings.Contains(msg.ToolError, "estop active") {
		t.Errorf("ToolError should contain reason, got: %s", msg.ToolError)
	}
	if !strings.Contains(msg.ToolError, "/estop off") {
		t.Errorf("ToolError should contain re-enable instruction, got: %s", msg.ToolError)
	}
}

func TestLegacyEventToTUI_ToolDeniedPointer(t *testing.T) {
	t.Parallel()

	payload := &ToolDeniedPayload{
		ChatID:   42,
		ToolName: "ssh",
		Family:   "ssh",
		Reason:   "estop active",
		ReEnable: "/estop off",
	}

	msgs := legacyEventToTUI(EvtToolDenied, payload)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].ToolName != "ssh" {
		t.Errorf("expected tool_name %q, got %q", "ssh", msgs[0].ToolName)
	}
}

func TestLegacyEventToTUI_ToolDeniedNilPointer(t *testing.T) {
	t.Parallel()

	msgs := legacyEventToTUI(EvtToolDenied, (*ToolDeniedPayload)(nil))
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages for nil pointer, got %d", len(msgs))
	}
}
