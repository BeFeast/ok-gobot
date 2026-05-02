package videosummary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	defaultScribeURL    = "http://slava.ok.labs:19010"
	defaultPollInterval = 30 * time.Second
	defaultTimeout      = 2 * time.Hour
)

var (
	badFilenameChars = regexp.MustCompile(`[\\/:*?"<>|]`)
	whitespace       = regexp.MustCompile(`\s+`)
	transcriptRefRE  = regexp.MustCompile(`>\s*Transcript:\s*\[\[_transcripts/([^\]|]+)`)
	slugChars        = regexp.MustCompile(`[^a-zA-Z0-9]+`)
)

// Config controls the native video-summary workflow.
type Config struct {
	ScribeURL    string
	VaultDir     string
	PollInterval time.Duration
	Timeout      time.Duration
	HTTPClient   *http.Client
	Now          func() time.Time
}

// Submission is the accepted Scribe job.
type Submission struct {
	JobID       string
	Title       string
	Status      string
	Position    any
	StatusURL   string
	SubmittedAt time.Time
	SourceURL   string
}

// Result is the completed Obsidian write outcome.
type Result struct {
	JobID                     string
	Status                    string
	StatusURL                 string
	Title                     string
	SummaryPath               string
	TranscriptPath            string
	SummaryLink               string
	TranscriptLink            string
	ProcessingDuration        time.Duration
	ProcessingDurationDisplay string
}

// Progress is a non-terminal Scribe polling observation.
type Progress struct {
	JobID  string
	Status string
	Title  string
}

