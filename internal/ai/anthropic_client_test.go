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

func TestAnthropicClientCompleteStreamTextDeltas(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Fatalf("unexpected x-api-key header: %q", got)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := NewAnthropicClient(ProviderConfig{
		Name:    "anthropic",
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Model:   "claude-sonnet-4-5-20250929",
	})

	ch := client.CompleteStream(context.Background(), []Message{{Role: "user", Content: "hello"}})
	var got strings.Builder
	done := false
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Error)
		}
		got.WriteString(chunk.Content)
		if chunk.Done {
			done = true
		}
	}

	if got.String() != "Hello world" {
		t.Fatalf("unexpected stream text: %q", got.String())
	}
	if !done {
		t.Fatal("expected done chunk")
	}
}

func TestAnthropicClientCompleteStreamWithToolsMarker(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req AnthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if !req.Stream {
			t.Fatal("expected stream=true request")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Working...\"}}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"search\",\"input\":{}}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"query\\\":\\\"tes\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"t\\\"}\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := NewAnthropicClient(ProviderConfig{
		Name:    "anthropic",
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Model:   "claude-sonnet-4-5-20250929",
	})

	ch := client.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: RoleUser, Content: "find x"}}, nil)
	var marker string
	var text strings.Builder
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Error)
		}
		if strings.HasPrefix(chunk.Content, "\n__TOOL_CALLS__:") {
			marker = chunk.Content
		} else {
			text.WriteString(chunk.Content)
		}
	}

	if text.String() != "Working..." {
		t.Fatalf("unexpected streamed text: %q", text.String())
	}
	if marker == "" {
		t.Fatal("expected tool-calls marker chunk")
	}

	payload := strings.TrimPrefix(marker, "\n__TOOL_CALLS__:")
	var calls []ToolCall
	if err := json.Unmarshal([]byte(payload), &calls); err != nil {
		t.Fatalf("failed to parse tool-calls marker: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(calls))
	}
	if calls[0].Function.Name != "search" {
		t.Fatalf("unexpected tool name: %s", calls[0].Function.Name)
	}
	if calls[0].Function.Arguments != `{"query":"test"}` {
		t.Fatalf("unexpected tool args: %s", calls[0].Function.Arguments)
	}
}

func TestAnthropicClientOAuthHeadersAndBetaQuery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("beta"); got != "true" {
			t.Fatalf("expected beta=true query, got %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer oauth-access" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		if !strings.Contains(r.Header.Get("anthropic-beta"), "oauth-2025-04-20") {
			t.Fatalf("expected oauth anthropic-beta header, got %q", r.Header.Get("anthropic-beta"))
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-4-5-20250929","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	client := NewAnthropicClient(ProviderConfig{
		Name:    "anthropic",
		APIKey:  "oauth:oauth-access",
		BaseURL: srv.URL,
		Model:   "claude-sonnet-4-5-20250929",
	})

	resp, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("unexpected response: %q", resp)
	}
}

func TestTranslateMessages_UserContentBlocks(t *testing.T) {
	t.Parallel()

	msgs := []ChatMessage{
		{Role: RoleSystem, Content: "sys"},
		{
			Role:    RoleUser,
			Content: "fallback text",
			ContentBlocks: []ContentBlock{
				{
					Type: "image",
					Source: &ContentSource{
						Type:      "base64",
						MediaType: "image/jpeg",
						Data:      "aGVsbG8=",
					},
				},
				{Type: "text", Text: "caption text"},
			},
		},
	}

	system, translated := translateMessages(msgs)
	if system != "sys" {
		t.Fatalf("unexpected system message: %q", system)
	}
	if len(translated) != 1 {
		t.Fatalf("expected 1 translated message, got %d", len(translated))
	}

	userMsg := translated[0]
	blocks, ok := userMsg.Content.([]ContentBlock)
	if !ok {
		t.Fatalf("expected []ContentBlock content, got %T", userMsg.Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(blocks))
	}
	if blocks[0].Type != "image" || blocks[0].Source == nil {
		t.Fatalf("expected first block to be image with source, got %+v", blocks[0])
	}
	if blocks[1].Type != "text" || blocks[1].Text != "caption text" {
		t.Fatalf("unexpected text block: %+v", blocks[1])
	}
}
