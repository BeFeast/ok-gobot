package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// TTSProvider is the interface for TTS providers
type TTSProvider interface {
	Synthesize(ctx context.Context, text, voice string) (string, error)
	IsAvailable() bool
	AvailableVoices() []string
	Name() string
}

// TTSTool provides text-to-speech capabilities with multiple providers
type TTSTool struct {
	providers       map[string]TTSProvider
	defaultProvider string
	defaultVoice    string
}

// NewTTSTool creates a new TTS tool with configured providers
func NewTTSTool(apiKey, baseURL, defaultProvider, defaultVoice string) *TTSTool {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	if defaultProvider == "" {
		defaultProvider = "openai"
	}

	providers := make(map[string]TTSProvider)

	// Initialize OpenAI provider
	providers["openai"] = NewOpenAITTSProvider(apiKey, baseURL)

	// Initialize Edge TTS provider
	providers["edge"] = NewEdgeTTSProvider()

	return &TTSTool{
		providers:       providers,
		defaultProvider: defaultProvider,
		defaultVoice:    defaultVoice,
	}
}

func (t *TTSTool) Name() string {
	return "tts"
}

func (t *TTSTool) Description() string {
	return "Convert text to speech using multiple providers (OpenAI, Edge TTS)"
}

func (t *TTSTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: tts [provider:]<text> [--voice <voice>] [--speed 0.25-4.0]\nProviders: openai, edge\nUse 'edge:text' or 'openai:text' to specify provider")
	}

	// Parse provider prefix from first arg
	provider := t.defaultProvider
	text := strings.Join(args, " ")

	// Check for provider prefix (e.g., "edge:hello world")
	if colonIdx := strings.Index(text, ":"); colonIdx > 0 && colonIdx < 20 {
		possibleProvider := text[:colonIdx]
		if _, ok := t.providers[possibleProvider]; ok {
			provider = possibleProvider
			text = text[colonIdx+1:]
			// Re-parse args after removing provider prefix
			args = strings.Fields(text)
		}
	}

	// Parse arguments
	voice := t.defaultVoice
	speed := 1.0
	var textParts []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--voice":
			if i+1 < len(args) {
				voice = args[i+1]
				i++
			}
		case "--speed":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%f", &speed)
				i++
			}
		default:
			textParts = append(textParts, args[i])
		}
	}

	text = strings.Join(textParts, " ")
	if text == "" {
		return "", fmt.Errorf("text is required")
	}

	// Get the provider
	p, ok := t.providers[provider]
	if !ok {
		return "", fmt.Errorf("unknown provider: %s (available: openai, edge)", provider)
	}

	if !p.IsAvailable() {
		return "", fmt.Errorf("provider %s is not available", provider)
	}

	// For OpenAI provider, handle speed parameter separately
	var audioPath string
	var err error

	if provider == "openai" {
		openaiProvider := p.(*OpenAITTSProvider)
		audioPath, err = openaiProvider.SynthesizeWithSpeed(ctx, text, voice, speed)
	} else {
		// Other providers don't support speed
		audioPath, err = p.Synthesize(ctx, text, voice)
	}

	if err != nil {
		return "", err
	}

	return fmt.Sprintf("🔊 Speech generated!\n\nProvider: %s\nText: %s\nVoice: %s\nFile: %s", provider, text, voice, audioPath), nil
}

// OpenAITTSProvider implements TTS using OpenAI API
type OpenAITTSProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
	tempDir string
}

// NewOpenAITTSProvider creates a new OpenAI TTS provider
func NewOpenAITTSProvider(apiKey, baseURL string) *OpenAITTSProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	tempDir := filepath.Join(os.TempDir(), "okgobot-tts")
	os.MkdirAll(tempDir, 0755)

	return &OpenAITTSProvider{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 60 * time.Second},
		tempDir: tempDir,
	}
}

// Synthesize generates speech from text using OpenAI TTS
func (o *OpenAITTSProvider) Synthesize(ctx context.Context, text, voice string) (string, error) {
	return o.SynthesizeWithSpeed(ctx, text, voice, 1.0)
}

// SynthesizeWithSpeed generates speech with speed control
func (o *OpenAITTSProvider) SynthesizeWithSpeed(ctx context.Context, text, voice string, speed float64) (string, error) {
	if o.apiKey == "" {
		return "", fmt.Errorf("OpenAI API key not configured")
	}

	// Use default voice if not specified
	if voice == "" {
		voice = "alloy"
	}

	// Validate voice
	if !o.isValidVoice(voice) {
		return "", fmt.Errorf("invalid voice: %s (valid: %v)", voice, o.AvailableVoices())
	}

	// Validate speed
	if speed < 0.25 || speed > 4.0 {
		speed = 1.0
	}

	reqBody := map[string]interface{}{
		"model":           "tts-1",
		"input":           text,
		"voice":           voice,
		"speed":           speed,
		"response_format": "mp3",
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/audio/speech", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Save the audio file
	filename := fmt.Sprintf("tts_openai_%d.mp3", time.Now().UnixNano())
	mp3Path := filepath.Join(o.tempDir, filename)

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read audio: %w", err)
	}

	if err := os.WriteFile(mp3Path, audioData, 0644); err != nil {
		return "", fmt.Errorf("failed to save audio: %w", err)
	}

	// Convert to OGG for Telegram voice messages (if ffmpeg available)
	oggPath := convertToOGG(mp3Path)
	if oggPath != "" {
		return oggPath, nil
	}

	return mp3Path, nil
}

// IsAvailable checks if OpenAI TTS is available
func (o *OpenAITTSProvider) IsAvailable() bool {
	return o.apiKey != ""
}

// AvailableVoices returns the list of OpenAI TTS voices
func (o *OpenAITTSProvider) AvailableVoices() []string {
	return []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}
}

// isValidVoice checks if a voice is valid
func (o *OpenAITTSProvider) isValidVoice(voice string) bool {
	for _, v := range o.AvailableVoices() {
		if v == voice {
			return true
		}
	}
	return false
}

// Name returns the provider name
func (o *OpenAITTSProvider) Name() string {
	return "openai"
}

// convertToOGG converts MP3 to OGG using ffmpeg (shared utility function)
func convertToOGG(mp3Path string) string {
	// Check if ffmpeg is available
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "" // Return empty to use MP3
	}

	oggPath := strings.TrimSuffix(mp3Path, ".mp3") + ".ogg"

	cmd := exec.Command("ffmpeg", "-y", "-i", mp3Path,
		"-c:a", "libopus",
		"-b:a", "64k",
		"-vbr", "on",
		"-compression_level", "10",
		oggPath)

	if err := cmd.Run(); err != nil {
		return "" // Return empty to use MP3
	}

	// Clean up MP3
	os.Remove(mp3Path)

	return oggPath
}

// GetTempDir returns the temp directory for audio files
func (t *TTSTool) GetTempDir() string {
	// Return temp dir from any available provider
	for _, p := range t.providers {
		if openai, ok := p.(*OpenAITTSProvider); ok {
			return openai.tempDir
		}
		if edge, ok := p.(*EdgeTTSProvider); ok {
			return edge.tempDir
		}
	}
	return filepath.Join(os.TempDir(), "okgobot-tts")
}

// GetProvider returns a specific provider by name
func (t *TTSTool) GetProvider(name string) (TTSProvider, bool) {
	p, ok := t.providers[name]
	return p, ok
}
