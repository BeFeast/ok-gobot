package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEdgeTTSProvider_IsAvailable(t *testing.T) {
	provider := NewEdgeTTSProvider()

	// This test will pass if edge-tts is installed, skip otherwise
	if !provider.IsAvailable() {
		t.Skip("edge-tts not installed, skipping test")
	}

	if !provider.IsAvailable() {
		t.Error("Expected IsAvailable to return true when edge-tts is installed")
	}
}

func TestEdgeTTSProvider_AvailableVoices(t *testing.T) {
	provider := NewEdgeTTSProvider()

	voices := provider.AvailableVoices()
	expectedVoices := []string{
		"ru-RU-DmitryNeural",
		"ru-RU-SvetlanaNeural",
		"en-US-GuyNeural",
		"en-US-JennyNeural",
		"en-US-AriaNeural",
	}

	if len(voices) != len(expectedVoices) {
		t.Errorf("Expected %d voices, got %d", len(expectedVoices), len(voices))
	}

	for i, voice := range voices {
		if voice != expectedVoices[i] {
			t.Errorf("Expected voice %s at index %d, got %s", expectedVoices[i], i, voice)
		}
	}
}

func TestEdgeTTSProvider_Name(t *testing.T) {
	provider := NewEdgeTTSProvider()

	if provider.Name() != "edge" {
		t.Errorf("Expected provider name 'edge', got '%s'", provider.Name())
	}
}

func TestEdgeTTSProvider_Synthesize(t *testing.T) {
	provider := NewEdgeTTSProvider()

	if !provider.IsAvailable() {
		t.Skip("edge-tts not installed, skipping synthesis test")
	}

	ctx := context.Background()
	text := "Hello, this is a test"
	voice := "en-US-GuyNeural"

	audioPath, err := provider.Synthesize(ctx, text, voice)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	// Check that file exists
	if _, err := os.Stat(audioPath); os.IsNotExist(err) {
		t.Errorf("Audio file was not created at %s", audioPath)
	}

	// Check file extension
	ext := filepath.Ext(audioPath)
	if ext != ".mp3" && ext != ".ogg" {
		t.Errorf("Expected .mp3 or .ogg file, got %s", ext)
	}

	// Cleanup
	os.Remove(audioPath)
}

func TestEdgeTTSProvider_InvalidVoice(t *testing.T) {
	provider := NewEdgeTTSProvider()

	if !provider.IsAvailable() {
		t.Skip("edge-tts not installed, skipping test")
	}

	ctx := context.Background()
	text := "Test"
	voice := "invalid-voice"

	_, err := provider.Synthesize(ctx, text, voice)
	if err == nil {
		t.Error("Expected error for invalid voice, got nil")
	}
}

func TestEdgeTTSProvider_DefaultVoice(t *testing.T) {
	provider := NewEdgeTTSProvider()

	if !provider.IsAvailable() {
		t.Skip("edge-tts not installed, skipping test")
	}

	ctx := context.Background()
	text := "Test with default voice"

	audioPath, err := provider.Synthesize(ctx, text, "")
	if err != nil {
		t.Fatalf("Synthesize with default voice failed: %v", err)
	}

	// Check that file exists
	if _, err := os.Stat(audioPath); os.IsNotExist(err) {
		t.Errorf("Audio file was not created at %s", audioPath)
	}

	// Cleanup
	os.Remove(audioPath)
}
