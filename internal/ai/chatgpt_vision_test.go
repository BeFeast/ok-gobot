package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func multimodalUserMessage(text, mediaType, data string) ChatMessage {
	return ChatMessage{
		Role:    RoleUser,
		Content: text,
		ContentBlocks: []ContentBlock{
			{Type: "image", Source: &ContentSource{Type: "base64", MediaType: mediaType, Data: data}},
		},
	}
}

func TestConvertChatMessagesSerializesImageBlocks(t *testing.T) {
	t.Parallel()

	client := NewChatGPTClient(ProviderConfig{Name: "chatgpt", APIKey: "static-test-key", Model: "gpt-test"})
	_, input := client.convertChatMessages([]ChatMessage{
		{Role: RoleSystem, Content: "sys"},
		multimodalUserMessage("what is that?", "image/png", "cGl4ZWxz"),
	})

	if len(input) != 1 {
		t.Fatalf("input items = %d, want 1", len(input))
	}
	parts, ok := input[0]["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("content = %T, want multimodal parts", input[0]["content"])
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want text + image", len(parts))
	}
	if parts[0]["type"] != "input_text" || parts[0]["text"] != "what is that?" {
		t.Fatalf("text part = %+v", parts[0])
	}
	if parts[1]["type"] != "input_image" || parts[1]["image_url"] != "data:image/png;base64,cGl4ZWxz" {
		t.Fatalf("image part = %+v", parts[1])
	}
}

func TestConvertChatMessagesPlainTextUnchanged(t *testing.T) {
	t.Parallel()

	client := NewChatGPTClient(ProviderConfig{Name: "chatgpt", APIKey: "static-test-key", Model: "gpt-test"})
	_, input := client.convertChatMessages([]ChatMessage{{Role: RoleUser, Content: "hi"}})

	if len(input) != 1 || input[0]["content"] != "hi" {
		t.Fatalf("plain message serialization changed: %+v", input)
	}
}

func TestCompleteWithToolsRequestCarriesImagePart(t *testing.T) {
	t.Parallel()

	// Reuse the SSE test client; capture the outgoing request body.
	var captured []byte
	client, grab := newChatGPTCapturingClient(t, chatGPTCompletedOnlyTextEvent)
	_, err := client.CompleteWithTools(context.Background(), []ChatMessage{
		multimodalUserMessage("describe", "image/jpeg", "aW1n"),
	}, nil)
	if err != nil {
		t.Fatalf("CompleteWithTools: %v", err)
	}
	captured = grab()

	var req struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if len(req.Input) == 0 {
		t.Fatal("request has no input items")
	}
	if !strings.Contains(string(captured), `"input_image"`) || !strings.Contains(string(captured), "data:image/jpeg;base64,aW1n") {
		t.Fatalf("request body does not carry the image part: %s", captured)
	}
}

// newChatGPTCapturingClient serves the given SSE stream and captures the
// outgoing request body for assertions.
func newChatGPTCapturingClient(t *testing.T, stream string) (*ChatGPTClient, func() []byte) {
	t.Helper()
	var mu sync.Mutex
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = body
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, stream)
	}))
	t.Cleanup(server.Close)
	client := NewChatGPTClient(ProviderConfig{Name: "chatgpt", APIKey: "static-test-key", BaseURL: server.URL, Model: "gpt-test"})
	return client, func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return captured
	}
}

func TestChatGPTContentPartsDedupesCaption(t *testing.T) {
	t.Parallel()

	msg := ChatMessage{
		Role:    RoleUser,
		Content: "[Photo attached: 100x100, 5 bytes] look",
		ContentBlocks: []ContentBlock{
			{Type: "text", Text: "look"},
			{Type: "image", Source: &ContentSource{Type: "base64", MediaType: "image/jpeg", Data: "aW1n"}},
		},
	}
	parts := chatGPTContentParts(msg)
	if len(parts) != 2 {
		t.Fatalf("parts = %d (%+v), want caption block + image only", len(parts), parts)
	}
	if parts[0]["text"] != "look" {
		t.Fatalf("text part = %+v, want the caption block, not the duplicated Content", parts[0])
	}
}
