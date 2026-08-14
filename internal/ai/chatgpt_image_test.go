package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func imageSSEStream(b64, revisedPrompt string) string {
	return fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"image_generation_call","status":"completed","result":%q,"revised_prompt":%q}}`, b64, revisedPrompt) + "\n\ndata: [DONE]\n\n"
}

func TestParseImageSSEHappyPath(t *testing.T) {
	t.Parallel()

	png := []byte("fake-png-bytes")
	stream := imageSSEStream(base64.StdEncoding.EncodeToString(png), "a fluffy cat")

	result, err := parseImageSSE(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseImageSSE: %v", err)
	}
	if string(result.PNG) != string(png) {
		t.Errorf("PNG = %q, want %q", result.PNG, png)
	}
	if result.RevisedPrompt != "a fluffy cat" {
		t.Errorf("RevisedPrompt = %q, want %q", result.RevisedPrompt, "a fluffy cat")
	}
}

func TestParseImageSSETerminalFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stream     string
		wantErrSub string
	}{
		{
			name:       "failed",
			stream:     `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"upstream failed"}}}` + "\n\ndata: [DONE]\n\n",
			wantErrSub: "response failed: server_error: upstream failed",
		},
		{
			name:       "incomplete",
			stream:     `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\ndata: [DONE]\n\n",
			wantErrSub: "response incomplete: max_output_tokens",
		},
		{
			name:       "error event with nested error object",
			stream:     `data: {"type":"error","error":{"code":"rate_limited","message":"slow down"}}` + "\n\ndata: [DONE]\n\n",
			wantErrSub: "rate_limited: slow down",
		},
		{
			name:       "error event with top-level fields",
			stream:     `data: {"type":"error","code":"server_error","message":"boom"}` + "\n\ndata: [DONE]\n\n",
			wantErrSub: "server_error: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseImageSSE(strings.NewReader(tt.stream))
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("parseImageSSE error = %v, want substring %q", err, tt.wantErrSub)
			}
		})
	}
}

// errAfterReader serves its content, then fails like a dropped connection.
type errAfterReader struct {
	r    io.Reader
	done bool
}

func (e *errAfterReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		if e.done {
			return n, fmt.Errorf("connection reset")
		}
		e.done = true
		return n, nil
	}
	return n, err
}

func TestParseImageSSEKeepsImageOnLateStreamError(t *testing.T) {
	t.Parallel()

	png := []byte("late-error-png")
	// Image event fully received, then the connection drops before [DONE].
	event := fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"image_generation_call","status":"completed","result":%q,"revised_prompt":""}}`, base64.StdEncoding.EncodeToString(png)) + "\n\n"

	result, err := parseImageSSE(&errAfterReader{r: strings.NewReader(event)})
	if err != nil {
		t.Fatalf("parseImageSSE: %v", err)
	}
	if string(result.PNG) != string(png) {
		t.Errorf("PNG = %q, want %q", result.PNG, png)
	}
}

func TestParseImageSSEKeepsImageOnLateTerminalEvent(t *testing.T) {
	t.Parallel()

	png := []byte("late-terminal-png")
	stream := fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"image_generation_call","status":"completed","result":%q,"revised_prompt":""}}`, base64.StdEncoding.EncodeToString(png)) + "\n\n" +
		`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\ndata: [DONE]\n\n"

	result, err := parseImageSSE(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseImageSSE: %v", err)
	}
	if string(result.PNG) != string(png) {
		t.Errorf("PNG = %q, want %q", result.PNG, png)
	}
}

func TestParseImageSSERecoversImageFromCompletedResponse(t *testing.T) {
	t.Parallel()

	png := []byte("completed-png")
	stream := fmt.Sprintf(`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","status":"completed"},{"type":"image_generation_call","status":"completed","result":%q,"revised_prompt":"from completed"}]}}`, base64.StdEncoding.EncodeToString(png)) + "\n\ndata: [DONE]\n\n"

	result, err := parseImageSSE(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseImageSSE: %v", err)
	}
	if string(result.PNG) != string(png) {
		t.Errorf("PNG = %q, want %q", result.PNG, png)
	}
	if result.RevisedPrompt != "from completed" {
		t.Errorf("RevisedPrompt = %q, want from completed", result.RevisedPrompt)
	}
}

