package cli

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"ok-gobot/internal/storage"
)

func TestSessionsIndexRequiresMemoryEnabled(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)
	cfg.Memory.Enabled = false

	cmd := newSessionsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"index"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "memory.enabled is false") {
		t.Fatalf("expected memory.enabled error, got %v", err)
	}
}

func TestSessionsShowReturnsSpanAroundAnchor(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	sessionKey := "agent:default:telegram:dm:42"
	if err := store.UpsertSessionV2(&storage.SessionV2{SessionKey: sessionKey, AgentID: "default"}); err != nil {
		t.Fatalf("UpsertSessionV2: %v", err)
	}
	for i := 0; i < 5; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if err := store.SaveSessionMessageV2(sessionKey, role, "message body for turn", "run-1"); err != nil {
			t.Fatalf("SaveSessionMessageV2: %v", err)
		}
	}

	// fetch ids to find the third message id
	msgs, err := store.GetSessionMessagesV2(sessionKey, 100)
	if err != nil {
		t.Fatalf("GetSessionMessagesV2: %v", err)
	}
	if len(msgs) < 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}
	anchor := msgs[2].ID

	cmd := newSessionsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"show", sessionKey,
		"--around", strString(anchor),
		"--span", "1",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "fingerprint=") {
		t.Errorf("expected fingerprint label, got: %q", output)
	}
	if !strings.Contains(output, "Messages: 3") {
		t.Errorf("expected 3 messages in span, got: %q", output)
	}
	// Ensure the anchor row uses the highlight marker.
	if !strings.Contains(output, "> msg "+strString(anchor)) {
		t.Errorf("expected anchor marker, got: %q", output)
	}
}

func strString(n int64) string {
	return strconv.FormatInt(n, 10)
}
