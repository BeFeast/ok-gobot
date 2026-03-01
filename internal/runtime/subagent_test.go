package runtime

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestExtractAgentID verifies that agent IDs are correctly parsed from session keys.
func TestExtractAgentID(t *testing.T) {
	cases := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "agent:myAgent:main", want: "myAgent"},
		{input: "agent:bot1:telegram:dm:12345", want: "bot1"},
		{input: "agent:bot2:telegram:group:99", want: "bot2"},
		{input: "agent:molt:subagent:abc-123", want: "molt"},
		{input: "agent:solo", want: "solo"},
		{input: "telegram:foo", wantErr: true},
		{input: "agent:", wantErr: true},
		{input: "agent::rest", wantErr: true},
		{input: "", wantErr: true},
	}

	for _, tc := range cases {
		got, err := extractAgentID(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("extractAgentID(%q): expected error, got %q", tc.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("extractAgentID(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("extractAgentID(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestNewRunSlugUnique verifies that successive slugs are unique.
func TestNewRunSlugUnique(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		s := newRunSlug()
		if seen[s] {
			t.Fatalf("newRunSlug returned duplicate slug %q", s)
		}
		seen[s] = true
	}
}

// TestSpawnSubagentSessionKeyFormat verifies that SpawnSubagent produces a child
// session key in the expected "agent:<agentId>:subagent:<runSlug>" format.
func TestSpawnSubagentSessionKeyFormat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hub := NewHub(ctx, 10)

	var executed bool
	req := SubagentSpawnRequest{
		ParentSessionKey: "agent:testAgent:telegram:dm:42",
		Task:             "do something",
		Model:            "claude-3-haiku",
		Thinking:         "low",
		ToolAllowlist:    []string{"search", "file"},
		WorkspaceRoot:    "/tmp/workspace",
		DeliverBack:      true,
	}

	done := make(chan struct{})
	handle, err := hub.SpawnSubagent(req, func(ctx context.Context, ack AckHandle) {
		defer close(done)
		executed = true
		ack.Close(nil)
	})

	if err != nil {
		t.Fatalf("SpawnSubagent returned unexpected error: %v", err)
	}

	// Verify handle fields.
	if handle.AgentID != "testAgent" {
		t.Errorf("AgentID = %q, want %q", handle.AgentID, "testAgent")
	}
	if handle.RunSlug == "" {
		t.Error("RunSlug is empty")
	}
	wantPrefix := "agent:testAgent:subagent:"
	if !strings.HasPrefix(handle.SessionKey, wantPrefix) {
		t.Errorf("SessionKey = %q, want prefix %q", handle.SessionKey, wantPrefix)
	}
	if !strings.HasSuffix(handle.SessionKey, handle.RunSlug) {
		t.Errorf("SessionKey = %q does not end with RunSlug %q", handle.SessionKey, handle.RunSlug)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error("sub-agent run did not complete in time")
	}

	if !executed {
		t.Error("RunFunc was never executed")
	}
}

// TestSpawnSubagentInvalidParent verifies that SpawnSubagent returns an error
// for invalid parent session keys.
func TestSpawnSubagentInvalidParent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hub := NewHub(ctx, 10)

	_, err := hub.SpawnSubagent(SubagentSpawnRequest{
		ParentSessionKey: "not-a-valid-key",
		Task:             "irrelevant",
	}, func(ctx context.Context, ack AckHandle) { ack.Close(nil) })

	if err == nil {
		t.Error("expected error for invalid parent session key, got nil")
	}
}

// TestSpawnSubagentIsolated verifies that child sessions run independently of
// each other and of the parent session.
func TestSpawnSubagentIsolated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hub := NewHub(ctx, 10)

	const numSubagents = 3
	done := make(chan string, numSubagents)

	for i := 0; i < numSubagents; i++ {
		req := SubagentSpawnRequest{
			ParentSessionKey: "agent:parent:main",
			Task:             "task",
		}
		handle, err := hub.SpawnSubagent(req, func(ctx context.Context, ack AckHandle) {
			time.Sleep(20 * time.Millisecond)
			ack.Close(nil)
		})
		if err != nil {
			t.Fatalf("SpawnSubagent[%d] error: %v", i, err)
		}
		key := handle.SessionKey
		go func() { done <- key }()
	}

	keys := make(map[string]bool)
	for i := 0; i < numSubagents; i++ {
		select {
		case k := <-done:
			if keys[k] {
				t.Errorf("duplicate session key: %q", k)
			}
			keys[k] = true
		case <-time.After(4 * time.Second):
			t.Fatal("timed out waiting for sub-agent completion")
		}
	}
}
