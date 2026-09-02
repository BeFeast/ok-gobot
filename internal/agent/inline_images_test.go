package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/tools"
)

// inlineImageClient replays a captured response body through the real parser.
// Feeding raw JSON rather than a hand-built struct is the point: the bug this
// guards was a missing struct tag, which any Go-level fixture would hide.
type inlineImageClient struct {
	body   string
	chunks []ai.StreamChunk
}

func (c *inlineImageClient) Complete(context.Context, []ai.Message) (string, error) {
	return "", nil
}

func (c *inlineImageClient) SupportsVision() bool { return false }

func (c *inlineImageClient) CompleteWithTools(context.Context, []ai.ChatMessage, []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	var resp ai.ChatCompletionResponse
	if err := json.Unmarshal([]byte(c.body), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *inlineImageClient) CompleteStream(context.Context, []ai.Message) <-chan ai.StreamChunk {
	ch := make(chan ai.StreamChunk)
	close(ch)
	return ch
}

func (c *inlineImageClient) CompleteStreamWithTools(context.Context, []ai.ChatMessage, []ai.ToolDefinition) <-chan ai.StreamChunk {
	ch := make(chan ai.StreamChunk, len(c.chunks)+1)
	for _, chunk := range c.chunks {
		ch <- chunk
	}
	ch <- ai.StreamChunk{Done: true, FinishReason: "stop"}
	close(ch)
	return ch
}

func inlineImageAgent(client ai.Client) *ToolCallingAgent {
	return NewToolCallingAgent(client, tools.NewRegistry(), &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	})
}

// tinyPNG stands in for the megabyte-scale data URL the gateway really sends.
const tinyPNG = "data:image/png;base64,aGVsbG8="

// The gateway answers an image request inside the assistant message: no tool
// call, an images array beside (or instead of) the text. Every one of these
// bodies is the shape captured on the wire, reduced to structure. The image has
// to survive all the way into AgentResponse — anything less and the bot bills
// for a picture it throws away.
func TestInlineImagesReachAgentResponse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		body       string
		wantImages int
		wantText   string
	}{
		{
			// Verbatim structure of the captured 2.7 MB response: object
			// "chat.completion", text alongside the image, tool_calls null.
			name: "captured non-streaming response",
			body: `{"id":"resp_0788c4cc","object":"chat.completion","created":1788279840,"model":"gpt-5.6-sol",` +
				`"choices":[{"index":0,"message":{"role":"assistant","content":"## Готово",` +
				`"reasoning_content":"**Initiating image generation**","tool_calls":null,` +
				`"images":[{"type":"image_url","image_url":{"url":"` + tinyPNG + `"}}]},"finish_reason":"stop"}]}`,
			wantImages: 1,
			wantText:   "## Готово",
		},
		{
			// The reported symptom: a picture and not one character of text.
			name: "image with no text at all",
			body: `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"",` +
				`"tool_calls":null,"images":[{"type":"image_url","image_url":{"url":"` + tinyPNG + `"}}]},` +
				`"finish_reason":"stop"}]}`,
			wantImages: 1,
			wantText:   "",
		},
		{
			name: "several images in one message",
			body: `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"two",` +
				`"images":[{"type":"image_url","image_url":{"url":"` + tinyPNG + `"},"index":0},` +
				`{"type":"image_url","image_url":{"url":"` + tinyPNG + `"},"index":1}]},"finish_reason":"stop"}]}`,
			wantImages: 2,
			wantText:   "two",
		},
		{
			name: "ordinary text answer carries no images",
			body: `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant",` +
				`"content":"just words"},"finish_reason":"stop"}]}`,
			wantImages: 0,
			wantText:   "just words",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp, err := inlineImageAgent(&inlineImageClient{body: tc.body}).
				ProcessRequest(context.Background(), "draw a cat", "")
			if err != nil {
				t.Fatalf("ProcessRequest: %v", err)
			}
			if len(resp.Images) != tc.wantImages {
				t.Fatalf("Images = %d, want %d — the wire delivered them and the run dropped them",
					len(resp.Images), tc.wantImages)
			}
			if resp.Message != tc.wantText {
				t.Fatalf("Message = %q, want %q", resp.Message, tc.wantText)
			}
			for i, img := range resp.Images {
				mediaType, raw, err := img.Decode()
				if err != nil {
					t.Fatalf("image %d: Decode: %v", i, err)
				}
				if mediaType != "image/png" || string(raw) != "hello" {
					t.Fatalf("image %d: mediaType=%q bytes=%q", i, mediaType, raw)
				}
			}
		})
	}
}