func TestParseImageSSENoImage(t *testing.T) {
	t.Parallel()

	stream := `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\ndata: [DONE]\n\n"
	_, err := parseImageSSE(strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "no image in response") {
		t.Fatalf("parseImageSSE error = %v, want no-image error", err)
	}
}

func TestParseImageSSEBadBase64(t *testing.T) {
	t.Parallel()

	_, err := parseImageSSE(strings.NewReader(imageSSEStream("!!!not-base64!!!", "")))
	if err == nil || !strings.Contains(err.Error(), "failed to decode image payload") {
		t.Fatalf("parseImageSSE error = %v, want decode error", err)
	}
}

func TestParseImageSSEAcceptsPayloadBeyondChatBuffer(t *testing.T) {
	t.Parallel()

	// gpt-image-2 results arrive as one base64 blob far beyond the 1 MiB
	// buffer used by the chat SSE parser; the image parser must accept it.
	png := make([]byte, 2*1024*1024)
	for i := range png {
		png[i] = byte(i % 251)
	}
	stream := imageSSEStream(base64.StdEncoding.EncodeToString(png), "")

	result, err := parseImageSSE(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseImageSSE: %v", err)
	}
	if len(result.PNG) != len(png) {
		t.Fatalf("PNG length = %d, want %d", len(result.PNG), len(png))
	}
}

func TestGenerateImageRequiresPrompt(t *testing.T) {
	t.Parallel()

	client := NewChatGPTClient(ProviderConfig{Name: "chatgpt", APIKey: "static-test-key", Model: "gpt-test"})
	if _, err := client.GenerateImage(context.Background(), "   ", ImageGenOptions{}); err == nil || !strings.Contains(err.Error(), "image prompt is required") {
		t.Fatalf("GenerateImage error = %v, want prompt-required error", err)
	}
}

func TestGenerateImageSendsImageToolRequest(t *testing.T) {
	t.Parallel()

	png := []byte("png-payload")
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, imageSSEStream(base64.StdEncoding.EncodeToString(png), "revised"))
	}))
	t.Cleanup(server.Close)

	client := NewChatGPTClient(ProviderConfig{Name: "chatgpt", APIKey: "static-test-key", BaseURL: server.URL, Model: "gpt-test"})
	result, err := client.GenerateImage(context.Background(), "a cat", ImageGenOptions{Model: "gpt-image-2", Size: "1536x1024", Quality: "high"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if string(result.PNG) != string(png) {
		t.Errorf("PNG = %q, want %q", result.PNG, png)
	}
	if result.RevisedPrompt != "revised" {
		t.Errorf("RevisedPrompt = %q, want revised", result.RevisedPrompt)
	}

	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
		Store  bool   `json:"store"`
		Tools  []struct {
			Type    string `json:"type"`
			Model   string `json:"model"`
			Size    string `json:"size"`
			Quality string `json:"quality"`
		} `json:"tools"`
		ToolChoice struct {
			Type string `json:"type"`
		} `json:"tool_choice"`
	}
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatalf("unmarshal captured request: %v", err)
	}
	if req.Model != chatGPTImageWrapperModel {
		t.Errorf("request model = %q, want %q", req.Model, chatGPTImageWrapperModel)
	}
	if !req.Stream || req.Store {
		t.Errorf("stream/store = %v/%v, want true/false", req.Stream, req.Store)
	}
	if len(req.Tools) != 1 || req.Tools[0].Type != "image_generation" {
		t.Fatalf("tools = %+v, want one image_generation tool", req.Tools)
	}
	if req.Tools[0].Model != "gpt-image-2" || req.Tools[0].Size != "1536x1024" || req.Tools[0].Quality != "high" {
		t.Errorf("image tool = %+v, want model/size/quality passthrough", req.Tools[0])
	}
	if req.ToolChoice.Type != "image_generation" {
		t.Errorf("tool_choice.type = %q, want image_generation", req.ToolChoice.Type)
	}
}

func TestGenerateImageAppliesDefaults(t *testing.T) {
	t.Parallel()

	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, imageSSEStream(base64.StdEncoding.EncodeToString([]byte("x")), ""))
	}))
	t.Cleanup(server.Close)

	client := NewChatGPTClient(ProviderConfig{Name: "chatgpt", APIKey: "static-test-key", BaseURL: server.URL, Model: "gpt-test"})
	if _, err := client.GenerateImage(context.Background(), "a cat", ImageGenOptions{}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	var req struct {
		Tools []struct {
			Model   string `json:"model"`
			Size    string `json:"size"`
			Quality string `json:"quality"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatalf("unmarshal captured request: %v", err)
	}
	if len(req.Tools) != 1 || req.Tools[0].Model != defaultChatGPTImageModel || req.Tools[0].Size != defaultImageSize {
		t.Fatalf("tools = %+v, want defaults %s/%s", req.Tools, defaultChatGPTImageModel, defaultImageSize)
	}
	if req.Tools[0].Quality != "" {
		t.Errorf("quality = %q, want omitted", req.Tools[0].Quality)
	}
}

func TestGenerateImageReportsHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "boom")
	}))
	t.Cleanup(server.Close)

	client := NewChatGPTClient(ProviderConfig{Name: "chatgpt", APIKey: "static-test-key", BaseURL: server.URL, Model: "gpt-test"})
	_, err := client.GenerateImage(context.Background(), "a cat", ImageGenOptions{})
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("GenerateImage error = %v, want status 500 error", err)
	}
}

// imageGenStubClient implements ai.Client without image generation support.
type imageGenStubClient struct{}

func (c *imageGenStubClient) Complete(_ context.Context, _ []Message) (string, error) {
	return "", nil
}

func (c *imageGenStubClient) CompleteWithTools(_ context.Context, _ []ChatMessage, _ []ToolDefinition) (*ChatCompletionResponse, error) {
	return &ChatCompletionResponse{}, nil
}

func TestAsImageGenerator(t *testing.T) {
	t.Parallel()

	if _, ok := AsImageGenerator(nil); ok {
		t.Error("nil client must not be an image generator")
	}
	if _, ok := AsImageGenerator(&imageGenStubClient{}); ok {
		t.Error("plain client must not be an image generator")
	}

	chatGPT := NewChatGPTClient(ProviderConfig{Name: "chatgpt", APIKey: "static-test-key", Model: "gpt-test"})
	if _, ok := AsImageGenerator(chatGPT); !ok {
		t.Error("ChatGPTClient must be an image generator")
	}

	// A FailoverClient is unwrapped to its first capable backend.
	if _, ok := AsImageGenerator(NewFailoverClient("stub", &imageGenStubClient{})); ok {
		t.Error("failover client without capable backends must not be an image generator")
	}
	igc, ok := AsImageGenerator(NewFailoverClient("gpt-test", chatGPT))
	if !ok {
		t.Fatal("failover client wrapping ChatGPTClient must unwrap to an image generator")
	}
	if igc != ImageGenerationClient(chatGPT) {
		t.Error("unwrap must return the underlying capable backend, not the wrapper")
	}
}
