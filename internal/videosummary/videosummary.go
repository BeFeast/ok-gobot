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
	"strconv"
	"strings"
	"time"
)

const (
	defaultPollInterval = 5 * time.Second
	defaultTimeout      = 2 * time.Hour
	maxTitleLength      = 120
	maxSlugLength       = 96
	maxJobSuffixLength  = 24
)

var (
	badFilenameChars = regexp.MustCompile(`[\\/:*?"<>|]`)
	whitespace       = regexp.MustCompile(`\s+`)
	transcriptRefRE  = regexp.MustCompile(`>\s*Transcript:\s*\[\[_transcripts/([^\]|]+)`)
	slugChars        = regexp.MustCompile(`[^a-zA-Z0-9]+`)
)

// Config controls the native video-summary workflow.
type Config struct {
	ScribeURL     string
	APIToken      string
	SummaryPrompt string
	VaultDir      string
	PollInterval  time.Duration
	Timeout       time.Duration
	HTTPClient    *http.Client
	Now           func() time.Time
}

// Submission is the accepted Scribe job.
type Submission struct {
	JobID       string
	Title       string
	Status      string
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
	if cfg.ScribeURL == "" {
		return Submission{}, fmt.Errorf("scribe service URL is not configured")
	}
	payload := map[string]any{
		"url":       rawURL,
		"source":    "ok-gobot",
		"summarize": true,
		"notify":    false,
	}
	if prompt := strings.TrimSpace(cfg.SummaryPrompt); prompt != "" {
		payload["summary_prompt"] = prompt
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Submission{}, err
	}
	endpoint := strings.TrimRight(cfg.ScribeURL, "/") + "/jobs"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Submission{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	setAPIHeaders(req, cfg.APIToken, "application/json")

	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return Submission{}, fmt.Errorf("scribe submit failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Submission{}, fmt.Errorf("scribe submit failed: HTTP %d: %s", resp.StatusCode, limitedBody(resp.Body))
	}

	var data struct {
		JobID  any    `json:"job_id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return Submission{}, fmt.Errorf("decode scribe submit response: %w", err)
	}
	jobID := strings.TrimSpace(fmt.Sprint(data.JobID))
	if jobID == "" || jobID == "<nil>" {
		return Submission{}, fmt.Errorf("scribe submit returned no job id")
	}
	title := strings.TrimSpace(data.Title)
	now := cfg.now()
	return Submission{
		JobID:       jobID,
		Title:       title,
		Status:      strings.TrimSpace(data.Status),
		StatusURL:   strings.TrimRight(cfg.ScribeURL, "/") + "/jobs/" + url.PathEscape(jobID),
		SubmittedAt: now,
		SourceURL:   rawURL,
	}, nil
}

// WaitAndWrite polls Scribe until terminal success and writes summary/transcript
// Markdown into _Assets/Daily Notes/YYYY/MM/DD under the configured Obsidian vault.
func WaitAndWrite(ctx context.Context, submission Submission, cfg Config) (Result, error) {
	cfg = cfg.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	var statusData scribeStatus
	for {
		var err error
		statusData, err = fetchStatus(ctx, cfg, submission.StatusURL)
		if err != nil {
			return Result{}, err
		}
		status := strings.ToLower(strings.TrimSpace(statusData.Status))
		switch status {
		case "done", "completed":
			return writeResult(ctx, cfg, submission, statusData)
		case "failed", "error", "cancelled", "canceled", "timeout":
			reason := strings.TrimSpace(statusData.Error)
			if reason == "" {
				reason = "no error detail returned"
			}
			return Result{}, fmt.Errorf("scribe job ended with status %q: %s", status, reason)
		}

		select {
		case <-ctx.Done():
			return Result{}, fmt.Errorf("scribe job timed out after %s while status=%q", cfg.Timeout, status)
		case <-ticker.C:
		}
	}
}

func (cfg Config) withDefaults() Config {
	cfg.ScribeURL = strings.TrimSpace(cfg.ScribeURL)
	cfg.APIToken = strings.TrimSpace(cfg.APIToken)
	cfg.SummaryPrompt = strings.TrimSpace(cfg.SummaryPrompt)
	cfg.VaultDir = strings.TrimSpace(cfg.VaultDir)
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
	JobID      any               `json:"job_id"`
	Status     string            `json:"status"`
	Title      string            `json:"title"`
	Error      string            `json:"error"`
	Transcript *scribeTranscript `json:"transcript"`
}

type scribeTranscript struct {
	ID                  int    `json:"id"`
	Title               string `json:"title"`
	SummaryShortlink    string `json:"summary_shortlink"`
	TranscriptShortlink string `json:"transcript_shortlink"`
}

func fetchStatus(ctx context.Context, cfg Config, statusURL string) (scribeStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return scribeStatus{}, err
	}
	setAPIHeaders(req, cfg.APIToken, "application/json")
	resp, err := cfg.HTTPClient.Do(req)
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
	if cfg.VaultDir == "" {
		return Result{}, fmt.Errorf("obsidian vault directory is not configured")
	}
	if statusData.Transcript == nil || statusData.Transcript.ID <= 0 {
		return Result{}, fmt.Errorf("scribe completed but transcript metadata is missing")
	}
	baseURL := strings.TrimRight(cfg.ScribeURL, "/")
	transcriptID := strconv.Itoa(statusData.Transcript.ID)
	summaryURL := baseURL + "/transcripts/" + url.PathEscape(transcriptID) + "/summary.md"
	transcriptURL := baseURL + "/transcripts/" + url.PathEscape(transcriptID) + "/transcript.md"
	summaryText, err := fetchText(ctx, cfg, summaryURL)
	if err != nil {
		return Result{}, fmt.Errorf("fetch summary artifact: %w", err)
	}
	transcriptText, err := fetchText(ctx, cfg, transcriptURL)
	if err != nil {
		return Result{}, fmt.Errorf("fetch transcript artifact: %w", err)
	}

	title := sanitizeTitle(firstNonEmpty(statusData.Title, statusData.Transcript.Title, submission.Title, "Untitled video"))
	day := cfg.now()
	dayParts := []string{"_Assets", "Daily Notes", day.Format("2006"), day.Format("01"), day.Format("02")}
	digestDir := filepath.Join(append([]string{cfg.VaultDir}, dayParts...)...)
	transcriptDir := filepath.Join(digestDir, "_transcripts")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create digest directory: %w", err)
	}

	transcriptSlug := transcriptSlugFromSummary(summaryText, title)
	if match := transcriptRefRE.FindStringSubmatchIndex(summaryText); len(match) >= 4 {
		summaryText = summaryText[:match[2]] + transcriptSlug + summaryText[match[3]:]
	} else {
		summaryText = fmt.Sprintf("> Transcript: [[_transcripts/%s|Transcript]]\n\n%s", transcriptSlug, summaryText)
	}
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

func fetchText(ctx context.Context, cfg Config, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	setAPIHeaders(req, cfg.APIToken, "text/markdown,text/plain,*/*")
	resp, err := cfg.HTTPClient.Do(req)
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

func setAPIHeaders(req *http.Request, token, accept string) {
	req.Header.Set("Accept", accept)
	if token = strings.TrimSpace(token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func sanitizeTitle(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	value = badFilenameChars.ReplaceAllString(strings.TrimSpace(value), " ")
	value = whitespace.ReplaceAllString(value, " ")
	value = truncateRunes(strings.Trim(value, " ."), maxTitleLength)
	value = strings.Trim(value, " .")
	if value == "" {
		return "Untitled Video"
	}
	return value
}

func transcriptSlugFromSummary(summary, title string) string {
	match := transcriptRefRE.FindStringSubmatch(summary)
	if len(match) > 1 {
		if slug := strings.TrimSpace(match[1]); slug != "" {
			return safeSlug(slug, "transcript", maxSlugLength)
		}
	}
	return slugify(title)
}

func slugify(value string) string {
	return safeSlug(value, "transcript", maxSlugLength)
}

func safeSlug(value, fallback string, maxLength int) string {
	slug := slugChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-")
	slug = strings.Trim(slug, "-")
	slug = truncateRunes(slug, maxLength)
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return fallback
	}
	return slug
}

func truncateRunes(value string, maxLength int) string {
	if maxLength <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxLength {
		return value
	}
	return string(runes[:maxLength])
}

func uniquePath(path, jobID string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(filepath.Base(path), ext)
	suffix := safeSlug(jobID, "job", maxJobSuffixLength)
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
