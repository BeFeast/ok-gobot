package bot

import (
	"testing"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
)

func TestMemoryRecallContextUsesSenderIDForUserScope(t *testing.T) {
	bot := &Bot{adminID: 777}

	ctx := bot.memoryRecallContext(555, 777, string(telebot.ChatPrivate), agent.NewDMSessionKey(555))
	if ctx.ChatID != 555 {
		t.Fatalf("expected chat id 555, got %+v", ctx)
	}
	if ctx.UserID != 777 {
		t.Fatalf("expected sender user id 777, got %+v", ctx)
	}
	if ctx.SessionKey != "dm:555" {
		t.Fatalf("expected chat session key, got %+v", ctx)
	}
	if !ctx.AllowGlobalPrivate {
		t.Fatalf("expected admin sender to allow private-global recall, got %+v", ctx)
	}
}

func TestMemoryRecallContextDoesNotTreatChatIDAsAdminUserID(t *testing.T) {
	bot := &Bot{adminID: 555}

	ctx := bot.memoryRecallContext(555, 777, string(telebot.ChatPrivate), agent.NewDMSessionKey(555))
	if ctx.AllowGlobalPrivate {
		t.Fatalf("chat id must not grant admin private-global recall when sender differs: %+v", ctx)
	}
}
