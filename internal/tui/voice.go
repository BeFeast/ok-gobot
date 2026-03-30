package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// voiceRecordCmd records audio from the microphone and transcribes it.
// It returns a tea.Cmd that eventually resolves to a voiceResultMsg.
func voiceRecordCmd(sttBaseURL, sttAPIKey string, durationSec int) tea.Cmd {
	return func() tea.Msg {
		// Create a temp file for the recording
		tmp, err := os.CreateTemp("", "tui_voice_*.ogg")
		if err != nil {
			return voiceResultMsg{err: fmt.Errorf("create temp file: %w", err)}
		}
		tmp.Close()
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)

		// Record audio (context with timeout = max duration)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(durationSec)*time.Second)
		defer cancel()

		if err := recordMic(ctx, tmpPath); err != nil && ctx.Err() == nil {
			return voiceResultMsg{err: fmt.Errorf("recording failed: %w", err)}
		}

		// Transcribe
		text, err := tuiTranscribeFile(context.Background(), sttBaseURL, sttAPIKey, tmpPath)
		if err != nil {
			return voiceResultMsg{err: fmt.Errorf("transcription failed: %w", err)}
		}

		return voiceResultMsg{text: text}
	}
}

// recordMic records from the default microphone to outPath.
// Tries sox first (cross-platform), then ffmpeg (macOS avfoundation).
func recordMic(ctx context.Context, outPath string) error {
	if _, err := exec.LookPath("sox"); err == nil {
		cmd := exec.CommandContext(ctx, "sox", "-d", outPath)
		cmd.Stderr = io.Discard
		return cmd.Run()
	}

	if _, err := exec.LookPath("ffmpeg"); err == nil {
		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-f", "avfoundation",
			"-i", ":0",
			"-y",
			outPath,
		)
		cmd.Stderr = io.Discard
		return cmd.Run()
	}

	return fmt.Errorf("neither sox nor ffmpeg found in PATH; install one to use /voice")
}

// tuiTranscribeFile sends an audio file to the Whisper-compatible API.
func tuiTranscribeFile(ctx context.Context, baseURL, apiKey, audioPath string) (string, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return "", fmt.Errorf("open audio: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	part, err := mw.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", fmt.Errorf("copy audio: %w", err)
	}
	if err := mw.WriteField("model", "whisper-1"); err != nil {
		return "", fmt.Errorf("write model: %w", err)
	}
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/audio/transcriptions", &buf)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return result.Text, nil
}
