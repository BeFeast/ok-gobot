package youtubekaraoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	"ok-gobot/internal/videosummary"
)

const (
	defaultOutputDir    = "~/.ok-gobot/youtube-karaoke"
	defaultPollInterval = 5 * time.Second
	defaultTimeout      = 2 * time.Hour
)

var (
	badFilenameChars = regexp.MustCompile(`[\\/:*?"<>|]`)
	whitespace       = regexp.MustCompile(`\s+`)
	artifactNames    = []string{"karaoke.mp3", "vocals.mp3", "lyrics.lrc", "lyrics.txt", "metadata.json"}
)

// Config controls the native Karaoke service adapter.
type Config struct {
	BaseURL      string
	APIToken     string
	OutputDir    string
	PollInterval time.Duration
	Timeout      time.Duration
	HTTPClient   *http.Client
	Now          func() time.Time
}

// Result is the downloaded Karaoke service outcome.
type Result struct {
	Title          string
	JobID          string
	JobToken       string
	SourceURL      string
	ShareURL       string
	Status         string
	OutputDir      string
	KaraokePath    string
	VocalsPath     string
	LyricsLRCPath  string
	LyricsTextPath string
	MetadataPath   string
	GeneratedAt    time.Time
}

// PrimaryArtifactPath returns the artifact that should be delivered first.
func (r Result) PrimaryArtifactPath() string {
	for _, path := range []string{r.KaraokePath, r.LyricsLRCPath, r.LyricsTextPath, r.VocalsPath} {
		if strings.TrimSpace(path) != "" {
			return path
		}
	}
	return ""
}

// ArtifactPaths returns every downloaded artifact keyed by service filename.
func (r Result) ArtifactPaths() map[string]string {
	return map[string]string{
		"karaoke.mp3":   r.KaraokePath,
		"vocals.mp3":    r.VocalsPath,
		"lyrics.lrc":    r.LyricsLRCPath,
		"lyrics.txt":    r.LyricsTextPath,
		"metadata.json": r.MetadataPath,
	}
}

// ValidateYouTubeURL accepts normal YouTube watch URLs and youtu.be short links.
func ValidateYouTubeURL(raw string) error {
	return videosummary.ValidateYouTubeURL(raw)
}

