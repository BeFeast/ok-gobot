package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// VoiceTranscriber transcribes audio via a Whisper-compatible HTTP API.
type VoiceTranscriber struct {
	baseURL   string
	apiKey    string
	threshold float64 // minimum confidence to process without a warning (0.0–1.0)
	client    *http.Client
}

// TranscriptionResult holds the output of a transcription call.
type TranscriptionResult struct {
	Text       string
	Confidence float64 // 1.0 = high confidence, 0.0 = unintelligible
}

// NewVoiceTranscriber creates a transcriber that calls a Whisper-compatible API.
// baseURL should point to the API root, e.g. "https://scribe.ok.labs/v1".
// threshold is the minimum confidence (0.0–1.0) required to process without a
// confirmation prompt; values outside the range are clamped to 0.6.
func NewVoiceTranscriber(baseURL, apiKey string, threshold float64) *VoiceTranscriber {
	if threshold <= 0 || threshold > 1.0 {
		threshold = 0.6
	}
	return &VoiceTranscriber{
		baseURL:   baseURL,
		apiKey:    apiKey,
		threshold: threshold,
		client:    &http.Client{Timeout: 120 * time.Second},
	}
}

// IsAvailable returns true when a base URL is configured.
func (v *VoiceTranscriber) IsAvailable() bool {
	return v.baseURL != ""
}

// Transcribe sends the audio file at audioPath to the Whisper API and returns
// the transcribed text together with a confidence score.
//
// Confidence is derived from the average no_speech_prob returned by the
// verbose_json response format: confidence = 1 - avg(no_speech_prob).
// If the server does not return segments, confidence defaults to 1.0.
func (v *VoiceTranscriber) Transcribe(ctx context.Context, audioPath string) (*TranscriptionResult, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return nil, fmt.Errorf("open audio file: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	part, err := mw.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return nil, fmt.Errorf("copy audio data: %w", err)
	}

	if err := mw.WriteField("model", "whisper-1"); err != nil {
		return nil, fmt.Errorf("write model field: %w", err)
	}
	if err := mw.WriteField("response_format", "verbose_json"); err != nil {
		return nil, fmt.Errorf("write response_format field: %w", err)
	}
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", v.baseURL+"/audio/transcriptions", &buf)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if v.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+v.apiKey)
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("transcription request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("transcription API error (status %d): %s", resp.StatusCode, string(body))
	}

	var payload struct {
		Text     string `json:"text"`
		Segments []struct {
			NoSpeechProb float64 `json:"no_speech_prob"`
		} `json:"segments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode transcription response: %w", err)
	}

	confidence := 1.0
	if len(payload.Segments) > 0 {
		var total float64
		for _, seg := range payload.Segments {
			total += seg.NoSpeechProb
		}
		confidence = 1.0 - total/float64(len(payload.Segments))
	}

	return &TranscriptionResult{
		Text:       payload.Text,
		Confidence: confidence,
	}, nil
}

// IsHighConfidence returns true when confidence meets or exceeds the configured
// threshold.
func (v *VoiceTranscriber) IsHighConfidence(confidence float64) bool {
	return confidence >= v.threshold
}
