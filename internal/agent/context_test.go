package agent

import (
	"strings"
	"testing"

	"ok-gobot/internal/ai"
)

func msg(role, content string) ai.ChatMessage {
	return ai.ChatMessage{Role: role, Content: content}
}

func TestAssembleContext_ChatMode_SystemAndUserAlways(t *testing.T) {
	out := AssembleContext(ContextModeChat, "sys", nil, msg(ai.RoleUser, "hi"), "gpt-4o")

	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	if out[0].Role != ai.RoleSystem || out[0].Content != "sys" {
		t.Errorf("first message should be system prompt, got %s: %q", out[0].Role, out[0].Content)
	}
	if out[len(out)-1].Role != ai.RoleUser || out[len(out)-1].Content != "hi" {
		t.Errorf("last message should be user message, got %s: %q", out[len(out)-1].Role, out[len(out)-1].Content)
	}
}

func TestAssembleContext_ChatMode_HistoryPreserved(t *testing.T) {
	history := []ai.ChatMessage{
		msg(ai.RoleUser, "first"),
		msg(ai.RoleAssistant, "reply"),
	}

	out := AssembleContext(ContextModeChat, "sys", history, msg(ai.RoleUser, "hi"), "gpt-4o")

	// system + 2 history + user = 4
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(out))
	}
	if out[1].Content != "first" {
		t.Errorf("expected first history message, got %q", out[1].Content)
	}
	if out[2].Content != "reply" {
		t.Errorf("expected second history message, got %q", out[2].Content)
	}
}

func TestAssembleContext_ChatMode_ProtectedTailSurvives(t *testing.T) {
	// Build history where the first 10 messages are huge (should be evicted)
	// and the last 10 are small (should be protected).
	var history []ai.ChatMessage
	huge := strings.Repeat("x", 20000) // ~5000 tokens each
	for i := 0; i < 10; i++ {
		history = append(history, msg(ai.RoleUser, huge))
		history = append(history, msg(ai.RoleAssistant, huge))
	}
	// Add 10 small protected-tail messages.
	for i := 0; i < chatTailProtected; i++ {
		role := ai.RoleUser
		if i%2 == 1 {
			role = ai.RoleAssistant
		}
		history = append(history, msg(role, "tail"))
	}

	// Use a model with small context window to force trimming.
	out := AssembleContext(ContextModeChat, "sys", history, msg(ai.RoleUser, "hi"), "gpt-4")

	// All chatTailProtected messages should survive.
	// system + tail messages + user
	if len(out) < chatTailProtected+2 {
		t.Fatalf("expected at least %d messages (sys+tail+user), got %d", chatTailProtected+2, len(out))
	}

	// Verify the last messages before the user message are the tail.
	for i := len(out) - 2; i >= len(out)-1-chatTailProtected && i >= 1; i-- {
		if out[i].Content != "tail" {
			t.Errorf("message %d should be protected tail, got %q", i, out[i].Content)
		}
	}
}

func TestAssembleContext_ChatMode_SmallHistoryUnchanged(t *testing.T) {
	// History smaller than chatTailProtected should pass through unchanged.
	history := []ai.ChatMessage{
		msg(ai.RoleUser, "a"),
		msg(ai.RoleAssistant, "b"),
	}

	out := AssembleContext(ContextModeChat, "sys", history, msg(ai.RoleUser, "hi"), "gpt-4o")

	// system + 2 history + user = 4
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(out))
	}
}

func TestAssembleContext_JobMode_LimitsHistory(t *testing.T) {
	var history []ai.ChatMessage
	for i := 0; i < 20; i++ {
		role := ai.RoleUser
		if i%2 == 1 {
			role = ai.RoleAssistant
		}
		history = append(history, msg(role, "msg"))
	}

	out := AssembleContext(ContextModeJob, "sys", history, msg(ai.RoleUser, "task"), "gpt-4o")

	// system + jobHistoryMessages + user
	expected := 1 + jobHistoryMessages + 1
	if len(out) != expected {
		t.Fatalf("expected %d messages, got %d", expected, len(out))
	}

	// First message is system.
	if out[0].Role != ai.RoleSystem {
		t.Errorf("first message should be system, got %s", out[0].Role)
	}
	// Last message is user.
	if out[len(out)-1].Role != ai.RoleUser || out[len(out)-1].Content != "task" {
		t.Errorf("last message should be user task, got %s: %q", out[len(out)-1].Role, out[len(out)-1].Content)
	}
}

func TestAssembleContext_JobMode_SmallHistoryUnchanged(t *testing.T) {
	history := []ai.ChatMessage{
		msg(ai.RoleUser, "a"),
		msg(ai.RoleAssistant, "b"),
	}

	out := AssembleContext(ContextModeJob, "sys", history, msg(ai.RoleUser, "task"), "")

	// system + 2 history + user = 4
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(out))
	}
}

func TestAssembleContext_JobMode_NoHistory(t *testing.T) {
	out := AssembleContext(ContextModeJob, "sys", nil, msg(ai.RoleUser, "task"), "")

	// system + user = 2
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
}

func TestAssembleContext_JobMode_KeepsLastMessages(t *testing.T) {
	var history []ai.ChatMessage
	for i := 0; i < 10; i++ {
		history = append(history, msg(ai.RoleUser, strings.Repeat("old-", i+1)))
	}

	out := AssembleContext(ContextModeJob, "sys", history, msg(ai.RoleUser, "task"), "")

	// The kept history should be the LAST jobHistoryMessages, not the first.
	keptHistory := out[1 : len(out)-1] // skip system and user
	if len(keptHistory) != jobHistoryMessages {
		t.Fatalf("expected %d history messages, got %d", jobHistoryMessages, len(keptHistory))
	}
	// First kept message should be history[10-jobHistoryMessages].
	wantFirst := strings.Repeat("old-", 10-jobHistoryMessages+1)
	if keptHistory[0].Content != wantFirst {
		t.Errorf("expected first kept history %q, got %q", wantFirst, keptHistory[0].Content)
	}
}

func TestTrimChatHistory_EmptyHistory(t *testing.T) {
	out := TrimChatHistory(nil, "gpt-4o")
	if out != nil {
		t.Fatalf("expected nil, got %v", out)
	}
}

func TestTrimChatHistory_FitsInBudget(t *testing.T) {
	history := []ai.ChatMessage{
		msg(ai.RoleUser, "hello"),
		msg(ai.RoleAssistant, "hi"),
	}

	out := TrimChatHistory(history, "gpt-4o")
	if len(out) != 2 {
		t.Fatalf("expected 2 messages (fits budget), got %d", len(out))
	}
}
