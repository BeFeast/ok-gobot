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

// oversizedSSEServer answers with a single SSE line far past bufio.Scanner's
// 64 KiB default. A gateway answering a tool-enabled request really does this:
// measured at 2.6 MB against the local gateway, versus 427 bytes for the same
// request without tools. Before the buffer was raised, every such turn died
// with "token too long" while plain chat kept working — so the bot answered
// without ever calling a tool.
func oversizedSSEServer(t *testing.T, contentLen int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := map[string]any{
			"choices": []map[string]any{{
				"delta": map[string]any{"content": strings.Repeat("x", contentLen)},
			}},
		}
		payload, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		fmt.Fprint(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
}

func TestCompleteStreamHandlesLineOverScannerDefault(t *testing.T) {
	t.Parallel()

	const contentLen = 300 << 10 // ~300 KiB in one line, well past the 64 KiB default
	server := oversizedSSEServer(t, contentLen)
	defer server.Close()

	c := &OpenAICompatibleClient{
		config:     ProviderConfig{Name: "openai", BaseURL: server.URL, Model: "m"},
		httpClient: &http.Client{},
	}

	var got int
	for chunk := range c.CompleteStream(context.Background(), []Message{{Role: "user", Content: "hi"}}) {
		if chunk.Error != nil {
			t.Fatalf("stream returned an error: %v", chunk.Error)
		}
		got += len(chunk.Content)
	}
	if got != contentLen {
		t.Fatalf("received %d bytes of content, want %d", got, contentLen)
	}
}

func TestCompleteStreamWithToolsHandlesLineOverScannerDefault(t *testing.T) {
	t.Parallel()

	const contentLen = 300 << 10
	server := oversizedSSEServer(t, contentLen)
	defer server.Close()

	c := &OpenAICompatibleClient{
		config:     ProviderConfig{Name: "openai", BaseURL: server.URL, Model: "m"},
		httpClient: &http.Client{},
	}

	var got int
	for chunk := range c.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, nil) {
		if chunk.Error != nil {
			t.Fatalf("stream returned an error: %v", chunk.Error)
		}
		got += len(chunk.Content)
	}
	if got != contentLen {
		t.Fatalf("received %d bytes of content, want %d", got, contentLen)
	}
}

func TestSSELimitIsAboveObservedGatewayLines(t *testing.T) {
	t.Parallel()

	// The largest line actually measured was 2.6 MB; the cap must leave real
	// headroom above that rather than sit just past it.
	const observed = 2_614_171
	if sseMaxLineBytes <= observed*2 {
		t.Fatalf("sseMaxLineBytes = %d leaves no headroom over the observed %d", sseMaxLineBytes, observed)
	}
}
