package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ImageGenerator interface for image generation providers
type ImageGenerator interface {
	Generate(ctx context.Context, prompt string, opts ImageOptions) (*GeneratedImage, error)
}

// ImageOptions holds options for image generation
type ImageOptions struct {
	Size    string // "1024x1024", "1792x1024", "1024x1792"
	Quality string // "standard", "hd"
	Style   string // "vivid", "natural"
	Model   string // "dall-e-3", etc.
}

// GeneratedImage holds the result of image generation
type GeneratedImage struct {
	Path         string
	RevisedPrompt string
	URL          string
}

// OpenAIImageGenerator generates images using OpenAI's DALL-E API
type OpenAIImageGenerator struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewOpenAIImageGenerator creates a new OpenAI image generator
func NewOpenAIImageGenerator(apiKey, baseURL string) *OpenAIImageGenerator {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIImageGenerator{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

// Generate creates an image from a prompt
func (g *OpenAIImageGenerator) Generate(ctx context.Context, prompt string, opts ImageOptions) (*GeneratedImage, error) {
	if opts.Size == "" {
		opts.Size = "1024x1024"
	}
	if opts.Quality == "" {
		opts.Quality = "standard"
	}
	if opts.Model == "" {
		opts.Model = "dall-e-3"
	}

	reqBody := map[string]interface{}{
		"model":           opts.Model,
		"prompt":          prompt,
		"n":               1,
		"size":            opts.Size,
		"quality":         opts.Quality,
		"response_format": "b64_json",
	}

	if opts.Style != "" {
		reqBody["style"] = opts.Style
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", g.baseURL+"/images/generations", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.apiKey)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no image generated")
	}

	// Decode and save the image
	imgData, err := base64.StdEncoding.DecodeString(result.Data[0].B64JSON)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	// Save to temp file
	tempDir := filepath.Join(os.TempDir(), "okgobot-images")
	os.MkdirAll(tempDir, 0755)
	
	filename := fmt.Sprintf("img_%d.png", time.Now().UnixNano())
	filePath := filepath.Join(tempDir, filename)

	if err := os.WriteFile(filePath, imgData, 0644); err != nil {
		return nil, fmt.Errorf("failed to save image: %w", err)
	}

	return &GeneratedImage{
		Path:          filePath,
		RevisedPrompt: result.Data[0].RevisedPrompt,
		URL:           result.Data[0].URL,
	}, nil
}

// ImageTool provides image generation capabilities
type ImageTool struct {
	generator ImageGenerator
	tempDir   string
}

// NewImageTool creates a new image generation tool
func NewImageTool(apiKey, baseURL string) *ImageTool {
	tempDir := filepath.Join(os.TempDir(), "okgobot-images")
	os.MkdirAll(tempDir, 0755)

	return &ImageTool{
		generator: NewOpenAIImageGenerator(apiKey, baseURL),
		tempDir:   tempDir,
	}
}

func (t *ImageTool) Name() string {
	return "image_gen"
}

func (t *ImageTool) Description() string {
	return "Generate images from text descriptions using DALL-E"
}

func (t *ImageTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: image_gen <prompt> [--size 1024x1024] [--quality standard|hd] [--style vivid|natural]")
	}

	// Parse arguments
	opts := ImageOptions{}
	var promptParts []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--size":
			if i+1 < len(args) {
				opts.Size = args[i+1]
				i++
			}
		case "--quality":
			if i+1 < len(args) {
				opts.Quality = args[i+1]
				i++
			}
		case "--style":
			if i+1 < len(args) {
				opts.Style = args[i+1]
				i++
			}
		default:
			promptParts = append(promptParts, args[i])
		}
	}

	prompt := strings.Join(promptParts, " ")
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}

	if t.generator == nil {
		return "", fmt.Errorf("image generator not configured")
	}

	result, err := t.generator.Generate(ctx, prompt, opts)
	if err != nil {
		return "", fmt.Errorf("failed to generate image: %w", err)
	}

	response := fmt.Sprintf("🎨 Image generated!\n\nPrompt: %s\nFile: %s", prompt, result.Path)
	if result.RevisedPrompt != "" && result.RevisedPrompt != prompt {
		response += fmt.Sprintf("\n\nRevised prompt: %s", result.RevisedPrompt)
	}

	return response, nil
}

// GetImagePath returns the path to the generated image (for sending via Telegram)
func (t *ImageTool) GetImagePath() string {
	return t.tempDir
}
