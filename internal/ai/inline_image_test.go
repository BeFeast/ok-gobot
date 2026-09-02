package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInlineImageDecode(t *testing.T) {
	t.Parallel()

	want := []byte("\x89PNG\r\n\x1a\npayload")
	var img InlineImage
	img.ImageURL.URL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(want)

	mediaType, raw, err := img.Decode()
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if mediaType != "image/png" || string(raw) != string(want) {
		t.Fatalf("mediaType=%q bytes=%q", mediaType, raw)
	}
}

// A model must never be able to make the bot fetch a URL of its choosing, so
// anything that is not a base64 data URL is reported rather than followed.
func TestInlineImageRejectsNonDataURLs(t *testing.T) {
	t.Parallel()

	for _, url := range []string{
		"https://example.com/cat.png",
		"file:///etc/passwd",
		"data:text/plain;base64,aGk=",
		"data:image/png;base64,!!!not-base64!!!",
		"data:image/png;base64,",
		"",
	} {
		var img InlineImage
		img.ImageURL.URL = url
		if _, _, err := img.Decode(); err == nil {
			t.Errorf("Decode(%q) succeeded, want an error", url)
		}
	}
}

// This is the shape the gateway actually sends: no content, no tool calls, an
// images array on the assistant message. Parsing it is the difference between
// delivering a picture and reporting empty model output.
func TestAssistantMessageCarriesInlineImages(t *testing.T) {
	t.Parallel()

	body := `{"choices":[{"index":0,"message":{"role":"assistant","images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="},"index":0}]},"finish_reason":"stop"}]}`
	var resp ChatCompletionResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg := resp.Choices[0].Message
	if msg.Content != "" {
		t.Fatalf("expected no text content, got %q", msg.Content)
	}
	if len(msg.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(msg.Images))
	}
	if _, raw, err := msg.Images[0].Decode(); err != nil || string(raw) != "hello" {
		t.Fatalf("decoded = %q err = %v", raw, err)
	}
}

func TestStreamingDeltaCarriesInlineImages(t *testing.T) {
	t.Parallel()

	body := `{"choices":[{"index":0,"delta":{"role":"assistant","images":[{"image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}}]}`
	var chunk StreamChunkResponse
	if err := json.Unmarshal([]byte(body), &chunk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(chunk.Choices[0].Delta.Images) != 1 {
		t.Fatal("streaming delta dropped the images array")
	}
}

// Inbound-only: an assistant message we send back must not carry an images key.
func TestChatMessageOmitsImagesWhenSending(t *testing.T) {
	t.Parallel()

	out, err := json.Marshal(ChatMessage{Role: "user", Content: "hi"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "images") {
		t.Fatalf("outbound message carries an images key: %s", out)
	}
}

// The Telegram path streams, so an image that only survives the non-streaming
// parser is still lost. This pins the whole streaming route: an SSE delta that
// carries an image and no content must reach the caller as a chunk.
func TestCompleteStreamWithToolsEmitsInlineImages(t *testing.T) {
	t.Parallel()

	const png = "data:image/png;base64,aGVsbG8="
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"images\":[{\"image_url\":{\"url\":\"%s\"}}]}}]}\n\n", png)
		fmt.Fprint(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer server.Close()

	c := &OpenAICompatibleClient{
		config:     ProviderConfig{Name: "openai", BaseURL: server.URL, Model: "m"},
		httpClient: &http.Client{},
	}

	var got []InlineImage
	var text string
	for chunk := range c.CompleteStreamWithTools(context.Background(), []ChatMessage{{Role: "user", Content: "draw"}}, nil) {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		got = append(got, chunk.Images...)
		text += chunk.Content
	}
	if len(got) != 1 {
		t.Fatalf("received %d images, want 1 — an image-only delta was dropped", len(got))
	}
	if _, raw, err := got[0].Decode(); err != nil || string(raw) != "hello" {
		t.Fatalf("decoded %q err %v", raw, err)
	}
	if text != "" {
		t.Fatalf("unexpected text content %q", text)
	}
}