// Run submits url to Scribe, waits for completion, writes Obsidian files, and
// returns stable Obsidian links.
func Run(ctx context.Context, rawURL string, cfg Config, progress func(string)) (Submission, Result, error) {
	if err := ValidateYouTubeURL(rawURL); err != nil {
		return Submission{}, Result{}, err
	}
	submission, err := Submit(ctx, rawURL, cfg)
	if err != nil {
		return Submission{}, Result{}, err
	}
	if progress != nil {
		progress(fmt.Sprintf("scribe job accepted: %s", submission.JobID))
	}
	result, err := WaitAndWrite(ctx, submission, cfg)
	return submission, result, err
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

// Submit creates a Scribe transcription job.
func Submit(ctx context.Context, rawURL string, cfg Config) (Submission, error) {
	cfg = cfg.withDefaults()
	payload := map[string]any{
		"url":          rawURL,
		"skip_summary": false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Submission{}, err
	}
	endpoint := strings.TrimRight(cfg.ScribeURL, "/") + "/transcribe"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Submission{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return Submission{}, fmt.Errorf("scribe submit failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Submission{}, fmt.Errorf("scribe submit failed: HTTP %d: %s", resp.StatusCode, limitedBody(resp.Body))
	}

	var data struct {
		ID       any    `json:"id"`
		Title    string `json:"title"`
		Status   string `json:"status"`
		Position any    `json:"position"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return Submission{}, fmt.Errorf("decode scribe submit response: %w", err)
	}
	jobID := strings.TrimSpace(fmt.Sprint(data.ID))
	if jobID == "" || jobID == "<nil>" {
		return Submission{}, fmt.Errorf("scribe submit returned no job id")
	}
	title := strings.TrimSpace(data.Title)
	if title == "" {
		title = "Untitled video"
	}
	now := cfg.now()
	return Submission{
		JobID:       jobID,
		Title:       title,
		Status:      strings.TrimSpace(data.Status),
		Position:    data.Position,
		StatusURL:   strings.TrimRight(cfg.ScribeURL, "/") + "/status/" + url.PathEscape(jobID),
		SubmittedAt: now,
		SourceURL:   rawURL,
	}, nil
}

// WaitAndWrite polls Scribe until terminal success and writes summary/transcript
// Markdown into Digests/YYYY-MM-DD under the configured Obsidian vault.
func WaitAndWrite(ctx context.Context, submission Submission, cfg Config) (Result, error) {
	return WaitAndWriteWithProgress(ctx, submission, cfg, nil)
}

// WaitAndWriteWithProgress polls Scribe until terminal success and reports
// status changes before writing summary/transcript Markdown into Obsidian.
func WaitAndWriteWithProgress(ctx context.Context, submission Submission, cfg Config, progress func(Progress)) (Result, error) {
	cfg = cfg.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	lastStatus := ""

	var statusData scribeStatus
	for {
		var err error
		statusData, err = fetchStatus(ctx, cfg.HTTPClient, submission.StatusURL)
		if err != nil {
			return Result{}, err
		}
		status := strings.ToLower(strings.TrimSpace(statusData.Status))
		if progress != nil && status != "" && status != lastStatus && !isTerminalStatus(status) {
			lastStatus = status
			progress(Progress{
				JobID:  submission.JobID,
				Status: statusData.Status,
				Title:  firstNonEmpty(statusData.Title, submission.Title),
			})
		}
		switch status {
		case "done", "completed":
			return writeResult(ctx, cfg, submission, statusData)
		case "failed", "error", "cancelled", "canceled", "timeout":
			return Result{}, fmt.Errorf("scribe job ended with status %q", status)
		}

		select {
		case <-ctx.Done():
			return Result{}, fmt.Errorf("scribe job timed out after %s while status=%q", cfg.Timeout, status)
		case <-ticker.C:
		}
	}
}

func isTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "completed", "failed", "error", "cancelled", "canceled", "timeout":
		return true
	default:
		return false
	}
}

func (cfg Config) withDefaults() Config {
	cfg.ScribeURL = strings.TrimSpace(cfg.ScribeURL)
	if cfg.ScribeURL == "" {
		cfg.ScribeURL = defaultScribeURL
	}
	cfg.VaultDir = strings.TrimSpace(cfg.VaultDir)
	if cfg.VaultDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.VaultDir = filepath.Join(home, "Obsidian Vault")
		}
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

type scribeStatus struct {
	Status    string            `json:"status"`
	Title     string            `json:"title"`
	Artifacts map[string]string `json:"artifacts"`
}

func fetchStatus(ctx context.Context, client *http.Client, statusURL string) (scribeStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return scribeStatus{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return scribeStatus{}, fmt.Errorf("scribe status failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return scribeStatus{}, fmt.Errorf("scribe status failed: HTTP %d: %s", resp.StatusCode, limitedBody(resp.Body))
	}
	var data scribeStatus
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return scribeStatus{}, fmt.Errorf("decode scribe status response: %w", err)
	}
	return data, nil
}

func writeResult(ctx context.Context, cfg Config, submission Submission, statusData scribeStatus) (Result, error) {
	summaryURL := strings.TrimSpace(statusData.Artifacts["summary_markdown"])
	transcriptURL := strings.TrimSpace(statusData.Artifacts["transcript_markdown"])
	if summaryURL == "" || transcriptURL == "" {
		return Result{}, fmt.Errorf("scribe completed but summary/transcript artifacts are missing")
	}
	summaryText, err := fetchText(ctx, cfg.HTTPClient, resolveArtifactURL(submission.StatusURL, summaryURL))
	if err != nil {
		return Result{}, fmt.Errorf("fetch summary artifact: %w", err)
	}
	transcriptText, err := fetchText(ctx, cfg.HTTPClient, resolveArtifactURL(submission.StatusURL, transcriptURL))
	if err != nil {
		return Result{}, fmt.Errorf("fetch transcript artifact: %w", err)
	}

	title := sanitizeTitle(firstNonEmpty(statusData.Title, submission.Title, "Untitled video"))
	day := cfg.now().Format("2006-01-02")
	digestDir := filepath.Join(cfg.VaultDir, "Digests", day)
	transcriptDir := filepath.Join(digestDir, "_transcripts")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create digest directory: %w", err)
	}

	transcriptSlug := transcriptSlugFromSummary(summaryText, title)
	summaryPath := uniquePath(filepath.Join(digestDir, title+".md"), submission.JobID)
	transcriptPath := uniquePath(filepath.Join(transcriptDir, transcriptSlug+".md"), submission.JobID)

	if err := os.WriteFile(summaryPath, []byte(summaryText), 0o644); err != nil {
		return Result{}, fmt.Errorf("write summary: %w", err)
	}
	if err := os.WriteFile(transcriptPath, []byte(transcriptText), 0o644); err != nil {
		return Result{}, fmt.Errorf("write transcript: %w", err)
	}

	duration := cfg.now().Sub(submission.SubmittedAt).Round(time.Second)
	if duration < 0 {
		duration = 0
	}
	return Result{
		JobID:                     submission.JobID,
		Status:                    statusData.Status,
		StatusURL:                 submission.StatusURL,
		Title:                     title,
		SummaryPath:               summaryPath,
		TranscriptPath:            transcriptPath,
		SummaryLink:               obsidianURL(cfg.VaultDir, summaryPath),
		TranscriptLink:            obsidianURL(cfg.VaultDir, transcriptPath),
		ProcessingDuration:        duration,
		ProcessingDurationDisplay: formatDuration(duration),
	}, nil
}

func fetchText(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/markdown,text/plain,*/*")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, limitedBody(resp.Body))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func limitedBody(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, 4096))
	return strings.TrimSpace(string(data))
}

func resolveArtifactURL(statusURL, artifactURL string) string {
	parsed, err := url.Parse(artifactURL)
	if err == nil && parsed.IsAbs() {
		return artifactURL
	}
	base, err := url.Parse(statusURL)
	if err != nil {
		return artifactURL
	}
	return base.ResolveReference(&url.URL{Path: artifactURL}).String()
}

func sanitizeTitle(value string) string {
	value = badFilenameChars.ReplaceAllString(strings.TrimSpace(value), " ")
	value = whitespace.ReplaceAllString(value, " ")
	value = strings.TrimRight(strings.TrimSpace(value), ".")
	if value == "" {
		return "Untitled Video"
	}
	return value
}

func transcriptSlugFromSummary(summary, title string) string {
	match := transcriptRefRE.FindStringSubmatch(summary)
	if len(match) > 1 {
		if slug := strings.TrimSpace(match[1]); slug != "" {
			return slug
		}
	}
	return slugify(title)
}

func slugify(value string) string {
	slug := slugChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "transcript"
	}
	return slug
}

func uniquePath(path, jobID string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(filepath.Base(path), ext)
	suffix := jobID
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	return filepath.Join(filepath.Dir(path), stem+" - "+suffix+ext)
}

func obsidianURL(vaultDir, filePath string) string {
	rel, err := filepath.Rel(vaultDir, filePath)
	if err != nil {
		rel = filePath
	}
	rel = filepath.ToSlash(rel)
	return "obsidian://open?vault=" + url.PathEscape(filepath.Base(vaultDir)) + "&file=" + url.PathEscape(rel)
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
