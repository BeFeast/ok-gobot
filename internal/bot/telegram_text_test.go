package bot

import (
	"strings"
	"testing"
)

func TestSanitizeTelegramModelReplyRemovesMarkdownAroundCommands(t *testing.T) {
	in := "1. **`/video_summary <YT url>`**:\n* Returns `obsidian://open` links from `Digests/YYYY-MM-DD`.\n2. **`/karaoke <YT url>`** returns `karaoke.mp3`."

	got := sanitizeTelegramModelReply(in)

	for _, bad := range []string{"**", "`/video_summary", "`/karaoke", "`obsidian://open", "`Digests/YYYY-MM-DD", "`karaoke.mp3"} {
		if strings.Contains(got, bad) {
			t.Fatalf("unexpected markdown artifact %q in %q", bad, got)
		}
	}
	for _, want := range []string{"/video_summary <YT url>", "/karaoke <YT url>", "obsidian://open", "Digests/YYYY-MM-DD", "karaoke.mp3", "• Returns"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestSanitizeTelegramModelReplyKeepsNonTokenInlineCode(t *testing.T) {
	in := "Use `make test` locally."
	got := sanitizeTelegramModelReply(in)
	want := "Use make test locally."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSanitizeTelegramModelReplyRemovesHeadingsAndGenericOffer(t *testing.T) {
	in := "### 1. `/video_summary <YT url>`\n• Работает.\n\n### 2. `/karaoke <YT url>`\n• Работает.\n\nВам нужно что-то конкретное с ними сделать сейчас?"

	got := sanitizeTelegramModelReply(in)

	for _, bad := range []string{"###", "`", "Вам нужно"} {
		if strings.Contains(got, bad) {
			t.Fatalf("unexpected artifact %q in %q", bad, got)
		}
	}
	for _, want := range []string{"1. /video_summary <YT url>", "2. /karaoke <YT url>", "• Работает."} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
