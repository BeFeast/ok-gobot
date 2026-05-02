package youtubekaraoke

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunWritesKaraokeLRCArtifact(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "karaoke")
	now := time.Date(2026, 5, 2, 15, 4, 5, 0, time.UTC)
	var commands []string
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		if len(args) > 0 && args[0] == "--dump-single-json" {
			return json.Marshal(map[string]string{
				"id":          "abc123",
				"title":       `My / Karaoke: Song?`,
				"webpage_url": "https://www.youtube.com/watch?v=abc123",
			})
		}
		outputTemplate := argAfter(args, "--output")
		if outputTemplate == "" {
			t.Fatalf("subtitle command missing --output: %#v", args)
		}
		vttPath := strings.TrimSuffix(outputTemplate, ".%(ext)s") + ".en.vtt"
		if err := os.MkdirAll(filepath.Dir(vttPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(vttPath, []byte(sampleVTT), 0o644); err != nil {
			t.Fatalf("write vtt: %v", err)
		}
		return nil, nil
	}

	result, err := Run(t.Context(), "https://youtu.be/abc123", Config{
		OutputDir:     outputDir,
		YTDLPPath:     "yt-dlp-test",
		SubtitleLangs: "en,ru",
		Timeout:       time.Second,
		RunCommand:    runner,
		Now:           func() time.Time { return now },
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Title != "My / Karaoke: Song?" {
		t.Fatalf("Title = %q", result.Title)
	}
	if result.LineCount != 2 {
		t.Fatalf("LineCount = %d, want 2", result.LineCount)
	}
	if filepath.Base(result.LRCPath) != "My Karaoke Song - abc123.lrc" {
		t.Fatalf("LRCPath = %q", result.LRCPath)
	}
	data, err := os.ReadFile(result.LRCPath)
	if err != nil {
		t.Fatalf("read LRC: %v", err)
	}
	lrc := string(data)
	for _, want := range []string{"[ti:My / Karaoke: Song?]", "[00:01.20]Hello world", "[00:04.50]Second line & more"} {
		if !strings.Contains(lrc, want) {
			t.Fatalf("expected %q in LRC:\n%s", want, lrc)
		}
	}
	if len(commands) != 2 {
		t.Fatalf("commands = %#v, want metadata and subtitle calls", commands)
	}
	if !strings.Contains(commands[1], "--write-auto-subs") || !strings.Contains(commands[1], "--sub-langs en,ru") {
		t.Fatalf("subtitle command did not request captions: %q", commands[1])
	}
}

func TestVTTToLRCRejectsEmptyCues(t *testing.T) {
	_, _, err := VTTToLRC("title", "https://youtu.be/abc", "WEBVTT\n\nNOTE no cues")
	if err == nil {
		t.Fatal("expected empty cue error")
	}
}

func TestValidateYouTubeURLRejectsNonYouTube(t *testing.T) {
	if err := ValidateYouTubeURL("https://example.com/watch?v=abc"); err == nil {
		t.Fatal("expected non-YouTube URL to fail")
	}
	if err := ValidateYouTubeURL("https://www.youtube.com/watch?v=abc"); err != nil {
		t.Fatalf("expected YouTube URL to pass: %v", err)
	}
}

func argAfter(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

const sampleVTT = `WEBVTT

00:00:01.200 --> 00:00:03.000 align:start position:0%
<c>Hello</c> <00:00:01.900>world

00:00:01.200 --> 00:00:03.000 align:start position:0%
Hello world

00:00:04.500 --> 00:00:06.000
Second line &amp; more
`
