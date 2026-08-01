package youtubekaraoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"ok-gobot/internal/videosummary"
)

const (
	defaultOutputDir     = "~/.ok-gobot/youtube-karaoke"
	defaultYTDLPPath     = "yt-dlp"
	defaultSubtitleLangs = "en.*,en"
	defaultTimeout       = 30 * time.Minute
)

var (
	badFilenameChars = regexp.MustCompile(`[\\/:*?"<>|]`)
	whitespace       = regexp.MustCompile(`\s+`)
	vttTimeLineRE    = regexp.MustCompile(`^\s*(\d{1,2}:\d{2}:\d{2}\.\d{3}|\d{1,2}:\d{2}\.\d{3})\s+-->\s+`)
	vttTimestampTag  = regexp.MustCompile(`<\d{1,2}:\d{2}(?::\d{2})?\.\d{3}>`)
	htmlTag          = regexp.MustCompile(`<[^>]+>`)
)

// CommandRunner executes one external command and returns stdout.
type CommandRunner func(context.Context, string, ...string) ([]byte, error)

// Config controls the native YouTube karaoke workflow.
type Config struct {
	OutputDir     string
	YTDLPPath     string
	SubtitleLangs string
	Timeout       time.Duration
	RunCommand    CommandRunner
	Now           func() time.Time
}

// Result is the generated karaoke artifact outcome.
type Result struct {
	Title       string
	VideoID     string
	SourceURL   string
	LRCPath     string
	VTTPath     string
	LineCount   int
	GeneratedAt time.Time
}

type videoInfo struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	WebpageURL string `json:"webpage_url"`
}

// ValidateYouTubeURL accepts normal YouTube watch URLs and youtu.be short links.
func ValidateYouTubeURL(raw string) error {
	return videosummary.ValidateYouTubeURL(raw)
}

// Run downloads YouTube subtitles with yt-dlp and converts them to an LRC
// karaoke artifact. It does not invoke OpenClaw runtime or skill execution.
func Run(ctx context.Context, rawURL string, cfg Config, progress func(string)) (Result, error) {
	if err := ValidateYouTubeURL(rawURL); err != nil {
		return Result{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = cfg.withDefaults()
	started := cfg.now()
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create karaoke output directory: %w", err)
	}

	runCtx := ctx
	cancel := func() {}
	if cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer cancel()

	info, err := fetchVideoInfo(runCtx, rawURL, cfg)
	if err != nil {
		return Result{}, err
	}
	if progress != nil {
		progress(fmt.Sprintf("yt-dlp metadata loaded: %s", firstNonEmpty(info.Title, info.ID)))
	}

	dayDir := filepath.Join(cfg.OutputDir, started.Format("2006-01-02"))
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create karaoke day directory: %w", err)
	}
	baseName := karaokeBaseName(info.Title, info.ID)
	outputTemplate := filepath.Join(dayDir, baseName+".%(ext)s")
	if err := downloadSubtitles(runCtx, rawURL, outputTemplate, cfg); err != nil {
		return Result{}, err
	}
	if progress != nil {
		progress("yt-dlp subtitle download completed")
	}

	vttPath, err := findSubtitleFile(dayDir, baseName)
	if err != nil {
		return Result{}, err
	}
	vttData, err := os.ReadFile(vttPath)
	if err != nil {
		return Result{}, fmt.Errorf("read VTT subtitle: %w", err)
	}
	lrc, lineCount, err := VTTToLRC(info.Title, firstNonEmpty(info.WebpageURL, rawURL), string(vttData))
	if err != nil {
		return Result{}, err
	}

	lrcPath := uniquePath(filepath.Join(dayDir, baseName+".lrc"), started)
	if err := os.WriteFile(lrcPath, []byte(lrc), 0o644); err != nil {
		return Result{}, fmt.Errorf("write LRC artifact: %w", err)
	}

	return Result{
		Title:       firstNonEmpty(info.Title, "Untitled video"),
		VideoID:     strings.TrimSpace(info.ID),
		SourceURL:   firstNonEmpty(info.WebpageURL, rawURL),
		LRCPath:     lrcPath,
		VTTPath:     vttPath,
		LineCount:   lineCount,
		GeneratedAt: started,
	}, nil
}

