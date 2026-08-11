package bot

import (
	"testing"

	"gopkg.in/telebot.v4"
)

func TestBareYouTubeURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "watch URL", input: "https://www.youtube.com/watch?v=abc123", want: "https://www.youtube.com/watch?v=abc123", ok: true},
		{name: "short URL with whitespace", input: "  https://youtu.be/abc123\n", want: "https://youtu.be/abc123", ok: true},
		{name: "URL with request text", input: "summarize https://youtu.be/abc123", ok: false},
		{name: "non YouTube URL", input: "https://example.com/video", ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := bareYouTubeURL(tc.input)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("bareYouTubeURL(%q) = %q, %v; want %q, %v", tc.input, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestHandleNativeTextCommandRoutesBareYouTubeURL(t *testing.T) {
	b := &Bot{}
	c := &fakeContext{msg: &telebot.Message{
		Text:    "https://youtu.be/abc123",
		Payload: "preserve-me",
		Chat:    &telebot.Chat{ID: 10},
		Sender:  &telebot.User{ID: 20},
	}}

	handled, err := b.handleNativeTextCommand(c)
	if err != nil {
		t.Fatalf("handleNativeTextCommand: %v", err)
	}
	if !handled {
		t.Fatal("expected bare YouTube URL to be handled")
	}
	if len(c.sent) != 1 || c.sent[0] != "Video summary runtime is not available." {
		t.Fatalf("unexpected response: %#v", c.sent)
	}
	if c.msg.Payload != "preserve-me" {
		t.Fatalf("message payload was not restored: %q", c.msg.Payload)
	}
}

func TestHandleNativeTextCommandLeavesConversationalMessageForAgent(t *testing.T) {
	b := &Bot{}
	c := &fakeContext{msg: &telebot.Message{
		Text:   "what do you think about https://youtu.be/abc123?",
		Chat:   &telebot.Chat{ID: 10},
		Sender: &telebot.User{ID: 20},
	}}

	handled, err := b.handleNativeTextCommand(c)
	if err != nil || handled {
		t.Fatalf("handleNativeTextCommand = %v, %v; want false, nil", handled, err)
	}
	if len(c.sent) != 0 {
		t.Fatalf("unexpected response: %#v", c.sent)
	}
}
