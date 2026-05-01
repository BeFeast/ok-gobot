package bot

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/storage"
)

func TestHandleSteerCommandQueuesInputWithoutChangingQueueMode(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	defer store.Close() //nolint:errcheck

	const chatID int64 = 77
	if err := store.SetSessionOption(chatID, "queue_mode", "collect"); err != nil {
		t.Fatalf("SetSessionOption() error = %v", err)
	}

	bot := &Bot{store: store, queueManager: NewQueueManager()}
	runToken := bot.queueManager.StartRun(chatID)
	defer bot.queueManager.EndRun(chatID, runToken)

	ctx := &fakeContext{msg: &telebot.Message{
		Payload: "please focus on the failing test first",
		Chat:    &telebot.Chat{ID: chatID, Type: telebot.ChatPrivate},
		Sender:  &telebot.User{ID: 1},
	}}

	if err := bot.handleSteerCommand(ctx); err != nil {
		t.Fatalf("handleSteerCommand() error = %v", err)
	}
	if depth := bot.queueManager.GetQueueDepth(chatID); depth != 1 {
		t.Fatalf("queue depth = %d, want 1", depth)
	}
	mode, err := store.GetSessionOption(chatID, "queue_mode")
	if err != nil {
		t.Fatalf("GetSessionOption() error = %v", err)
	}
	if mode != "collect" {
		t.Fatalf("queue mode changed to %q, want collect", mode)
	}
	if len(ctx.sent) != 1 || !strings.Contains(ctx.sent[0], "Queue mode remains `collect`") {
		t.Fatalf("unexpected response: %#v", ctx.sent)
	}
}

func TestHandleSteerCommandRequiresActiveRun(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	defer store.Close() //nolint:errcheck

	bot := &Bot{store: store, queueManager: NewQueueManager()}
	ctx := &fakeContext{msg: &telebot.Message{
		Payload: "new direction",
		Chat:    &telebot.Chat{ID: 78, Type: telebot.ChatPrivate},
		Sender:  &telebot.User{ID: 1},
	}}

	if err := bot.handleSteerCommand(ctx); err != nil {
		t.Fatalf("handleSteerCommand() error = %v", err)
	}
	if len(ctx.sent) != 1 || !strings.Contains(ctx.sent[0], "No active run") {
		t.Fatalf("unexpected response: %#v", ctx.sent)
	}
}