// Telegram turns wire a delta callback, which routes the run through the
// streaming client instead. An image that only survives the non-streaming
// parser is still lost on the path real users take.
func TestInlineImagesReachAgentResponseWhenStreaming(t *testing.T) {
	t.Parallel()

	var img ai.InlineImage
	img.Type = "image_url"
	img.ImageURL.URL = tinyPNG

	agent := inlineImageAgent(&inlineImageClient{
		chunks: []ai.StreamChunk{{Images: []ai.InlineImage{img}}},
	})
	agent.SetDeltaCallback(func(string) {})

	resp, err := agent.ProcessRequest(context.Background(), "draw a cat", "")
	if err != nil {
		t.Fatalf("ProcessRequest: %v", err)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("Images = %d, want 1 — the streaming path dropped an image-only reply", len(resp.Images))
	}
	if _, raw, err := resp.Images[0].Decode(); err != nil || string(raw) != "hello" {
		t.Fatalf("decoded = %q err = %v", raw, err)
	}
}

// The production chain is longer than the two tests above exercise: the SSE
// bytes go through the real HTTP client and then through FailoverClient, which
// is always in the path because the deployment configures fallback models.
// Both fixture-based tests passed while Telegram still reported empty output,
// because the wrapper — not the parser — was eating the picture: an
// image-carrying delta has no content and is not terminal, and the wrapper
// dropped exactly that shape to avoid leaking state from an attempt that might
// still fail over.
//
// The SSE payload below is the structure captured from the live gateway on
// 2026-09-01 (delta.images[].image_url.url as a data URL, no content beside it,
// finish_reason arriving in a later chunk), with the 2.7 MB base64 replaced by
// a few bytes.
func TestInlineImagesSurviveTheFullStreamingChain(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"resp_008b31862232e284","object":"chat.completion.chunk","created":1788280565,`+
			`"model":"gpt-5.6-sol","choices":[{"index":0,"delta":{"images":[{"type":"image_url",`+
			`"image_url":{"url":"`+tinyPNG+`"},"index":0}],"role":"assistant"},`+
			`"finish_reason":null,"native_finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"resp_008b31862232e284","object":"chat.completion.chunk","created":1788280565,`+
			`"model":"gpt-5.6-sol","choices":[{"index":0,"delta":{},"finish_reason":"stop",`+
			`"native_finish_reason":"stop"}],"usage":{"completion_tokens":103,"total_tokens":2476,`+
			`"prompt_tokens":2373}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer server.Close()

	// Mirrors the deployed configuration: an OpenAI-compatible gateway with
	// fallback models, hence a FailoverClient in front of the real client.
	client, err := ai.NewClientWithFailover(ai.ProviderConfig{
		Name:    "openai",
		APIKey:  "test",
		BaseURL: server.URL,
		Model:   "gpt-5.6-sol",
	}, []string{"gpt-5.6-luna"})
	if err != nil {
		t.Fatalf("NewClientWithFailover: %v", err)
	}

	agent := inlineImageAgent(client)
	agent.SetDeltaCallback(func(string) {})

	resp, err := agent.ProcessRequest(context.Background(), "нарисуй котика", "")
	if err != nil {
		t.Fatalf("ProcessRequest: %v", err)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("Images = %d, want 1 — an image-only delta was dropped between the wire and AgentResponse", len(resp.Images))
	}
	mediaType, raw, err := resp.Images[0].Decode()
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if mediaType != "image/png" || string(raw) != "hello" {
		t.Fatalf("mediaType=%q bytes=%q", mediaType, raw)
	}
}
