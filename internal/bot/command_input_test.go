package bot

import (
	"testing"
	"time"

	"gopkg.in/telebot.v4"
)

func TestVideoSummaryBareCommandStartsGuidedForceReply(t *testing.T) {
	b := &Bot{}
	ctx := newCommandInputTestContext("/video_summary", "")

	if err := b.handleVideoSummaryCommand(ctx); err != nil {
		t.Fatalf("handleVideoSummaryCommand: %v", err)
	}
	assertCommandInputPrompt(t, ctx, "Send the video URL to summarize.", "Paste a video URL")

	key, _ := commandInputKeyForContext(ctx)
	pending, ok := b.takePendingCommandInput(key, time.Now())
	if !ok || pending.kind != commandInputVideoSummary {
		t.Fatalf("pending input = %+v, %v; want video summary", pending, ok)
	}
}

func TestYouTubeKaraokeBareCommandStartsGuidedForceReply(t *testing.T) {
	b := &Bot{}
	ctx := newCommandInputTestContext("/youtube_karaoke", "")

	if err := b.handleYouTubeKaraokeCommand(ctx); err != nil {
		t.Fatalf("handleYouTubeKaraokeCommand: %v", err)
	}
	assertCommandInputPrompt(t, ctx, "Send the YouTube URL for karaoke.", "Paste a YouTube URL")

	key, _ := commandInputKeyForContext(ctx)
	pending, ok := b.takePendingCommandInput(key, time.Now())
	if !ok || pending.kind != commandInputYouTubeKaraoke {
		t.Fatalf("pending input = %+v, %v; want YouTube karaoke", pending, ok)
	}
}

func TestPendingCommandInputInvalidURLPromptsAgain(t *testing.T) {
	b := &Bot{}
	ctx := newCommandInputTestContext("not a YouTube URL", "")
	key, _ := commandInputKeyForContext(ctx)
	b.pendingCommandInputs = map[commandInputKey]pendingCommandInput{
		key: {kind: commandInputVideoSummary, expiresAt: time.Now().Add(time.Minute)},
	}

	handled, err := b.handlePendingCommandInput(ctx)
	if err != nil {
		t.Fatalf("handlePendingCommandInput: %v", err)
	}
	if !handled {
		t.Fatal("pending input was not handled")
	}
	assertCommandInputPrompt(t, ctx, "Send the video URL to summarize.", "Paste a video URL")
	if _, ok := b.takePendingCommandInput(key, time.Now()); !ok {
		t.Fatal("invalid URL should leave a fresh pending prompt")
	}
}

func TestPendingCommandInputRoutesValidURLToOriginalHandler(t *testing.T) {
	tests := []struct {
		name     string
		kind     commandInputKind
		wantText string
	}{
		{
			name:     "video summary",
			kind:     commandInputVideoSummary,
			wantText: "Video summary runtime is not available.",
		},
		{
			name:     "YouTube karaoke",
			kind:     commandInputYouTubeKaraoke,
			wantText: "YouTube karaoke runtime is not available.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Bot{}
			ctx := newCommandInputTestContext("https://youtu.be/dQw4w9WgXcQ", "")
			key, _ := commandInputKeyForContext(ctx)
			b.pendingCommandInputs = map[commandInputKey]pendingCommandInput{
				key: {kind: tt.kind, expiresAt: time.Now().Add(time.Minute)},
			}

			handled, err := b.handlePendingCommandInput(ctx)
			if err != nil {
				t.Fatalf("handlePendingCommandInput: %v", err)
			}
			if !handled {
				t.Fatal("pending input was not handled")
			}
			if len(ctx.sent) != 1 || ctx.sent[0] != tt.wantText {
				t.Fatalf("sent = %#v, want %q", ctx.sent, tt.wantText)
			}
			if ctx.msg.Payload != "" {
				t.Fatalf("message payload was not restored: %q", ctx.msg.Payload)
			}
		})
	}
}

func TestPendingCommandInputIsScopedAndExpires(t *testing.T) {
	now := time.Now()
	b := &Bot{pendingCommandInputs: map[commandInputKey]pendingCommandInput{
		{chatID: 1, userID: 2}: {kind: commandInputVideoSummary, expiresAt: now.Add(time.Minute)},
		{chatID: 1, userID: 3}: {kind: commandInputYouTubeKaraoke, expiresAt: now.Add(-time.Second)},
	}}

	if _, ok := b.takePendingCommandInput(commandInputKey{chatID: 1, userID: 4}, now); ok {
		t.Fatal("different user consumed pending input")
	}
	if pending, ok := b.takePendingCommandInput(commandInputKey{chatID: 1, userID: 2}, now); !ok || pending.kind != commandInputVideoSummary {
		t.Fatalf("valid pending input = %+v, %v", pending, ok)
	}
	if _, ok := b.takePendingCommandInput(commandInputKey{chatID: 1, userID: 3}, now); ok {
		t.Fatal("expired pending input was consumed")
	}
}

func newCommandInputTestContext(text, payload string) *fakeContext {
	return &fakeContext{msg: &telebot.Message{
		ID:      42,
		Text:    text,
		Payload: payload,
		Chat:    &telebot.Chat{ID: 123, Type: telebot.ChatPrivate},
		Sender:  &telebot.User{ID: 456, Username: "tester"},
	}}
}

func assertCommandInputPrompt(t *testing.T, ctx *fakeContext, wantText, wantPlaceholder string) {
	t.Helper()
	if len(ctx.sent) != 1 || ctx.sent[0] != wantText {
		t.Fatalf("sent = %#v, want %q", ctx.sent, wantText)
	}
	if len(ctx.sentOpts) != 1 || len(ctx.sentOpts[0]) != 1 {
		t.Fatalf("send options = %#v", ctx.sentOpts)
	}
	opts, ok := ctx.sentOpts[0][0].(*telebot.SendOptions)
	if !ok {
		t.Fatalf("send option type = %T, want *telebot.SendOptions", ctx.sentOpts[0][0])
	}
	if opts.ReplyTo != ctx.msg {
		t.Fatal("ForceReply prompt is not a reply to the command message")
	}
	if opts.ReplyMarkup == nil || !opts.ReplyMarkup.ForceReply || !opts.ReplyMarkup.Selective {
		t.Fatalf("reply markup = %+v", opts.ReplyMarkup)
	}
	if opts.ReplyMarkup.Placeholder != wantPlaceholder {
		t.Fatalf("placeholder = %q", opts.ReplyMarkup.Placeholder)
	}
}
