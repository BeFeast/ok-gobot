package cli

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
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
)

func newVoiceCommand(cfg *config.Config) *cobra.Command {
	var (
		duration int
		output   string
	)

	cmd := &cobra.Command{
		Use:   "voice",
		Short: "Record voice from microphone and transcribe via STT",
		Long: `Record audio from the microphone and transcribe it using the configured
speech-to-text service (Whisper-compatible API at stt.base_url).

Requires either sox or ffmpeg to be installed for microphone recording.

Examples:
  ok-gobot voice                  # Record 30s and print transcription
  ok-gobot voice --duration 10    # Record 10 seconds
  ok-gobot voice --output result.txt  # Save transcription to file`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVoiceCommand(cmd.Context(), cfg, duration, output)
		},
	}

	cmd.Flags().IntVarP(&duration, "duration", "d", 30, "maximum recording duration in seconds")
	cmd.Flags().StringVarP(&output, "output", "o", "", "write transcription to file instead of stdout")

	return cmd
}

func runVoiceCommand(ctx context.Context, cfg *config.Config, duration int, outputPath string) error {
	if cfg.STT.BaseURL == "" {
		return fmt.Errorf("stt.base_url is not configured — set it to your Whisper API endpoint (e.g. https://scribe.example.com/v1)")
	}

	// Find a recording tool
	recorder, err := findRecorder()
	if err != nil {
		return fmt.Errorf("no audio recorder found: %w\nInstall sox (brew install sox) or ffmpeg (brew install ffmpeg)", err)
	}

	// Create a temp file for the recording
	tmp, err := os.CreateTemp("", "voice_*.ogg")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmp.Close()
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	fmt.Fprintf(os.Stderr, "🎤 Recording... (press Ctrl+C to stop, max %ds)\n", duration)

	recordCtx, cancel := context.WithTimeout(ctx, time.Duration(duration)*time.Second)
	defer cancel()

	if err := recorder(recordCtx, tmpPath); err != nil && recordCtx.Err() == nil {
		return fmt.Errorf("recording failed: %w", err)
	}

	fmt.Fprintln(os.Stderr, "⏳ Transcribing...")

	text, err := transcribeFile(ctx, cfg.STT.BaseURL, cfg.STT.APIKey, tmpPath)
	if err != nil {
		return fmt.Errorf("transcription failed: %w", err)
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("transcription returned empty result")
	}

	if outputPath != "" {
		if err := os.WriteFile(outputPath, []byte(text+"\n"), 0644); err != nil {
			return fmt.Errorf("write output file: %w", err)
		}
		fmt.Fprintf(os.Stderr, "✅ Transcription saved to %s\n", outputPath)
	} else {
		fmt.Println(text)
	}

	return nil
}

// findRecorder returns a function that records microphone audio to a file.
// Tries sox first, then ffmpeg (macOS avfoundation).
func findRecorder() (func(ctx context.Context, outPath string) error, error) {
	if _, err := exec.LookPath("sox"); err == nil {
		return func(ctx context.Context, outPath string) error {
			cmd := exec.CommandContext(ctx, "sox", "-d", outPath)
			cmd.Stderr = io.Discard
			return cmd.Run()
		}, nil
	}

	if _, err := exec.LookPath("ffmpeg"); err == nil {
		return func(ctx context.Context, outPath string) error {
			cmd := exec.CommandContext(ctx, "ffmpeg",
				"-f", "avfoundation",
				"-i", ":0",
				"-y",
				outPath,
			)
			cmd.Stderr = io.Discard
			return cmd.Run()
		}, nil
	}

	return nil, fmt.Errorf("neither sox nor ffmpeg found in PATH")
}

// transcribeFile sends an audio file to the Whisper API and returns the text.
func transcribeFile(ctx context.Context, baseURL, apiKey, audioPath string) (string, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return "", fmt.Errorf("open audio file: %w", err)
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
		return "", fmt.Errorf("write model field: %w", err)
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
