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

func TestOpenAICompatibleClientStreamWithToolsWaitsForDoneMarker(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"memory_search\",\"arguments\":\"\"},\"extra_content\":{\"google\":{\"thought_signature\":\"sig123\"}}}]},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"query\\\":\\\"video\"}}]},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\" summary\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	baseClient, err := NewClient(ProviderConfig{
		Name:    "custom",
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Model:   "models/gemini-3.1-flash-lite-preview",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	client, ok := baseClient.(StreamingClient)
	if !ok {
		t.Fatalf("expected StreamingClient, got %T", baseClient)
	}

	ch := client.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "remember?"}}, nil)

	var marker string
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Error)
		}
		if strings.HasPrefix(chunk.Content, "\n__TOOL_CALLS__:") {
			marker = chunk.Content
		}
	}

	if marker == "" {
		t.Fatal("expected tool-call marker after [DONE]")
	}

	var payload StreamingToolCallPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(marker, "\n__TOOL_CALLS__:")), &payload); err != nil {
		t.Fatalf("failed to parse marker payload: %v", err)
	}
	if len(payload.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(payload.ToolCalls))
	}
	if payload.ToolCalls[0].Function.Name != "memory_search" {
		t.Fatalf("unexpected tool name: %q", payload.ToolCalls[0].Function.Name)
	}
	if payload.ToolCalls[0].Function.Arguments != `{"query":"video summary"}` {
		t.Fatalf("unexpected tool arguments: %q", payload.ToolCalls[0].Function.Arguments)
	}
	if payload.ToolCalls[0].ExtraContent == nil || payload.ToolCalls[0].ExtraContent.Google == nil {
		t.Fatalf("expected Gemini extra_content on tool call in marker: %+v", payload.ToolCalls[0].ExtraContent)
	}
	if payload.ToolCalls[0].ExtraContent.Google.ThoughtSignature != "sig123" {
		t.Fatalf("unexpected thought signature: %q", payload.ToolCalls[0].ExtraContent.Google.ThoughtSignature)
	}
}
