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

// TTSTool provides text-to-speech capabilities
type TTSTool struct {
	apiKey  string
	baseURL string
	client  *http.Client
	tempDir string
}

// NewTTSTool creates a new TTS tool
func NewTTSTool(apiKey, baseURL string) *TTSTool {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	tempDir := filepath.Join(os.TempDir(), "okgobot-tts")
	os.MkdirAll(tempDir, 0755)

	return &TTSTool{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 60 * time.Second},
		tempDir: tempDir,
	}
}

func (t *TTSTool) Name() string {
	return "tts"
}

func (t *TTSTool) Description() string {
	return "Convert text to speech using OpenAI TTS"
}

func (t *TTSTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: tts <text> [--voice alloy|echo|fable|onyx|nova|shimmer] [--speed 0.25-4.0]")
	}

	// Parse arguments
	voice := "alloy"
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

	text := strings.Join(textParts, " ")
	if text == "" {
		return "", fmt.Errorf("text is required")
	}

	// Validate voice
	validVoices := map[string]bool{
		"alloy": true, "echo": true, "fable": true,
		"onyx": true, "nova": true, "shimmer": true,
	}
	if !validVoices[voice] {
		return "", fmt.Errorf("invalid voice: %s (valid: alloy, echo, fable, onyx, nova, shimmer)", voice)
	}

	// Validate speed
	if speed < 0.25 || speed > 4.0 {
		speed = 1.0
	}

	// Generate speech
	audioPath, err := t.generateSpeech(ctx, text, voice, speed)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("🔊 Speech generated!\n\nText: %s\nVoice: %s\nFile: %s", text, voice, audioPath), nil
}

// generateSpeech calls the OpenAI TTS API
func (t *TTSTool) generateSpeech(ctx context.Context, text, voice string, speed float64) (string, error) {
	if t.apiKey == "" {
		return "", fmt.Errorf("OpenAI API key not configured")
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

	req, err := http.NewRequestWithContext(ctx, "POST", t.baseURL+"/audio/speech", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Save the audio file
	filename := fmt.Sprintf("tts_%d.mp3", time.Now().UnixNano())
	mp3Path := filepath.Join(t.tempDir, filename)

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read audio: %w", err)
	}

	if err := os.WriteFile(mp3Path, audioData, 0644); err != nil {
		return "", fmt.Errorf("failed to save audio: %w", err)
	}

	// Convert to OGG for Telegram voice messages (if ffmpeg available)
	oggPath := t.convertToOGG(mp3Path)
	if oggPath != "" {
		return oggPath, nil
	}

	return mp3Path, nil
}

// convertToOGG converts MP3 to OGG using ffmpeg
func (t *TTSTool) convertToOGG(mp3Path string) string {
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

// GetLastAudioPath returns the temp directory for audio files
func (t *TTSTool) GetTempDir() string {
	return t.tempDir
}

// AvailableVoices returns the list of available TTS voices
func AvailableVoices() []string {
	return []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}
}
