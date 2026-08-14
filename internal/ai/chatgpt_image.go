package ai

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ok-gobot/internal/logger"
)

const (
	// chatGPTImageWrapperModel is the top-level model id for image generation
	// requests. The backend routes the actual generation to the image model in
	// the tools block; this wrapper id is the one proven to work with
	// subscription OAuth (matches the reference OpenClaw implementation).
	chatGPTImageWrapperModel = "gpt-5.5"

	defaultChatGPTImageModel = "gpt-image-2"
	defaultImageSize         = "1024x1024"

	// maxImageSSEBytes caps a single SSE line: gpt-image-2 results arrive as
	// one base64 blob inside one data: line, far beyond the 1 MiB chat buffer.
	maxImageSSEBytes = 64 * 1024 * 1024

	// chatGPTImageTimeout bounds one generation end-to-end. The SSE request
	// runs on the timeout-less stream client, so without this deadline a
	// silently stalled stream would block forever.
	chatGPTImageTimeout = 5 * time.Minute
)

// ImageGenOptions holds options for a single image generation call.
type ImageGenOptions struct {
	Model   string // image model, e.g. "gpt-image-2"
	Size    string // e.g. "1024x1024", "1536x1024"
	Quality string // optional, passed through when set
}

// GeneratedImageResult is the raw result of an image generation call.
type GeneratedImageResult struct {
	PNG           []byte
	RevisedPrompt string
}

// ImageGenerationClient is implemented by clients that can generate images.
// Capability is discovered by type assertion, mirroring visionCapable.
type ImageGenerationClient interface {
	GenerateImage(ctx context.Context, prompt string, opts ImageGenOptions) (*GeneratedImageResult, error)
}

// AsImageGenerator reports whether the client can generate images natively.
// A FailoverClient is unwrapped to its first capable backend: fallback
// entries only vary the text model, so there is nothing real to fail over
// for image generation.
func AsImageGenerator(client Client) (ImageGenerationClient, bool) {
	if client == nil {
		return nil, false
	}
	if fc, ok := client.(*FailoverClient); ok {
		for _, entry := range fc.entries {
			if igc, ok := AsImageGenerator(entry.client); ok {
				return igc, true
			}
		}
		return nil, false
	}
	igc, ok := client.(ImageGenerationClient)
	return igc, ok
}

// chatGPTImageItem is the image_generation_call output item shape.
type chatGPTImageItem struct {
	Type          string `json:"type"`
	Status        string `json:"status"`
	Result        string `json:"result"`
	RevisedPrompt string `json:"revised_prompt"`
}

// chatGPTImageRequest is the Responses API request for the image_generation
// tool. The shape differs from chatGPTRequest: the tool block carries
// model/size/quality instead of a function definition.
type chatGPTImageRequest struct {
	Model        string                 `json:"model"`
	Instructions string                 `json:"instructions"`
	Input        []chatGPTImageInput    `json:"input"`
	Tools        []chatGPTImageToolDef  `json:"tools"`
	ToolChoice   chatGPTImageToolChoice `json:"tool_choice"`
	Stream       bool                   `json:"stream"`
	Store        bool                   `json:"store"`
}

type chatGPTImageInput struct {
	Role    string                     `json:"role"`
	Content []chatGPTImageInputContent `json:"content"`
}

type chatGPTImageInputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type chatGPTImageToolDef struct {
	Type    string `json:"type"`
	Model   string `json:"model"`
	Size    string `json:"size"`
	Quality string `json:"quality,omitempty"`
}

type chatGPTImageToolChoice struct {
	Type string `json:"type"`
}

// GenerateImage generates an image through the Codex Responses API using the
// image_generation tool, authenticated with the existing ChatGPT OAuth cache.
func (c *ChatGPTClient) GenerateImage(ctx context.Context, prompt string, opts ImageGenOptions) (*GeneratedImageResult, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("image prompt is required")
	}
	if opts.Model == "" {
		opts.Model = defaultChatGPTImageModel
	}
	if opts.Size == "" {
		opts.Size = defaultImageSize
	}

	reqBody := chatGPTImageRequest{
		Model:        chatGPTImageWrapperModel,
		Instructions: "You are an image generation assistant.",
		Input: []chatGPTImageInput{
			{
				Role: "user",
				Content: []chatGPTImageInputContent{
					{Type: "input_text", Text: prompt},
				},
			},
		},
		Tools: []chatGPTImageToolDef{
			{Type: "image_generation", Model: opts.Model, Size: opts.Size, Quality: opts.Quality},
		},
		ToolChoice: chatGPTImageToolChoice{Type: "image_generation"},
		Stream:     true,
		Store:      false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal image request: %w", err)
	}

	logger.Debugf("ChatGPT GenerateImage: model=%s size=%s prompt_len=%d", opts.Model, opts.Size, len(prompt))

	ctx, cancel := context.WithTimeout(ctx, chatGPTImageTimeout)
	defer cancel()

	// Use the timeout-less stream client like the other SSE consumers:
	// generation plus the base64 image download can exceed the fixed
	// httpClient timeout; the deadline above bounds the whole call.
	resp, err := c.doRequest(ctx, jsonData, c.streamClient)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ChatGPT image API error (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return parseImageSSE(resp.Body)
}

// parseImageSSE extracts the image_generation_call result from the SSE stream.
func parseImageSSE(body io.Reader) (*GeneratedImageResult, error) {
	var result *GeneratedImageResult
	var terminalErr error

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxImageSSEBytes)

scan:
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event struct {
			Type     string          `json:"type"`
			Item     json.RawMessage `json:"item"`
			Code     string          `json:"code"`
			Message  string          `json:"message"`
			Response struct {
				Output []chatGPTImageItem `json:"output"`
			} `json:"response"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "response.output_item.done":
			var item chatGPTImageItem
			if err := json.Unmarshal(event.Item, &item); err != nil {
				continue
			}
			img, err := decodeImageItem(item)
			if err != nil {
				return nil, err
			}
			if img != nil {
				result = img
			}
		case "response.completed":
			// Authoritative fallback: recover the image from the final
			// response when the incremental item event was absent.
			if result != nil {
				continue
			}
			for _, item := range event.Response.Output {
				img, err := decodeImageItem(item)
				if err != nil {
					return nil, err
				}
				if img != nil {
					result = img
					break
				}
			}
		case "response.failed", "response.incomplete":
			terminalErr = chatGPTTerminalError([]byte(data), event.Type)
			break scan
		case "error":
			code, message := event.Error.Code, event.Error.Message
			if message == "" {
				code, message = event.Code, event.Message
			}
			if message != "" {
				terminalErr = fmt.Errorf("ChatGPT image error: %s: %s", code, message)
			} else {
				terminalErr = chatGPTTerminalError([]byte(data), event.Type)
			}
			break scan
		}
	}
	// A fully received image wins over late stream errors or terminal events:
	// the generation already succeeded and is too expensive to discard.
	if result != nil {
		return result, nil
	}
	if terminalErr != nil {
		return nil, terminalErr
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read image stream: %w", err)
	}
	return nil, fmt.Errorf("no image in response")
}

// decodeImageItem returns the decoded image when the item is a completed
// image_generation_call carrying a payload, or nil for any other item.
func decodeImageItem(item chatGPTImageItem) (*GeneratedImageResult, error) {
	if item.Type != "image_generation_call" || item.Result == "" {
		return nil, nil
	}
	png, err := base64.StdEncoding.DecodeString(item.Result)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image payload: %w", err)
	}
	return &GeneratedImageResult{PNG: png, RevisedPrompt: item.RevisedPrompt}, nil
}
