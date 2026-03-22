package control

import "testing"

func TestLegacyEventToTUI_ToolDenied(t *testing.T) {
	t.Parallel()

	payload := ToolDeniedPayload{
		ChatID:   42,
		ToolName: "local",
		Family:   "local",
		Reason:   "estop active",
		Hint:     "Run /estop off to re-enable.",
	}

	msgs := legacyEventToTUI(EvtToolDenied, payload)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	msg := msgs[0]
	if msg.Kind != KindToolDenied {
		t.Errorf("expected Kind=%s, got %s", KindToolDenied, msg.Kind)
	}
	if msg.ToolName != "local" {
		t.Errorf("expected ToolName=local, got %s", msg.ToolName)
	}
	if msg.ToolFamily != "local" {
		t.Errorf("expected ToolFamily=local, got %s", msg.ToolFamily)
	}
	if msg.DenialReason != "estop active" {
		t.Errorf("expected DenialReason='estop active', got %s", msg.DenialReason)
	}
	if msg.DenialHint != "Run /estop off to re-enable." {
		t.Errorf("expected DenialHint, got %s", msg.DenialHint)
	}
}

func TestLegacyEventToTUI_ToolDenied_PointerPayload(t *testing.T) {
	t.Parallel()

	payload := &ToolDeniedPayload{
		ChatID:   42,
		ToolName: "ssh",
		Family:   "ssh",
		Reason:   "estop active",
	}

	msgs := legacyEventToTUI(EvtToolDenied, payload)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].ToolName != "ssh" {
		t.Errorf("expected ToolName=ssh, got %s", msgs[0].ToolName)
	}
}

func TestLegacyEventToTUI_ToolDenied_WrongPayload(t *testing.T) {
	t.Parallel()

	msgs := legacyEventToTUI(EvtToolDenied, "not a payload")
	if msgs != nil {
		t.Fatalf("expected nil for wrong payload type, got %v", msgs)
	}
}
