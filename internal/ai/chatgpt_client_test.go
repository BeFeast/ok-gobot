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

const chatGPTCompletedOnlyTextEvent = `data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","model":"gpt-test","output":[{"id":"msg_test","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Weather is clear."}]}]}}` + "\n\ndata: [DONE]\n\n"

const chatGPTDeltaAndCompletedTextEvent = `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Weather is clear."}` + "\n\n" +
	`data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","model":"gpt-test","output":[{"id":"msg_test","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Weather is clear."}]}]}}` + "\n\ndata: [DONE]\n\n"

const chatGPTCompletedOnlyToolCallEvent = `data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","model":"gpt-test","output":[{"id":"fc_test","type":"function_call","status":"completed","call_id":"call_test","name":"web_search","arguments":"{\"query\":\"Netanya weather\"}"}]}}` + "\n\ndata: [DONE]\n\n"

func TestChatGPTCompletedOnlyTextIsNotDropped(t *testing.T) {
	client := newChatGPTSSETestClient(t, chatGPTCompletedOnlyTextEvent)
	ctx := context.Background()

	complete, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "weather"}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if complete != "Weather is clear." {
		t.Fatalf("Complete = %q", complete)
	}

	withTools, err := client.CompleteWithTools(ctx, []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)
	if err != nil {
		t.Fatalf("CompleteWithTools: %v", err)
	}
	if got := withTools.Choices[0].Message.Content; got != "Weather is clear." {
		t.Fatalf("CompleteWithTools content = %q", got)
	}

	if got := collectChatGPTStream(t, client.CompleteStream(ctx, []Message{{Role: RoleUser, Content: "weather"}})); got != "Weather is clear." {
		t.Fatalf("CompleteStream content = %q", got)
	}
	if got := collectChatGPTStream(t, client.CompleteStreamWithTools(ctx, []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)); got != "Weather is clear." {
		t.Fatalf("CompleteStreamWithTools content = %q", got)
	}
}

func TestChatGPTCompletedTextDoesNotDuplicateStreamedDeltas(t *testing.T) {
	client := newChatGPTSSETestClient(t, chatGPTDeltaAndCompletedTextEvent)
	ctx := context.Background()

	withTools, err := client.CompleteWithTools(ctx, []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)
	if err != nil {
		t.Fatalf("CompleteWithTools: %v", err)
	}
	if got := withTools.Choices[0].Message.Content; got != "Weather is clear." {
		t.Fatalf("CompleteWithTools content = %q", got)
	}

	if got := collectChatGPTStream(t, client.CompleteStreamWithTools(ctx, []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)); got != "Weather is clear." {
		t.Fatalf("CompleteStreamWithTools content = %q", got)
	}
}

func TestChatGPTCompletedOnlyToolCallIsNotDropped(t *testing.T) {
	client := newChatGPTSSETestClient(t, chatGPTCompletedOnlyToolCallEvent)
	ctx := context.Background()

	withTools, err := client.CompleteWithTools(ctx, []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil)
	if err != nil {
		t.Fatalf("CompleteWithTools: %v", err)
	}
	assertChatGPTToolCall(t, withTools.Choices[0].Message.ToolCalls)

	streamed := collectChatGPTStream(t, client.CompleteStreamWithTools(ctx, []ChatMessage{{Role: RoleUser, Content: "weather"}}, nil))
	const marker = "\n__TOOL_CALLS__:"
	if !strings.HasPrefix(streamed, marker) {
		t.Fatalf("streamed tool call = %q", streamed)
	}
	var toolCalls []ToolCall
	if err := json.Unmarshal([]byte(strings.TrimPrefix(streamed, marker)), &toolCalls); err != nil {
		t.Fatalf("decode streamed tool calls: %v", err)
	}
	assertChatGPTToolCall(t, toolCalls)
}

func newChatGPTSSETestClient(t *testing.T, stream string) *ChatGPTClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, stream)
	}))
	t.Cleanup(server.Close)
	return NewChatGPTClient(ProviderConfig{
		Name:    "chatgpt",
		APIKey:  "static-test-key",
		BaseURL: server.URL,
		Model:   "gpt-test",
	})
}

func collectChatGPTStream(t *testing.T, chunks <-chan StreamChunk) string {
	t.Helper()
	var content strings.Builder
	done := false
	for chunk := range chunks {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		content.WriteString(chunk.Content)
		if chunk.Done {
			done = true
		}
	}
	if !done {
		t.Fatal("stream ended without a done chunk")
	}
	return content.String()
}

func assertChatGPTToolCall(t *testing.T, toolCalls []ToolCall) {
	t.Helper()
	if len(toolCalls) != 1 {
		t.Fatalf("tool calls = %+v", toolCalls)
	}
	toolCall := toolCalls[0]
	if toolCall.ID != "call_test" || toolCall.Function.Name != "web_search" || toolCall.Function.Arguments != `{"query":"Netanya weather"}` {
		t.Fatalf("tool call = %+v", toolCall)
	}
}