// VTTToLRC converts WebVTT subtitle cues into timestamped LRC lyrics.
func VTTToLRC(title, sourceURL, vtt string) (string, int, error) {
	var out []string
	out = append(out,
		"[ti:"+lrcHeaderValue(firstNonEmpty(title, "Untitled video"))+"]",
		"[re:ok-gobot youtube_karaoke]",
		"[ve:1]",
	)
	if source := strings.TrimSpace(sourceURL); source != "" {
		out = append(out, "[url:"+lrcHeaderValue(source)+"]")
	}
	out = append(out, "")

	var (
		cueStart time.Duration
		cueLines []string
		inCue    bool
		lastText string
		count    int
	)
	flushCue := func() {
		if !inCue {
			cueLines = nil
			return
		}
		text := cleanCueText(strings.Join(cueLines, " "))
		if text != "" && text != lastText {
			out = append(out, formatLRCTime(cueStart)+text)
			lastText = text
			count++
		}
		cueLines = nil
		inCue = false
	}

	for _, rawLine := range strings.Split(strings.ReplaceAll(vtt, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			flushCue()
			continue
		}
		if strings.HasPrefix(line, "WEBVTT") || strings.HasPrefix(line, "NOTE") || strings.HasPrefix(line, "STYLE") || strings.HasPrefix(line, "REGION") {
			continue
		}
		if match := vttTimeLineRE.FindStringSubmatch(line); len(match) == 2 {
			flushCue()
			start, err := parseVTTTime(match[1])
			if err != nil {
				return "", 0, err
			}
			cueStart = start
			inCue = true
			continue
		}
		if inCue {
			cueLines = append(cueLines, line)
		}
	}
	flushCue()

	if count == 0 {
		return "", 0, errors.New("subtitle file did not contain karaoke cues")
	}
	return strings.Join(out, "\n") + "\n", count, nil
}

func (cfg Config) withDefaults() Config {
	cfg.OutputDir = expandPath(strings.TrimSpace(cfg.OutputDir))
	if cfg.OutputDir == "" {
		cfg.OutputDir = expandPath(defaultOutputDir)
	}
	cfg.YTDLPPath = strings.TrimSpace(cfg.YTDLPPath)
	if cfg.YTDLPPath == "" {
		cfg.YTDLPPath = defaultYTDLPPath
	}
	cfg.SubtitleLangs = strings.TrimSpace(cfg.SubtitleLangs)
	if cfg.SubtitleLangs == "" {
		cfg.SubtitleLangs = defaultSubtitleLangs
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.RunCommand == nil {
		cfg.RunCommand = runCommand
	}
	return cfg
}

func (cfg Config) now() time.Time {
	if cfg.Now != nil {
		return cfg.Now()
	}
	return time.Now()
}

func fetchVideoInfo(ctx context.Context, rawURL string, cfg Config) (videoInfo, error) {
	out, err := cfg.RunCommand(ctx, cfg.YTDLPPath, "--dump-single-json", "--no-playlist", "--no-warnings", rawURL)
	if err != nil {
		return videoInfo{}, fmt.Errorf("yt-dlp metadata failed: %w", err)
	}
	var info videoInfo
	if err := json.Unmarshal(bytes.TrimSpace(out), &info); err != nil {
		return videoInfo{}, fmt.Errorf("decode yt-dlp metadata: %w", err)
	}
	info.ID = strings.TrimSpace(info.ID)
	info.Title = strings.TrimSpace(info.Title)
	info.WebpageURL = strings.TrimSpace(info.WebpageURL)
	if info.ID == "" && info.Title == "" {
		return videoInfo{}, errors.New("yt-dlp metadata did not include a video id or title")
	}
	return info, nil
}

func downloadSubtitles(ctx context.Context, rawURL, outputTemplate string, cfg Config) error {
	_, err := cfg.RunCommand(ctx, cfg.YTDLPPath,
		"--skip-download",
		"--write-subs",
		"--write-auto-subs",
		"--sub-langs", cfg.SubtitleLangs,
		"--sub-format", "vtt",
		"--no-playlist",
		"--no-warnings",
		"--output", outputTemplate,
		rawURL,
	)
	if err != nil {
		return fmt.Errorf("yt-dlp subtitle download failed: %w", err)
	}
	return nil
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, limited(stderr.String()))
	}
	return out, nil
}

