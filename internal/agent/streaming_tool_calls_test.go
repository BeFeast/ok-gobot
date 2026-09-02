package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/tools"
)

// recordingTool stands in for image_gen: the chat-bound tool the real inbound
// path registers and the harness never did.
type recordingTool struct {
	mu     sync.Mutex
	called []string
}

func (t *recordingTool) Name() string { return "image_gen" }

func (t *recordingTool) Description() string {
	return "Generate an image from a text description."
}

func (t *recordingTool) Execute(ctx context.Context, args ...string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.called = append(t.called, strings.Join(args, " "))
	return "image delivered to the chat", nil
}

func (t *recordingTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{"type": "string"},
		},
		"required": []string{"prompt"},
	}
}

func (t *recordingTool) calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.called)
}

// realInboundGateway replays the two turns of a real tool-using exchange in the
// exact SSE shape captured from the production gateway on 2026-09-01: the first
// turn streams tool_calls and terminates with an EMPTY delta carrying
// finish_reason "tool_calls" before [DONE]; the second turn answers with text.
func realInboundGateway(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	turn := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		turn++
		current := turn
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flush := func() {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		write := func(delta map[string]any, finishReason any) {
			choice := map[string]any{"index": 0, "delta": delta, "finish_reason": finishReason}
			payload, _ := json.Marshal(map[string]any{"choices": []map[string]any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flush()
		}

		if current == 1 {
			write(map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"index":    0,
					"id":       "call_real_1",
					"type":     "function",
					"function": map[string]any{"name": "image_gen", "arguments": `{"prompt":"two cats looking at me with love"}`},
				}},
			}, nil)
			write(map[string]any{}, "tool_calls")
		} else {
			write(map[string]any{"role": "assistant", "content": "Готово."}, nil)
			write(map[string]any{}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flush()
	}))
}

// TestStreamingFollowUpTurnRunsTheToolCall reproduces the real inbound Telegram
// shape that a bare harness could not: a follow-up turn inside an existing
// session, streamed (the live ack editor wires a delta callback, which is what
// selects the streaming path), with a tool registered.
//
// Before the fix the client announced the terminal chunk on the finish_reason
// delta while the tool call was still buffered. The agent loop stopped there,
// the tool never ran, and the user was told the model produced nothing — in
// about five seconds, with no tool line and no error anywhere in the journal.
func TestStreamingFollowUpTurnRunsTheToolCall(t *testing.T) {
	t.Parallel()

	server := realInboundGateway(t)
	defer server.Close()

	client, err := ai.NewClient(ai.ProviderConfig{Name: "openai", BaseURL: server.URL, Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	tool := &recordingTool{}
	registry := tools.NewRegistry()
	registry.Register(tool)

	agent := NewToolCallingAgent(client, registry, &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	})
	agent.SetModel("m")
	// The live ack editor sets this on every real inbound turn; it is what
	// routes the run through the streaming client instead of CompleteWithTools.
	agent.SetDeltaCallback(func(string) {})

	history := []ai.ChatMessage{
		{Role: ai.RoleUser, Content: "Сгенерируй котика"},
		{Role: ai.RoleAssistant, Content: "(No answer was produced for this message — the run ended before the model replied.)"},
	}

	res, err := agent.ProcessRequestWithContent(context.Background(), "and now 2 cats looking at me with love", nil, "", history)
	if err != nil {
		t.Fatalf("ProcessRequestWithContent: %v", err)
	}
	if res == nil {
		t.Fatal("nil response")
	}
	if tool.calls() != 1 {
		t.Fatalf("image_gen ran %d times, want 1 — the streamed tool call was dropped", tool.calls())
	}
	if res.IsFallback {
		t.Fatalf("run reported a fallback result: %q", res.Message)
	}
	if strings.Contains(res.Message, "empty model output") {
		t.Fatalf("run reported empty model output while the gateway had sent a tool call: %q", res.Message)
	}
	if res.Message != "Готово." {
		t.Fatalf("message = %q, want %q", res.Message, "Готово.")
	}
}
