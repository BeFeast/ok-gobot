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

	"ok-gobot/internal/ai"
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
	Path          string
	RevisedPrompt string
	URL           string
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
	os.MkdirAll(tempDir, 0700)

	filename := fmt.Sprintf("img_%d.png", time.Now().UnixNano())
	filePath := filepath.Join(tempDir, filename)

	if err := os.WriteFile(filePath, imgData, 0600); err != nil {
		return nil, fmt.Errorf("failed to save image: %w", err)
	}

	return &GeneratedImage{
		Path:          filePath,
		RevisedPrompt: result.Data[0].RevisedPrompt,
		URL:           result.Data[0].URL,
	}, nil
}

// nativeImageGenerator adapts ai.ImageGenerationClient (the AI backend's
// native image capability, e.g. ChatGPT subscription OAuth) to the
// ImageGenerator interface, persisting the returned PNG to the temp dir.
type nativeImageGenerator struct {
	client  ai.ImageGenerationClient
	tempDir string
	model   string
	size    string
	quality string
}

// Generate creates an image through the native backend capability.
func (g *nativeImageGenerator) Generate(ctx context.Context, prompt string, opts ImageOptions) (*GeneratedImage, error) {
	genOpts := ai.ImageGenOptions{Model: g.model, Size: g.size, Quality: g.quality}
	if opts.Model != "" {
		genOpts.Model = opts.Model
	}
	if opts.Size != "" {
		genOpts.Size = opts.Size
	}
	if opts.Quality != "" {
		genOpts.Quality = opts.Quality
	}

	result, err := g.client.GenerateImage(ctx, prompt, genOpts)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(g.tempDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create image dir: %w", err)
	}
	filePath := filepath.Join(g.tempDir, fmt.Sprintf("img_%d.png", time.Now().UnixNano()))
	if err := os.WriteFile(filePath, result.PNG, 0600); err != nil {
		return nil, fmt.Errorf("failed to save image: %w", err)
	}

	return &GeneratedImage{Path: filePath, RevisedPrompt: result.RevisedPrompt}, nil
}

// Size/quality vocabularies advertised in the tool schema; set per backend
// at construction because the APIs reject each other's values.
const (
	nativeSizeVocab    = "Image size such as 1024x1024, 1536x1024, 1024x1536, or auto (optional)"
	nativeQualityVocab = "Rendering quality: low, medium, high, or auto (optional)"
	dallESizeVocab     = "Image size: 1024x1024, 1792x1024, or 1024x1792 (optional)"
	dallEQualityVocab  = "Rendering quality: standard or hd (optional)"
)

// ImageTool provides image generation capabilities
type ImageTool struct {
	generator    ImageGenerator
	tempDir      string
	sender       MediaSender // optional: deliver the image into a chat
	chatID       int64       // bound chat for delivery; 0 = no delivery
	sizeVocab    string      // schema description for the size parameter
	qualityVocab string      // schema description for the quality parameter
}

// NewImageTool creates a new image generation tool
func NewImageTool(apiKey, baseURL string) *ImageTool {
	tempDir := filepath.Join(os.TempDir(), "okgobot-images")
	os.MkdirAll(tempDir, 0700)

	return &ImageTool{
		generator:    NewOpenAIImageGenerator(apiKey, baseURL),
		tempDir:      tempDir,
		sizeVocab:    dallESizeVocab,
		qualityVocab: dallEQualityVocab,
	}
}

// NewNativeImageTool creates an image generation tool backed by the AI
// backend's native image capability. model/size/quality are configured
// defaults; per-call parameters override them.
func NewNativeImageTool(client ai.ImageGenerationClient, model, size, quality string) *ImageTool {
	tempDir := filepath.Join(os.TempDir(), "okgobot-images")
	os.MkdirAll(tempDir, 0700)

	return &ImageTool{
		generator: &nativeImageGenerator{
			client:  client,
			tempDir: tempDir,
			model:   model,
			size:    size,
			quality: quality,
		},
		tempDir:      tempDir,
		sizeVocab:    nativeSizeVocab,
		qualityVocab: nativeQualityVocab,
	}
}

