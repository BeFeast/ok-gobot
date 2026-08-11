package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/telebot.v4"
)

func TestSendTelegramRichMarkdownUsesRichMessage(t *testing.T) {
	var path string
	var body map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = fmt.Fprint(w, `{"ok":true,"result":{"message_id":42,"chat":{"id":7,"type":"private"}}}`)
	}))
	defer server.Close()

	b := newTelegramRichTestBot(t, server)
	markdown := "### Weather\n\n- **31°C**\n- `clear`"
	message, err := b.sendTelegramRichMarkdown(telebot.ChatID(7), markdown, &telebot.SendOptions{
		ReplyTo: &telebot.Message{ID: 11},
	})
	if err != nil {
		t.Fatalf("sendTelegramRichMarkdown: %v", err)
	}
	if message == nil || message.ID != 42 {
		t.Fatalf("message = %+v", message)
	}
	if path != "/bottoken/sendRichMessage" {
		t.Fatalf("path = %q", path)
	}

	richRaw, ok := body["rich_message"].(string)
	if !ok {
		t.Fatalf("rich_message = %#v", body["rich_message"])
	}
	var rich struct {
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal([]byte(richRaw), &rich); err != nil {
		t.Fatalf("decode rich_message: %v", err)
	}
	if rich.Markdown != markdown {
		t.Fatalf("markdown = %q", rich.Markdown)
	}
	if got := body["reply_to_message_id"]; got != "11" {
		t.Fatalf("reply_to_message_id = %#v", got)
	}
}

func TestSendTelegramRichMarkdownFallsBackAfterAPIRejection(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if len(paths) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse rich message"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"ok":true,"result":{"message_id":43,"chat":{"id":7,"type":"private"},"text":"plain"}}`)
	}))
	defer server.Close()

	b := newTelegramRichTestBot(t, server)
	message, err := b.sendTelegramRichMarkdown(telebot.ChatID(7), "**broken <tag>**")
	if err != nil {
		t.Fatalf("sendTelegramRichMarkdown: %v", err)
	}
	if message == nil || message.ID != 43 {
		t.Fatalf("message = %+v", message)
	}
	want := []string{"/bottoken/sendRichMessage", "/bottoken/sendMessage"}
	if fmt.Sprint(paths) != fmt.Sprint(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestSendTelegramRichMarkdownDoesNotRetryTransportOrServerFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`)
	}))
	defer server.Close()

	b := newTelegramRichTestBot(t, server)
	if _, err := b.sendTelegramRichMarkdown(telebot.ChatID(7), "**hello**"); err == nil {
		t.Fatal("expected server error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestSplitMessageUsesUnicodeCharacterLimit(t *testing.T) {
	message := strings.Repeat("я", 10)
	chunks := splitMessage(message, 4)
	if got := strings.Join(chunks, ""); got != message {
		t.Fatalf("joined chunks = %q", got)
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 4 {
			t.Fatalf("chunk exceeds rune limit: %q", chunk)
		}
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk is invalid UTF-8: %q", chunk)
		}
	}
}

func newTelegramRichTestBot(t *testing.T, server *httptest.Server) *Bot {
	t.Helper()
	api, err := telebot.NewBot(telebot.Settings{
		Offline: true,
		Token:   "token",
		URL:     server.URL,
		Client:  server.Client(),
	})
	if err != nil {
		t.Fatalf("telebot.NewBot: %v", err)
	}
	return &Bot{api: api}
}
