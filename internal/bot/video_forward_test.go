package bot

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"

	"gopkg.in/telebot.v4"
)

func TestTranscribableDocument(t *testing.T) {
	t.Parallel()

	transcribable := []string{"video/mp4", "VIDEO/QUICKTIME", " video/webm ", "audio/mpeg", "audio/ogg"}
	for _, mime := range transcribable {
		if !transcribableDocument(&telebot.Document{MIME: mime}) {
			t.Errorf("transcribableDocument(%q) = false, want true", mime)
		}
	}

	other := []string{"", "text/plain", "application/pdf", "image/png", "videos/mp4"}
	for _, mime := range other {
		if transcribableDocument(&telebot.Document{MIME: mime}) {
			t.Errorf("transcribableDocument(%q) = true, want false", mime)
		}
	}
	if transcribableDocument(nil) {
		t.Error("transcribableDocument(nil) = true, want false")
	}
}

// captureLog collects everything written through the standard logger while fn
// runs. The point of issue #56 is that a branch leaves a trace at all, so the
// journal output is the assertion, not an implementation detail.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()
	fn()
	return buf.String()
}

func newForwardTestContext(msg *telebot.Message) *fakeContext {
	msg.Chat = &telebot.Chat{ID: 123, Type: telebot.ChatPrivate}
	msg.Sender = &telebot.User{ID: 456, Username: "tester"}
	return &fakeContext{msg: msg}
}

func TestVideoForwardTerminalRecordsDelivery(t *testing.T) {
	b := &Bot{}
	ctx := newForwardTestContext(&telebot.Message{ID: 1})

	out := captureLog(t, func() {
		if err := b.videoForwardTerminal(ctx, "some_reason", "text"); err != nil {
			t.Fatalf("videoForwardTerminal: %v", err)
		}
	})
	if !strings.Contains(out, "reason=some_reason") || !strings.Contains(out, "delivered=true") {
		t.Fatalf("log = %q, want reason and delivered=true", out)
	}
	if len(ctx.sent) != 1 || ctx.sent[0] != "text" {
		t.Fatalf("sent = %#v", ctx.sent)
	}
}

func TestVideoForwardTerminalReportsFailedDelivery(t *testing.T) {
	b := &Bot{}
	ctx := newForwardTestContext(&telebot.Message{ID: 1})
	ctx.sendErr = errors.New("telegram down")

	var err error
	out := captureLog(t, func() {
		err = b.videoForwardTerminal(ctx, "some_reason", "text")
	})
	if err == nil {
		t.Fatal("expected the send error to propagate")
	}
	// The 2026-08-29 silence was a failed send nobody could see afterwards.
	if !strings.Contains(out, "delivered=false") || !strings.Contains(out, "telegram down") {
		t.Fatalf("log = %q, want delivered=false and the send error", out)
	}
}

func TestHandleForwardedVideoAcceptsVideoDocument(t *testing.T) {
	// A video forwarded "as a file" arrives as a Document. Before issue #54 it
	// never reached this handler at all; oversize is used here because it is
	// the first terminal branch and needs no network.
	b := &Bot{}
	ctx := newForwardTestContext(&telebot.Message{
		ID:       1,
		Document: &telebot.Document{MIME: "video/mp4", File: telebot.File{FileSize: 21 * 1024 * 1024}},
	})

	out := captureLog(t, func() {
		if err := b.handleForwardedVideo(t.Context(), ctx); err != nil {
			t.Fatalf("handleForwardedVideo: %v", err)
		}
	})
	if !strings.Contains(out, "reason=over_telegram_limit") {
		t.Fatalf("log = %q, want the oversize terminal reason", out)
	}
	if len(ctx.sent) != 1 {
		t.Fatalf("sent = %#v", ctx.sent)
	}
	// The old copy told the user this only helps "if it is a YouTube video",
	// which is what pushed them into a manual re-upload that cannot work.
	if strings.Contains(ctx.sent[0], "YouTube-ролик") {
		t.Fatalf("oversize message still claims YouTube-only: %q", ctx.sent[0])
	}
	if !strings.Contains(ctx.sent[0], "/video_summary") {
		t.Fatalf("oversize message should point at the URL workflow: %q", ctx.sent[0])
	}
}

func TestHandleForwardedVideoRejectsNonMediaDocument(t *testing.T) {
	b := &Bot{}
	ctx := newForwardTestContext(&telebot.Message{
		ID:       1,
		Document: &telebot.Document{MIME: "application/pdf", File: telebot.File{FileSize: 1024}},
	})

	out := captureLog(t, func() {
		if err := b.handleForwardedVideo(t.Context(), ctx); err != nil {
			t.Fatalf("handleForwardedVideo: %v", err)
		}
	})
	if !strings.Contains(out, "reason=no_media_track") {
		t.Fatalf("log = %q, want the no-media terminal reason", out)
	}
}
