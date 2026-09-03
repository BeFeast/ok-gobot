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

// parallelToolCallSSEServer replays a turn where the model issues two tool
// calls at once. The gateway streams their argument fragments interleaved,
// each tagged with its index, and ends with an empty finish_reason chunk.
func parallelToolCallSSEServer(t *testing.T) *httptest.Server {
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
		call := func(index int, id, name string) map[string]any {
			return map[string]any{"index": index, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": ""}}
		}
		args := func(index int, fragment string) map[string]any {
			return map[string]any{"index": index, "function": map[string]any{"arguments": fragment}}
		}

		write(map[string]any{"role": "assistant", "tool_calls": []map[string]any{call(0, "call_a", "web_fetch")}}, nil)
		write(map[string]any{"tool_calls": []map[string]any{args(0, `{"url":"https://a.example`)}}, nil)
		write(map[string]any{"tool_calls": []map[string]any{call(1, "call_b", "web_fetch")}}, nil)
		write(map[string]any{"tool_calls": []map[string]any{args(1, `{"url":"https://b.exa`)}}, nil)
		write(map[string]any{"tool_calls": []map[string]any{args(0, `/one"}`)}}, nil)
		write(map[string]any{"tool_calls": []map[string]any{args(1, `mple/two"}`)}}, nil)
		write(map[string]any{}, "tool_calls")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flush()
	}))
}

// Two parallel tool calls used to collapse into one: every fragment landed on
// index 0, so the second call's JSON was appended to the first one's and the
// tool failed with `invalid character '{' after top-level value` (seen dozens
// of times in production on 2026-09-02).
func TestCompleteStreamWithToolsKeepsParallelToolCallsApart(t *testing.T) {
	t.Parallel()

	server := parallelToolCallSSEServer(t)
	defer server.Close()

	c := &OpenAICompatibleClient{
		config:     ProviderConfig{Name: "openai", BaseURL: server.URL, Model: "m"},
		httpClient: &http.Client{},
	}

	const marker = "\n__TOOL_CALLS__:"
	var payload string
	for chunk := range c.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: "user", Content: "compare a and b"}}, nil) {
		if chunk.Error != nil {
			t.Fatalf("stream returned an error: %v", chunk.Error)
		}
		if idx := strings.Index(chunk.Content, marker); idx >= 0 && payload == "" {
			payload = chunk.Content[idx+len(marker):]
		}
	}
	if payload == "" {
		t.Fatal("no tool calls delivered")
	}

	var toolCalls []ToolCall
	if err := json.Unmarshal([]byte(payload), &toolCalls); err != nil {
		t.Fatalf("tool call payload is not valid JSON: %v (%q)", err, payload)
	}
	if len(toolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2: %q", len(toolCalls), payload)
	}
	want := map[string]string{
		"call_a": `{"url":"https://a.example/one"}`,
		"call_b": `{"url":"https://b.example/two"}`,
	}
	for _, tc := range toolCalls {
		if tc.Function.Name != "web_fetch" {
			t.Errorf("call %s: name = %q, want web_fetch", tc.ID, tc.Function.Name)
		}
		if got := tc.Function.Arguments; got != want[tc.ID] {
			t.Errorf("call %s: arguments = %q, want %q", tc.ID, got, want[tc.ID])
		}
		if !json.Valid([]byte(tc.Function.Arguments)) {
			t.Errorf("call %s: arguments are not valid JSON: %q", tc.ID, tc.Function.Arguments)
		}
	}
}