func findSubtitleFile(dir, baseName string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("list subtitle output: %w", err)
	}
	prefix := baseName + "."
	var matches []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(lower, ".vtt") && !strings.Contains(lower, "live_chat") {
			matches = append(matches, filepath.Join(dir, name))
		}
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return "", fmt.Errorf("yt-dlp did not produce VTT subtitles for %q", baseName)
	}
	return matches[0], nil
}

func karaokeBaseName(title, id string) string {
	title = sanitizeTitle(title)
	id = sanitizeTitle(id)
	switch {
	case title != "" && id != "":
		return title + " - " + id
	case title != "":
		return title
	case id != "":
		return "YouTube Karaoke - " + id
	default:
		return "YouTube Karaoke"
	}
}

func sanitizeTitle(value string) string {
	value = badFilenameChars.ReplaceAllString(strings.TrimSpace(value), " ")
	value = whitespace.ReplaceAllString(value, " ")
	value = strings.TrimRight(strings.TrimSpace(value), ".")
	if len([]rune(value)) > 120 {
		runes := []rune(value)
		value = strings.TrimSpace(string(runes[:120]))
	}
	return value
}

func uniquePath(path string, now time.Time) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(filepath.Base(path), ext)
	suffix := now.Format("150405")
	for i := 0; i < 100; i++ {
		candidateSuffix := suffix
		if i > 0 {
			candidateSuffix = fmt.Sprintf("%s-%02d", suffix, i+1)
		}
		candidate := filepath.Join(filepath.Dir(path), stem+" - "+candidateSuffix+ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return filepath.Join(filepath.Dir(path), stem+" - "+fmt.Sprint(now.UnixNano())+ext)
}

func cleanCueText(text string) string {
	text = vttTimestampTag.ReplaceAllString(text, " ")
	text = htmlTag.ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	text = whitespace.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func parseVTTTime(value string) (time.Duration, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, fmt.Errorf("invalid VTT timestamp %q", value)
	}
	var hours, minutes int
	secondsPart := parts[len(parts)-1]
	if len(parts) == 3 {
		parsed, err := parseSmallInt(parts[0])
		if err != nil {
			return 0, fmt.Errorf("invalid VTT timestamp %q", value)
		}
		hours = parsed
	}
	parsedMinutes, err := parseSmallInt(parts[len(parts)-2])
	if err != nil {
		return 0, fmt.Errorf("invalid VTT timestamp %q", value)
	}
	minutes = parsedMinutes
	secParts := strings.Split(secondsPart, ".")
	if len(secParts) != 2 || len(secParts[1]) != 3 {
		return 0, fmt.Errorf("invalid VTT timestamp %q", value)
	}
	seconds, err := parseSmallInt(secParts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid VTT timestamp %q", value)
	}
	millis, err := parseSmallInt(secParts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid VTT timestamp %q", value)
	}
	return time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second + time.Duration(millis)*time.Millisecond, nil
}

func parseSmallInt(value string) (int, error) {
	var out int
	if value == "" {
		return 0, errors.New("empty integer")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid integer %q", value)
		}
		out = out*10 + int(r-'0')
	}
	return out, nil
}

func formatLRCTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalCentis := int(d / (10 * time.Millisecond))
	minutes := totalCentis / 100 / 60
	seconds := (totalCentis / 100) % 60
	centis := totalCentis % 100
	return fmt.Sprintf("[%02d:%02d.%02d]", minutes, seconds, centis)
}

func lrcHeaderValue(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "]", ")")
	value = strings.ReplaceAll(value, "\n", " ")
	return whitespace.ReplaceAllString(value, " ")
}

func limited(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4096 {
		return value
	}
	return value[:4096]
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