// BindChat implements ChatScoped: the returned copy delivers generated
// images into the given chat.
func (t *ImageTool) BindChat(sender MediaSender, chatID int64) Tool {
	bound := *t
	bound.sender = sender
	bound.chatID = chatID
	return &bound
}

// OwnsTimeout reports that the tool bounds its own execution: generation
// runs 30-120 s and the AI client enforces an internal deadline, so the
// generic tool timeout must not apply.
func (t *ImageTool) OwnsTimeout() bool {
	return true
}

func (t *ImageTool) Name() string {
	return "image_gen"
}

func (t *ImageTool) Description() string {
	return "Generate an image from a text description. The image is saved locally and, when a chat is bound, delivered to the chat as a photo."
}

// GetSchema returns the JSON Schema for the tool's parameters.
func (t *ImageTool) GetSchema() map[string]interface{} {
	sizeDesc := t.sizeVocab
	if sizeDesc == "" {
		sizeDesc = nativeSizeVocab
	}
	qualityDesc := t.qualityVocab
	if qualityDesc == "" {
		qualityDesc = nativeQualityVocab
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Text description of the image to generate",
			},
			"size": map[string]interface{}{
				"type":        "string",
				"description": sizeDesc,
			},
			"quality": map[string]interface{}{
				"type":        "string",
				"description": qualityDesc,
			},
		},
		"required": []string{"prompt"},
	}
}

// ExecuteJSON accepts structured named parameters from the agent.
func (t *ImageTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	prompt := strings.TrimSpace(params["prompt"])
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	opts := ImageOptions{
		Size:    strings.TrimSpace(params["size"]),
		Quality: strings.TrimSpace(params["quality"]),
	}
	return t.run(ctx, prompt, opts)
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

	return t.run(ctx, prompt, opts)
}

// maxImageCaptionRunes keeps photo captions under Telegram's 1024-char cap
// with headroom; an over-long caption fails the whole send.
const maxImageCaptionRunes = 1000

// maxImageAge bounds the shared temp dir: generated images older than this
// are pruned before each new generation.
const maxImageAge = 24 * time.Hour

// run generates the image and, when the tool is bound to a chat, delivers it.
func (t *ImageTool) run(ctx context.Context, prompt string, opts ImageOptions) (string, error) {
	if t.generator == nil {
		return "", fmt.Errorf("image generator not configured")
	}

	pruneOldImages(t.tempDir)

	result, err := t.generator.Generate(ctx, prompt, opts)
	if err != nil {
		return "", fmt.Errorf("failed to generate image: %w", err)
	}

	response := fmt.Sprintf("🎨 Image generated!\n\nPrompt: %s\nFile: %s", prompt, result.Path)
	if result.RevisedPrompt != "" && result.RevisedPrompt != prompt {
		response += fmt.Sprintf("\n\nRevised prompt: %s", result.RevisedPrompt)
	}

	if t.sender != nil && t.chatID != 0 {
		caption := prompt
		if runes := []rune(caption); len(runes) > maxImageCaptionRunes {
			caption = string(runes[:maxImageCaptionRunes-1]) + "…"
		}
		if err := t.sender.SendPhotoToChat(t.chatID, result.Path, caption); err != nil {
			response += fmt.Sprintf("\n\n⚠️ Failed to deliver the image to the chat: %v", err)
		} else {
			response += "\n\nDelivered to the chat as a photo."
		}
	}

	return response, nil
}

// pruneOldImages best-effort removes generated images older than maxImageAge
// so the shared temp dir does not grow without bound.
func pruneOldImages(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxImageAge)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.IsDir() {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// GetImagePath returns the path to the generated image (for sending via Telegram)
func (t *ImageTool) GetImagePath() string {
	return t.tempDir
}
