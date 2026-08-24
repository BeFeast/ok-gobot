package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/png"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseForwardedVideoProbeRoutesByAudioStream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		streams   string
		wantAudio bool
		wantRoute forwardedVideoRoute
	}{
		{
			name:      "video only uses vision",
			streams:   `[{"codec_type":"video"}]`,
			wantAudio: false,
			wantRoute: forwardedVideoRouteVision,
		},
		{
			name:      "audio video uses scribe",
			streams:   `[{"codec_type":"video"},{"codec_type":"audio"}]`,
			wantAudio: true,
			wantRoute: forwardedVideoRouteScribe,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			probe, err := parseForwardedVideoProbe([]byte(
				`{"streams":` + tt.streams + `,"format":{"duration":"32.866667"}}`,
			))
			if err != nil {
				t.Fatalf("parseForwardedVideoProbe: %v", err)
			}
			if !probe.HasVideo || probe.HasAudio != tt.wantAudio {
				t.Fatalf("probe = %+v, want video=true audio=%v", probe, tt.wantAudio)
			}
			if got := routeForwardedVideo(probe); got != tt.wantRoute {
				t.Fatalf("route = %q, want %q", got, tt.wantRoute)
			}
		})
	}
}

func TestParseForwardedVideoProbeRejectsMissingVideoOrDuration(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		`{"streams":[{"codec_type":"audio"}],"format":{"duration":"10"}}`,
		`{"streams":[{"codec_type":"video"}],"format":{"duration":"N/A"}}`,
	} {
		if _, err := parseForwardedVideoProbe([]byte(input)); err == nil {
			t.Fatalf("expected error for %s", input)
		}
	}
}

func TestSilentVideoSampleTimesCoverWholeDuration(t *testing.T) {
	t.Parallel()

	const duration = 32.866667
	times, err := silentVideoSampleTimes(duration, silentVideoFrameCount)
	if err != nil {
		t.Fatalf("silentVideoSampleTimes: %v", err)
	}
	if len(times) != silentVideoFrameCount {
		t.Fatalf("sample count = %d, want %d", len(times), silentVideoFrameCount)
	}
	if math.Abs(times[0]-duration/18) > 0.0001 {
		t.Fatalf("first sample = %.6f, want first bucket centre", times[0])
	}
	if math.Abs(times[len(times)-1]-duration*17/18) > 0.0001 {
		t.Fatalf("last sample = %.6f, want last bucket centre", times[len(times)-1])
	}
	for i := 1; i < len(times); i++ {
		if times[i] <= times[i-1] {
			t.Fatalf("samples not strictly ordered: %v", times)
		}
	}
}

func TestBuildSilentVideoVisionContent(t *testing.T) {
	t.Parallel()

	frames := []sampledVideoFrame{
		{TimeSecond: 1.5, PNG: []byte("first")},
		{TimeSecond: 9.5, PNG: []byte("second")},
	}
	content, blocks := buildSilentVideoVisionContent(frames, 10, "demo caption")
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want text + 2 images", len(blocks))
	}
	for _, want := range []string{"silent video", "1.5s, 9.5s", "Do not invent speech", "demo caption"} {
		if !strings.Contains(content, want) {
			t.Fatalf("content missing %q: %s", want, content)
		}
	}
	if blocks[0].Type != "text" || blocks[0].Text != content {
		t.Fatalf("first block is not the visual-summary prompt: %+v", blocks[0])
	}
	for i, want := range []string{"first", "second"} {
		block := blocks[i+1]
		if block.Type != "image" || block.Source == nil || block.Source.MediaType != "image/png" {
			t.Fatalf("image block %d malformed: %+v", i, block)
		}
		decoded, err := base64.StdEncoding.DecodeString(block.Source.Data)
		if err != nil || string(decoded) != want {
			t.Fatalf("image block %d payload = %q, err=%v", i, decoded, err)
		}
	}
}

func TestSampleSilentVideoFramesRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}

	src := filepath.Join(t.TempDir(), "silent.mp4")
	cmd := exec.Command(
		"ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=12:duration=1",
		"-an", "-pix_fmt", "yuv420p", src,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate silent video: %v: %s", err, output)
	}

	probe, err := probeForwardedVideo(context.Background(), src)
	if err != nil {
		t.Fatalf("probeForwardedVideo: %v", err)
	}
	if probe.HasAudio || routeForwardedVideo(probe) != forwardedVideoRouteVision {
		t.Fatalf("probe = %+v, want video-only vision route", probe)
	}

	frames, err := sampleSilentVideoFrames(context.Background(), src, probe.DurationSecond)
	if err != nil {
		t.Fatalf("sampleSilentVideoFrames: %v", err)
	}
	if len(frames) != silentVideoFrameCount {
		t.Fatalf("frames = %d, want %d", len(frames), silentVideoFrameCount)
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(src), ".ok-gobot-video-frame-*.png"))
	if err != nil {
		t.Fatalf("glob frame leftovers: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary frames were not cleaned up: %v", leftovers)
	}
	for i, frame := range frames {
		image, err := png.Decode(bytes.NewReader(frame.PNG))
		if err != nil {
			t.Fatalf("decode frame %d: %v", i, err)
		}
		bounds := image.Bounds()
		if bounds.Dx() > 960 || bounds.Dy() > 960 {
			t.Fatalf("frame %d dimensions = %s, max 960x960", i, bounds)
		}
		if i > 0 && frame.TimeSecond <= frames[i-1].TimeSecond {
			t.Fatalf("frame timestamps not ordered: %+v", frames)
		}
	}
}
