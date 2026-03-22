package control

import "testing"

func TestLegacyEventToTUI_ToolDenied(t *testing.T) {
	t.Parallel()

	msgs := legacyEventToTUI(EvtToolDenied, ToolDeniedPayload{
		ChatID:      123,
		ToolName:    "local",
		Family:      "local",
		Reason:      "estop active",
		Remediation: "Run `/estop off` to re-enable.",
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.Type != MsgTypeEvent {
		t.Errorf("Type = %q, want %q", m.Type, MsgTypeEvent)
	}
	if m.Kind != KindToolDenied {
		t.Errorf("Kind = %q, want %q", m.Kind, KindToolDenied)
	}
	if m.ToolName != "local" {
		t.Errorf("ToolName = %q, want %q", m.ToolName, "local")
	}
	if m.DenyReason != "estop active" {
		t.Errorf("DenyReason = %q, want %q", m.DenyReason, "estop active")
	}
	if m.DenyRemediation == "" {
		t.Error("DenyRemediation should not be empty")
	}
	if m.SessionID != "123" {
		t.Errorf("SessionID = %q, want %q", m.SessionID, "123")
	}
}

func TestLegacyEventToTUI_ToolDeniedPointer(t *testing.T) {
	t.Parallel()

	msgs := legacyEventToTUI(EvtToolDenied, &ToolDeniedPayload{
		ChatID:   456,
		ToolName: "ssh",
		Family:   "ssh",
		Reason:   "estop active",
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Kind != KindToolDenied {
		t.Errorf("Kind = %q, want %q", msgs[0].Kind, KindToolDenied)
	}
}

func TestLegacyEventToTUI_ToolDeniedBadPayload(t *testing.T) {
	t.Parallel()

	msgs := legacyEventToTUI(EvtToolDenied, "bad payload")
	if msgs != nil {
		t.Fatalf("expected nil for bad payload, got %v", msgs)
	}
}

func TestLegacyEventToTUI_ToolFinishedStillWorks(t *testing.T) {
	t.Parallel()

	msgs := legacyEventToTUI(EvtToolFinished, ToolEventPayload{
		ChatID:   789,
		ToolName: "file",
		Output:   "ok",
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Kind != KindToolEnd {
		t.Errorf("Kind = %q, want %q", msgs[0].Kind, KindToolEnd)
	}
	if msgs[0].ToolResult != "ok" {
		t.Errorf("ToolResult = %q, want %q", msgs[0].ToolResult, "ok")
	}
}