// Run submits a URL to the Karaoke service, polls the owner-scoped status
// endpoint, and downloads the completed artifacts through the unlisted share
// token. It does not run yt-dlp or GPU work locally.
func Run(ctx context.Context, rawURL string, cfg Config, progress func(string)) (Result, error) {
	if err := ValidateYouTubeURL(rawURL); err != nil {
		return Result{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = cfg.withDefaults()
	if cfg.BaseURL == "" {
		return Result{}, errors.New("karaoke service URL is not configured")
	}
	if cfg.OutputDir == "" {
		return Result{}, errors.New("karaoke output directory is not configured")
	}

	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	job, err := submit(runCtx, rawURL, cfg)
	if err != nil {
		return Result{}, err
	}
	if progress != nil {
		progress(fmt.Sprintf("karaoke job accepted: %d", job.ID))
	}

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	lastStatus := ""
	for {
		job, err = fetchStatus(runCtx, job.ID, cfg)
		if err != nil {
			return Result{}, err
		}
		status := strings.ToLower(strings.TrimSpace(job.Status))
		if progress != nil && status != "" && status != lastStatus {
			progress(fmt.Sprintf("karaoke status: %s (%d%%)", status, job.Progress))
			lastStatus = status
		}

		switch status {
		case "completed", "done":
			return downloadResult(runCtx, rawURL, job, cfg, progress)
		case "failed", "cancelled", "canceled", "error":
			reason := strings.TrimSpace(job.Error)
			if reason == "" {
				reason = strings.TrimSpace(job.StageNote)
			}
			if reason == "" {
				reason = "no error detail returned"
			}
			return Result{}, fmt.Errorf("karaoke job ended with status %q: %s", status, reason)
		}

		select {
		case <-runCtx.Done():
			return Result{}, fmt.Errorf("karaoke job timed out after %s while status=%q", cfg.Timeout, status)
		case <-ticker.C:
		}
	}
}

type jobView struct {
	ID        int            `json:"id"`
	JobToken  string         `json:"job_token"`
	SourceURL string         `json:"source_url"`
	Title     string         `json:"title"`
	Status    string         `json:"status"`
	Progress  int            `json:"progress"`
	StageNote string         `json:"stage_note"`
	Error     string         `json:"error"`
	ShareURL  string         `json:"share_url"`
	Artifacts []artifactView `json:"artifacts"`
}

type artifactView struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func submit(ctx context.Context, rawURL string, cfg Config) (jobView, error) {
	body, err := json.Marshal(map[string]string{"url": rawURL})
	if err != nil {
		return jobView{}, err
	}
	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/jobs"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return jobView{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	setAPIHeaders(req, cfg.APIToken)
	return executeJobRequest(cfg.HTTPClient, req, "karaoke submit")
}

func fetchStatus(ctx context.Context, jobID int, cfg Config) (jobView, error) {
	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/jobs/" + strconv.Itoa(jobID) + "/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return jobView{}, err
	}
	setAPIHeaders(req, cfg.APIToken)
	return executeJobRequest(cfg.HTTPClient, req, "karaoke status")
}

func executeJobRequest(client *http.Client, req *http.Request, operation string) (jobView, error) {
	resp, err := client.Do(req)
	if err != nil {
		return jobView{}, fmt.Errorf("%s failed: %w", operation, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return jobView{}, fmt.Errorf("%s failed: HTTP %d: %s", operation, resp.StatusCode, limitedBody(resp.Body))
	}
	var job jobView
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return jobView{}, fmt.Errorf("decode %s response: %w", operation, err)
	}
	if job.ID <= 0 || strings.TrimSpace(job.JobToken) == "" {
		return jobView{}, fmt.Errorf("%s returned incomplete job identity", operation)
	}
	return job, nil
}

func downloadResult(ctx context.Context, rawURL string, job jobView, cfg Config, progress func(string)) (Result, error) {
	started := cfg.now()
	title := sanitizeTitle(firstNonEmpty(job.Title, "YouTube Karaoke"))
	jobDir := filepath.Join(cfg.OutputDir, started.Format("2006-01-02"), title+" - "+strconv.Itoa(job.ID))
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create karaoke output directory: %w", err)
	}

	available := make(map[string]bool, len(job.Artifacts))
	for _, artifact := range job.Artifacts {
		name := filepath.Base(strings.TrimSpace(artifact.Name))
		available[name] = true
		if artifact.Kind == "lyrics_lrc" {
			available["lyrics.lrc"] = true
		}
	}

	paths := make(map[string]string)
	for _, name := range artifactNames {
		if !available[name] {
			continue
		}
		path := filepath.Join(jobDir, name)
		artifactURL := strings.TrimRight(cfg.BaseURL, "/") + "/share/" + url.PathEscape(job.JobToken) + "/" + url.PathEscape(name)
		if err := downloadArtifact(ctx, cfg.HTTPClient, artifactURL, path); err != nil {
			return Result{}, fmt.Errorf("download karaoke artifact %s: %w", name, err)
		}
		paths[name] = path
		if progress != nil {
			progress("karaoke artifact downloaded: " + name)
		}
	}
	if paths["karaoke.mp3"] == "" {
		return Result{}, errors.New("karaoke completed but karaoke.mp3 is missing")
	}

	return Result{
		Title:          title,
		JobID:          strconv.Itoa(job.ID),
		JobToken:       job.JobToken,
		SourceURL:      firstNonEmpty(job.SourceURL, rawURL),
		ShareURL:       firstNonEmpty(job.ShareURL, strings.TrimRight(cfg.BaseURL, "/")+"/share/"+url.PathEscape(job.JobToken)),
		Status:         job.Status,
		OutputDir:      jobDir,
		KaraokePath:    paths["karaoke.mp3"],
		VocalsPath:     paths["vocals.mp3"],
		LyricsLRCPath:  paths["lyrics.lrc"],
		LyricsTextPath: paths["lyrics.txt"],
		MetadataPath:   paths["metadata.json"],
		GeneratedAt:    started,
	}, nil
}

func downloadArtifact(ctx context.Context, client *http.Client, rawURL, target string) error {
	if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, limitedBody(resp.Body))
	}
	part := target + ".part"
	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(part)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(part)
		return closeErr
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(part)
		return err
	}
	if err := os.Rename(part, target); err != nil {
		_ = os.Remove(part)
		return err
	}
	return nil
}

func (cfg Config) withDefaults() Config {
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIToken = strings.TrimSpace(cfg.APIToken)
	cfg.OutputDir = expandPath(strings.TrimSpace(cfg.OutputDir))
	if cfg.OutputDir == "" {
		cfg.OutputDir = expandPath(defaultOutputDir)
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	return cfg
}

func (cfg Config) now() time.Time {
	if cfg.Now != nil {
		return cfg.Now()
	}
	return time.Now()
}

func setAPIHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/json")
	if token = strings.TrimSpace(token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func limitedBody(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, 4096))
	return strings.TrimSpace(string(data))
}

func sanitizeTitle(value string) string {
	value = badFilenameChars.ReplaceAllString(strings.TrimSpace(value), " ")
	value = whitespace.ReplaceAllString(value, " ")
	value = strings.TrimRight(strings.TrimSpace(value), ".")
	if len([]rune(value)) > 120 {
		value = strings.TrimSpace(string([]rune(value)[:120]))
	}
	if value == "" {
		return "YouTube Karaoke"
	}
	return value
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
