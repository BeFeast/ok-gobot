package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// toolCallSSEServer replays the exact shape the local gateway sends for a
// tool-calling turn, captured 2026-09-01 for a real inbound Telegram message:
// incremental tool_calls deltas, then an EMPTY delta carrying
// finish_reason "tool_calls", and only then "[DONE]".
//
// That ordering is the whole point of the fixture. The client used to announce
// the terminal chunk on the finish_reason delta while the tool call was still
// buffered, and built the marker chunk only at [DONE] — which the consumer,
// already stopped, never read. The turn then looked like a model that said
// nothing at all.
func toolCallSSEServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		write(map[string]any{
			"role": "assistant",
			"tool_calls": []map[string]any{{
				"index":    0,
				"id":       "call_real_1",
				"type":     "function",
				"function": map[string]any{"name": "image_gen", "arguments": ""},
			}},
		}, nil)
		for _, fragment := range []string{`{"`, `prompt`, `":"`, `two cats`, `"}`} {
			write(map[string]any{
				"tool_calls": []map[string]any{{
					"index":    0,
					"function": map[string]any{"arguments": fragment},
				}},
			}, nil)
		}
		write(map[string]any{}, "tool_calls")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flush()
	}))
}

func TestCompleteStreamWithToolsEmitsToolCallsOnFinishReasonChunk(t *testing.T) {
	t.Parallel()

	server := toolCallSSEServer(t)
	defer server.Close()

	c := &OpenAICompatibleClient{
		config:     ProviderConfig{Name: "openai", BaseURL: server.URL, Model: "m"},
		httpClient: &http.Client{},
	}

	const marker = "\n__TOOL_CALLS__:"
	var payload string
	// The agent loop stops reading at the first Done chunk, so anything the
	// caller needs must have arrived by then. Recording the payload only until
	// Done is what makes this test fail on the old ordering.
	done := false
	for chunk := range c.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: "user", Content: "draw two cats"}}, nil) {
		if chunk.Error != nil {
			t.Fatalf("stream returned an error: %v", chunk.Error)
		}
		if done {
			continue
		}
		if idx := strings.Index(chunk.Content, marker); idx >= 0 {
			payload = chunk.Content[idx+len(marker):]
		}
		if chunk.Done {
			done = true
		}
	}

	if payload == "" {
		t.Fatal("the stream reached Done without delivering the buffered tool calls: the agent loop stops there, so the call is lost and the turn reads to the user as empty model output")
	}

	var toolCalls []ToolCall
	if err := json.Unmarshal([]byte(payload), &toolCalls); err != nil {
		t.Fatalf("tool call payload is not valid JSON: %v (%q)", err, payload)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(toolCalls))
	}
	if toolCalls[0].Function.Name != "image_gen" {
		t.Fatalf("tool name = %q, want image_gen", toolCalls[0].Function.Name)
	}
	if got, want := toolCalls[0].Function.Arguments, `{"prompt":"two cats"}`; got != want {
		t.Fatalf("tool arguments = %q, want %q", got, want)
	}
}
