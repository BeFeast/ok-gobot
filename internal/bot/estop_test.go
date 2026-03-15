package bot

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

func TestHandleEstopCommand_AdminCanToggleAndReadStatus(t *testing.T) {
	t.Parallel()

	store, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	defer store.Close() //nolint:errcheck

	bot := &Bot{
		store: store,
		authManager: NewAuthManager(store, config.AuthConfig{
			Mode:    "allowlist",
			AdminID: 42,
		}),
	}

	ctx := &fakeContext{
		msg: &telebot.Message{
			Payload: "on",
			Chat:    &telebot.Chat{ID: 100, Type: telebot.ChatPrivate},
			Sender:  &telebot.User{ID: 42},
		},
	}
	if err := bot.handleEstopCommand(ctx); err != nil {
		t.Fatalf("handleEstopCommand(on) error = %v", err)
	}

	enabled, err := store.IsEmergencyStopEnabled()
	if err != nil {
		t.Fatalf("IsEmergencyStopEnabled() error = %v", err)
	}
	if !enabled {
		t.Fatal("expected estop to be enabled")
	}
	if got := ctx.sent[len(ctx.sent)-1]; !strings.Contains(got, "estop is ON") {
		t.Fatalf("unexpected on response: %q", got)
	}

	ctx.msg.Payload = "status"
	if err := bot.handleEstopCommand(ctx); err != nil {
		t.Fatalf("handleEstopCommand(status) error = %v", err)
	}
	if got := ctx.sent[len(ctx.sent)-1]; !strings.Contains(got, "Disabled tool families") {
		t.Fatalf("unexpected status response: %q", got)
	}

	ctx.msg.Payload = "off"
	if err := bot.handleEstopCommand(ctx); err != nil {
		t.Fatalf("handleEstopCommand(off) error = %v", err)
	}

	enabled, err = store.IsEmergencyStopEnabled()
	if err != nil {
		t.Fatalf("IsEmergencyStopEnabled() after off error = %v", err)
	}
	if enabled {
		t.Fatal("expected estop to be disabled")
	}
	if got := ctx.sent[len(ctx.sent)-1]; !strings.Contains(got, "estop is OFF") {
		t.Fatalf("unexpected off response: %q", got)
	}
}

func TestHandleEstopCommand_RejectsUnauthorizedToggle(t *testing.T) {
	t.Parallel()

	store, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	defer store.Close() //nolint:errcheck

	bot := &Bot{
		store: store,
		authManager: NewAuthManager(store, config.AuthConfig{
			Mode:    "allowlist",
			AdminID: 42,
		}),
	}

	ctx := &fakeContext{
		msg: &telebot.Message{
			Payload: "on",
			Chat:    &telebot.Chat{ID: 101, Type: telebot.ChatPrivate},
			Sender:  &telebot.User{ID: 7},
		},
	}
	if err := bot.handleEstopCommand(ctx); err != nil {
		t.Fatalf("handleEstopCommand(on) error = %v", err)
	}

	enabled, err := store.IsEmergencyStopEnabled()
	if err != nil {
		t.Fatalf("IsEmergencyStopEnabled() error = %v", err)
	}
	if enabled {
		t.Fatal("expected unauthorized toggle to leave estop disabled")
	}
	if got := ctx.sent[len(ctx.sent)-1]; !strings.Contains(got, "only available to administrators") {
		t.Fatalf("unexpected unauthorized response: %q", got)
	}
}
