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
	if got != in {
		t.Fatalf("got %q, want %q", got, in)
	}
}
