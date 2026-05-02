package karaoke

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultServiceURL   = "http://slava.ok.labs:19020"
	defaultPollInterval = 10 * time.Second
	defaultTimeout      = 3 * time.Hour
)

// Config controls the native YouTube karaoke workflow.
type Config struct {
	ServiceURL   string
	Token        string
	Profile      string
	LyricsMode   string
	PollInterval time.Duration
	Timeout      time.Duration
	HTTPClient   *http.Client
	Now          func() time.Time
}

// Submission is the accepted KaraokeService job.
type Submission struct {
	JobID     string
	Title     string
	Status    string
	Stage     string
	StatusURL string
	PageURL   string
	SourceURL string
}

// Result is the completed KaraokeService outcome.
type Result struct {
	JobID                     string
	Title                     string
	Status                    string
	Stage                     string
	StatusURL                 string
	PageURL                   string
	KaraokeMP3URL             string
	VocalsMP3URL              string
	LyricsTextURL             string
	ProcessingDuration        time.Duration
	ProcessingDurationDisplay string
}

// ValidateYouTubeURL accepts normal YouTube watch URLs and youtu.be short links.
func ValidateYouTubeURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("YouTube URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid YouTube URL")
	}
	host := strings.ToLower(parsed.Hostname())
	switch {
	case host == "youtu.be":
		return nil
	case host == "youtube.com" || host == "www.youtube.com" || strings.HasSuffix(host, ".youtube.com"):
		return nil
	default:
		return fmt.Errorf("URL host %q is not YouTube", host)
	}
}

// Health checks service reachability. It does not require an auth token.
func Health(ctx context.Context, cfg Config) error {
	cfg = cfg.withDefaults()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.ServiceURL, "/")+"/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("karaoke health failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("karaoke health failed: HTTP %d: %s", resp.StatusCode, limitedBody(resp.Body))
	}
	return nil
}

