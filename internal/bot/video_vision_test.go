package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image/png"
	"math"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/ai"
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
	for _, want := range []string{"silent video", "1.5s, 9.5s", "Do not invent speech", "Reply in Russian", "demo caption"} {
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

func TestAnalyzeSilentVideoStatelessUsesExactIsolatedEnvelope(t *testing.T) {
	t.Parallel()

	_, blocks := buildSilentVideoVisionContent([]sampledVideoFrame{
		{TimeSecond: 1.5, PNG: []byte("first")},
		{TimeSecond: 9.5, PNG: []byte("second")},
	}, 10, "Please answer in English.")
	client := &silentVideoRecordingClient{response: "Grounded result"}

	got, err := analyzeSilentVideoStateless(context.Background(), client, blocks)
	if err != nil {
		t.Fatalf("analyzeSilentVideoStateless: %v", err)
	}
	if got != "Grounded result" {
		t.Fatalf("result = %q", got)
	}

	wantMessages := []ai.ChatMessage{
		{Role: ai.RoleSystem, Content: silentVideoVisionSystemPrompt},
		{Role: ai.RoleUser, ContentBlocks: blocks},
	}
	if !reflect.DeepEqual(client.messages, wantMessages) {
		t.Fatalf("request messages = %#v, want exact stateless envelope %#v", client.messages, wantMessages)
	}
	if len(client.tools) != 0 {
		t.Fatalf("tools = %#v, want none", client.tools)
	}
	if len(client.messages) != 2 {
		t.Fatalf("message count = %d, want system + current video only", len(client.messages))
	}
	for _, forbidden := range []string{"SOUL.md", "conversation history", "Active Memory result", "workshop-host", "memory_search"} {
		if strings.Contains(client.messages[1].Content, forbidden) {
			t.Fatalf("injected context %q found in current request", forbidden)
		}
	}
}

func TestAnalyzeSilentVideoStatelessRejectsToolCalls(t *testing.T) {
	t.Parallel()

	client := &silentVideoRecordingClient{toolCall: true}
	_, err := analyzeSilentVideoStateless(context.Background(), client, []ai.ContentBlock{{Type: "text", Text: "current video"}})
	if err == nil || !strings.Contains(err.Error(), "unexpected tool call") {
		t.Fatalf("error = %v, want unexpected tool call", err)
	}
	if len(client.tools) != 0 {
		t.Fatalf("tools = %#v, want none", client.tools)
	}
}

func TestRunSilentVideoVisionAsyncDeliversTerminalSuccess(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	client := &silentVideoRecordingClient{response: "Только наблюдаемый результат"}
	bot := newSilentVideoDeliveryTestBot(t, tg, client)
	chat := &telebot.Chat{ID: 77, Type: telebot.ChatPrivate}

	if ack := bot.sendImmediateAck(chat, 12); ack == nil {
		t.Fatal("sendImmediateAck returned nil")
	}
	bot.runSilentVideoVisionAsync(context.Background(), telegramDelivery{Chat: chat}, []ai.ContentBlock{{Type: "text", Text: "current video"}})

	completed := tg.waitForText(t, "✅ Done", 2*time.Second)
	result := tg.waitForText(t, "Только наблюдаемый результат", 2*time.Second)
	if completed.Method != "editMessageText" {
		t.Fatalf("completion method = %q, want editMessageText", completed.Method)
	}
	if result.Method != "sendRichMessage" {
		t.Fatalf("result method = %q, want sendRichMessage", result.Method)
	}
	if bot.ackManager.Peek(chat.ID) != nil {
		t.Fatal("ack handle was not consumed after terminal success")
	}
}

func TestRunSilentVideoVisionAsyncDeliversTerminalFailure(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	client := &silentVideoRecordingClient{err: errors.New("provider failed")}
	bot := newSilentVideoDeliveryTestBot(t, tg, client)
	chat := &telebot.Chat{ID: 88, Type: telebot.ChatPrivate}

	if ack := bot.sendImmediateAck(chat, 13); ack == nil {
		t.Fatal("sendImmediateAck returned nil")
	}
	bot.runSilentVideoVisionAsync(context.Background(), telegramDelivery{Chat: chat}, []ai.ContentBlock{{Type: "text", Text: "current video"}})

	failed := tg.waitForText(t, silentVideoVisionFailureDetail, 2*time.Second)
	if failed.Method != "editMessageText" || !strings.Contains(failed.Text, "❌ Something went wrong") {
		t.Fatalf("failure request = %+v, want terminal failed edit", failed)
	}
	if tg.hasText("Только наблюдаемый результат") {
		t.Fatal("result was delivered after model failure")
	}
	if bot.ackManager.Peek(chat.ID) != nil {
		t.Fatal("ack handle was not consumed after terminal failure")
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

type silentVideoRecordingClient struct {
	messages []ai.ChatMessage
	tools    []ai.ToolDefinition
	response string
	err      error
	toolCall bool
}

func (c *silentVideoRecordingClient) Complete(context.Context, []ai.Message) (string, error) {
	return "", errors.New("legacy Complete must not be used for silent-video vision")
}

func (c *silentVideoRecordingClient) CompleteWithTools(_ context.Context, messages []ai.ChatMessage, tools []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	c.messages = append([]ai.ChatMessage(nil), messages...)
	c.tools = append([]ai.ToolDefinition(nil), tools...)
	if c.err != nil {
		return nil, c.err
	}
	response := silentVideoAIResponse(c.response)
	if c.toolCall {
		response.Choices[0].Message.ToolCalls = []ai.ToolCall{{
			ID:   "unexpected",
			Type: "function",
			Function: ai.FunctionCall{
				Name:      "memory_search",
				Arguments: `{}`,
			},
		}}
	}
	return response, nil
}

func (c *silentVideoRecordingClient) SupportsVision() bool { return true }

func silentVideoAIResponse(content string) *ai.ChatCompletionResponse {
	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{
			{
				Message:      ai.ChatMessage{Role: ai.RoleAssistant, Content: content},
				FinishReason: "stop",
			},
		},
	}
}

func newSilentVideoDeliveryTestBot(t *testing.T, tg *fakeTelegramAPI, client ai.Client) *Bot {
	t.Helper()
	api, err := telebot.NewBot(telebot.Settings{
		Token:   "TEST",
		URL:     tg.server.URL,
		Client:  tg.server.Client(),
		Offline: true,
	})
	if err != nil {
		t.Fatalf("telebot.NewBot: %v", err)
	}
	api.Me = &telebot.User{ID: 1, Username: "okgobot", IsBot: true}
	return &Bot{
		api:        api,
		ai:         client,
		ackManager: NewAckHandleManager(),
	}
}
