package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openAIImageMaxBytes caps the decoded response for one generation. A 1024x1024
// PNG from gpt-image-2 is around 1 MB; 32 MB leaves room for larger sizes and
// still refuses to buffer an unbounded body into memory.
const openAIImageMaxBytes = 32 << 20

type openAIImageRequest struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	Size    string `json:"size,omitempty"`
	Quality string `json:"quality,omitempty"`
}

type openAIImageResponse struct {
	Data []struct {
		B64JSON       string `json:"b64_json"`
		RevisedPrompt string `json:"revised_prompt"`
	} `json:"data"`
}

// GenerateImage implements ImageGenerationClient for OpenAI-compatible
// endpoints, which includes the local CLIProxyAPI gateway.
//
// Image generation previously existed only on the ChatGPT client, which talks
// to chatgpt.com directly under its own OAuth cache. That made image_gen the
// one capability that could not follow the rest of the fleet behind the proxy:
// switching the provider to an OpenAI-compatible endpoint silently dropped the
// tool, because AsImageGenerator matches by capability and nothing else
// implemented it.
//
// The wire format is the standard images API — POST {BaseURL}/images/generations
// returning base64 in data[0].b64_json. response_format is deliberately not
// sent: gpt-image models return base64 regardless, and OpenAI itself rejects
// the parameter for them, so sending it would break the direct endpoint while
// buying nothing on the proxy.
func (c *OpenAICompatibleClient) GenerateImage(ctx context.Context, prompt string, opts ImageGenOptions) (*GeneratedImageResult, error) {
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

	payload, err := json.Marshal(openAIImageRequest{
		Model:   opts.Model,
		Prompt:  prompt,
		Size:    opts.Size,
		Quality: opts.Quality,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal image request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, chatGPTImageTimeout)
	defer cancel()

	endpoint := strings.TrimRight(c.config.BaseURL, "/") + "/images/generations"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(c.config.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	// The shared httpClient carries a fixed timeout that a full generation can
	// exceed; the context deadline above is what bounds this call.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("image generation request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("image API error (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded openAIImageResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, openAIImageMaxBytes)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("failed to decode image response: %w", err)
	}
	if len(decoded.Data) == 0 || decoded.Data[0].B64JSON == "" {
		return nil, fmt.Errorf("image API returned no image data")
	}

	png, err := base64.StdEncoding.DecodeString(decoded.Data[0].B64JSON)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 image: %w", err)
	}

	return &GeneratedImageResult{
		PNG:           png,
		RevisedPrompt: decoded.Data[0].RevisedPrompt,
	}, nil
}
