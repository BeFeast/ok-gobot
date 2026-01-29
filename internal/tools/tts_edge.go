package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// EdgeTTSProvider implements TTS using Microsoft Edge's speech service
type EdgeTTSProvider struct {
	tempDir string
}

// NewEdgeTTSProvider creates a new Edge TTS provider
func NewEdgeTTSProvider() *EdgeTTSProvider {
	tempDir := filepath.Join(os.TempDir(), "okgobot-tts")
	os.MkdirAll(tempDir, 0755)

	return &EdgeTTSProvider{
		tempDir: tempDir,
	}
}

// Synthesize generates speech from text using Edge TTS
func (e *EdgeTTSProvider) Synthesize(ctx context.Context, text, voice string) (string, error) {
	if !e.IsAvailable() {
		return "", fmt.Errorf("edge-tts command not found in PATH. Install with: pip install edge-tts")
	}

	// Use default voice if not specified
	if voice == "" {
		voice = "ru-RU-DmitryNeural"
	}

	// Validate voice
	if !e.isValidVoice(voice) {
		return "", fmt.Errorf("invalid voice: %s (valid: %v)", voice, e.AvailableVoices())
	}

	// Generate unique filename
	filename := fmt.Sprintf("tts_edge_%d.mp3", time.Now().UnixNano())
	mp3Path := filepath.Join(e.tempDir, filename)

	// Run edge-tts command
	cmd := exec.CommandContext(ctx, "edge-tts",
		"--text", text,
		"--voice", voice,
		"--write-media", mp3Path,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("edge-tts failed: %w (output: %s)", err, string(output))
	}

	// Verify file was created
	if _, err := os.Stat(mp3Path); err != nil {
		return "", fmt.Errorf("edge-tts did not create output file: %w", err)
	}

	// Convert to OGG for Telegram voice messages (if ffmpeg available)
	oggPath := convertToOGG(mp3Path)
	if oggPath != "" {
		return oggPath, nil
	}

	return mp3Path, nil
}

// IsAvailable checks if edge-tts command exists in PATH
func (e *EdgeTTSProvider) IsAvailable() bool {
	_, err := exec.LookPath("edge-tts")
	return err == nil
}

// AvailableVoices returns the list of supported Edge TTS voices
func (e *EdgeTTSProvider) AvailableVoices() []string {
	return []string{
		"ru-RU-DmitryNeural",
		"ru-RU-SvetlanaNeural",
		"en-US-GuyNeural",
		"en-US-JennyNeural",
		"en-US-AriaNeural",
	}
}

// isValidVoice checks if a voice is in the supported list
func (e *EdgeTTSProvider) isValidVoice(voice string) bool {
	for _, v := range e.AvailableVoices() {
		if v == voice {
			return true
		}
	}
	return false
}

// GetTempDir returns the temp directory for audio files
func (e *EdgeTTSProvider) GetTempDir() string {
	return e.tempDir
}

// Name returns the provider name
func (e *EdgeTTSProvider) Name() string {
	return "edge"
}