// Submit creates a KaraokeService YouTube job.
func Submit(ctx context.Context, rawURL string, cfg Config) (Submission, error) {
	if err := ValidateYouTubeURL(rawURL); err != nil {
		return Submission{}, err
	}
	cfg = cfg.withDefaults()
	if strings.TrimSpace(cfg.Token) == "" {
		return Submission{}, fmt.Errorf("karaoke service token is not configured")
	}
	if err := Health(ctx, cfg); err != nil {
		return Submission{}, err
	}

	payload := map[string]any{
		"url":             rawURL,
		"profile":         cfg.Profile,
		"lyrics_mode":     cfg.LyricsMode,
		"keep_full_stems": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Submission{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.ServiceURL, "/")+"/jobs/youtube", bytes.NewReader(body))
	if err != nil {
		return Submission{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return Submission{}, fmt.Errorf("karaoke submit failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Submission{}, fmt.Errorf("karaoke submit failed: HTTP %d: %s", resp.StatusCode, limitedBody(resp.Body))
	}

	var data serviceStatus
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return Submission{}, fmt.Errorf("decode karaoke submit response: %w", err)
	}
	jobID := strings.TrimSpace(data.JobID)
	if jobID == "" {
		return Submission{}, fmt.Errorf("karaoke service did not return a job_id")
	}
	return Submission{
		JobID:     jobID,
		Title:     strings.TrimSpace(data.Title),
		Status:    strings.TrimSpace(data.Status),
		Stage:     strings.TrimSpace(data.Stage),
		StatusURL: strings.TrimRight(cfg.ServiceURL, "/") + "/status/" + url.PathEscape(jobID),
		PageURL:   strings.TrimSpace(data.Share.PageURL),
		SourceURL: rawURL,
	}, nil
}

// Wait polls an existing KaraokeService job until completion and verifies links.
func Wait(ctx context.Context, submission Submission, cfg Config) (Result, error) {
	cfg = cfg.withDefaults()
	if strings.TrimSpace(cfg.Token) == "" {
		return Result{}, fmt.Errorf("karaoke service token is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	startedAt := cfg.now()

	for {
		data, err := fetchStatus(ctx, cfg, submission.JobID)
		if err != nil {
			return Result{}, err
		}
		status := strings.ToLower(strings.TrimSpace(data.Status))
		switch status {
		case "completed", "done":
			if err := verifyShareLinks(ctx, cfg.HTTPClient, data.Share); err != nil {
				return Result{}, err
			}
			duration := cfg.now().Sub(startedAt).Round(time.Second)
			if duration < 0 {
				duration = 0
			}
			return Result{
				JobID:                     firstNonEmpty(data.JobID, submission.JobID),
				Title:                     firstNonEmpty(data.Title, submission.Title, "Untitled track"),
				Status:                    data.Status,
				Stage:                     data.Stage,
				StatusURL:                 submission.StatusURL,
				PageURL:                   data.Share.PageURL,
				KaraokeMP3URL:             data.Share.KaraokeMP3URL,
				VocalsMP3URL:              data.Share.VocalsMP3URL,
				LyricsTextURL:             data.Share.LyricsTextURL,
				ProcessingDuration:        duration,
				ProcessingDurationDisplay: formatDuration(duration),
			}, nil
		case "failed", "error", "cancelled", "canceled", "expired":
			if data.Error != "" {
				return Result{}, fmt.Errorf("karaoke job ended with status %q: %s", status, data.Error)
			}
			return Result{}, fmt.Errorf("karaoke job ended with status %q", status)
		}

		select {
		case <-ctx.Done():
			return Result{}, fmt.Errorf("karaoke job timed out after %s while status=%q stage=%q", cfg.Timeout, status, data.Stage)
		case <-ticker.C:
		}
	}
}

func (cfg Config) withDefaults() Config {
	cfg.ServiceURL = strings.TrimSpace(cfg.ServiceURL)
	if cfg.ServiceURL == "" {
		cfg.ServiceURL = strings.TrimSpace(os.Getenv("KARAOKE_SERVICE_URL"))
	}
	if cfg.ServiceURL == "" {
		cfg.ServiceURL = defaultServiceURL
	}
	cfg.Token = strings.TrimSpace(cfg.Token)
	if cfg.Token == "" {
		cfg.Token = strings.TrimSpace(os.Getenv("KARAOKE_SERVICE_TOKEN"))
	}
	cfg.Profile = strings.TrimSpace(cfg.Profile)
	if cfg.Profile == "" {
		cfg.Profile = "karaoke"
	}
	cfg.LyricsMode = strings.TrimSpace(cfg.LyricsMode)
	if cfg.LyricsMode == "" {
		cfg.LyricsMode = "plain"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	return cfg
}

func (cfg Config) now() time.Time {
	if cfg.Now != nil {
		return cfg.Now()
	}
	return time.Now()
}

type serviceStatus struct {
	JobID  string     `json:"job_id"`
	Title  string     `json:"title"`
	Status string     `json:"status"`
	Stage  string     `json:"stage"`
	Error  string     `json:"error"`
	Share  shareLinks `json:"share"`
}

type shareLinks struct {
	PageURL       string `json:"page_url"`
	KaraokeMP3URL string `json:"karaoke_mp3_url"`
	VocalsMP3URL  string `json:"vocals_mp3_url"`
	LyricsTextURL string `json:"lyrics_txt_url"`
}

func fetchStatus(ctx context.Context, cfg Config, jobID string) (serviceStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.ServiceURL, "/")+"/status/"+url.PathEscape(jobID), nil)
	if err != nil {
		return serviceStatus{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return serviceStatus{}, fmt.Errorf("karaoke status failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return serviceStatus{}, fmt.Errorf("karaoke status failed: HTTP %d: %s", resp.StatusCode, limitedBody(resp.Body))
	}
	var data serviceStatus
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return serviceStatus{}, fmt.Errorf("decode karaoke status response: %w", err)
	}
	return data, nil
}

func verifyShareLinks(ctx context.Context, client *http.Client, links shareLinks) error {
	required := map[string]string{
		"share page":  links.PageURL,
		"karaoke MP3": links.KaraokeMP3URL,
		"vocals MP3":  links.VocalsMP3URL,
		"lyrics text": links.LyricsTextURL,
	}
	for label, rawURL := range required {
		if strings.TrimSpace(rawURL) == "" {
			return fmt.Errorf("completed karaoke job is missing %s link", label)
		}
	}
	if err := checkURL(ctx, client, links.PageURL, "page", false); err != nil {
		return err
	}
	if err := checkURL(ctx, client, links.KaraokeMP3URL, "karaoke MP3", true); err != nil {
		return err
	}
	if err := checkURL(ctx, client, links.VocalsMP3URL, "vocals MP3", true); err != nil {
		return err
	}
	if err := checkURL(ctx, client, links.LyricsTextURL, "lyrics text", false); err != nil {
		return err
	}
	return nil
}

func checkURL(ctx context.Context, client *http.Client, rawURL, label string, audio bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("%s link is invalid: %w", label, err)
	}
	if audio {
		req.Header.Set("Range", "bytes=0-0")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s link check failed: %w", label, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	_, _ = io.CopyN(io.Discard, resp.Body, 1024)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s link check failed: HTTP %d", label, resp.StatusCode)
	}
	return nil
}

func limitedBody(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, 4096))
	return strings.TrimSpace(string(data))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func formatDuration(d time.Duration) string {
	seconds := int(d.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	secs := seconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %ds", minutes, secs)
	}
	hours := minutes / 60
	minutes = minutes % 60
	return fmt.Sprintf("%dh %dm %ds", hours, minutes, secs)
}
