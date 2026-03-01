package bot

import (
	"context"
	"fmt"
	"log"
	"sync"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/runtime"
)

// SubagentNotifier subscribes to a runtime.Hub and delivers Telegram notifications
// to parent chats when a child sub-agent run completes or fails.
//
// Usage:
//  1. Create a SubagentNotifier and start it with Run.
//  2. Before spawning a sub-agent, call RegisterParent(childSessionKey, parentChatID)
//     so the notifier knows which Telegram chat to notify on completion.
type SubagentNotifier struct {
	api *telebot.Bot

	mu        sync.Mutex
	parentMap map[string]int64 // childSessionKey → parent chatID
}

// NewSubagentNotifier creates a notifier that sends Telegram messages via api.
func NewSubagentNotifier(api *telebot.Bot) *SubagentNotifier {
	return &SubagentNotifier{
		api:       api,
		parentMap: make(map[string]int64),
	}
}

// RegisterParent records that when the run keyed by childSessionKey completes,
// a Telegram notification should be sent to parentChatID.
// The registration is consumed once the child run finishes.
func (n *SubagentNotifier) RegisterParent(childSessionKey string, parentChatID int64) {
	n.mu.Lock()
	n.parentMap[childSessionKey] = parentChatID
	n.mu.Unlock()
}

// Run subscribes to hub and processes child-completion events until ctx is cancelled.
// Call this in a goroutine.
func (n *SubagentNotifier) Run(ctx context.Context, hub *runtime.Hub) {
	ch := make(chan runtime.RuntimeEvent, 64)
	hub.Subscribe(ch)
	defer hub.Unsubscribe(ch)

	log.Printf("[subagent-notifier] started")
	for {
		select {
		case <-ctx.Done():
			log.Printf("[subagent-notifier] stopped")
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Type == runtime.EventChildDone || ev.Type == runtime.EventChildFailed {
				n.handleCompletion(ev)
			}
		}
	}
}

// handleCompletion sends a Telegram notification to the parent chat.
func (n *SubagentNotifier) handleCompletion(ev runtime.RuntimeEvent) {
	payload, ok := ev.Payload.(runtime.ChildCompletionPayload)
	if !ok {
		return
	}

	n.mu.Lock()
	chatID, exists := n.parentMap[payload.ChildSessionKey]
	if exists {
		delete(n.parentMap, payload.ChildSessionKey)
	}
	n.mu.Unlock()

	if !exists {
		return
	}

	text := formatCompletionMessage(ev.Type, payload)
	chat := &telebot.Chat{ID: chatID}
	if _, err := n.api.Send(chat, text, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
		log.Printf("[subagent-notifier] failed to notify chat %d: %v", chatID, err)
	}
}

// formatCompletionMessage builds the notification text for a child-completion event.
func formatCompletionMessage(evType runtime.EventType, payload runtime.ChildCompletionPayload) string {
	childKey := payload.ChildSessionKey

	if evType == runtime.EventChildDone {
		msg := fmt.Sprintf("✅ *Sub-agent completed*\n`%s`", childKey)
		if payload.Summary != "" {
			msg += "\n\n" + payload.Summary
		}
		return msg
	}

	// EventChildFailed
	errStr := "unknown error"
	if payload.Err != nil {
		errStr = payload.Err.Error()
	}
	return fmt.Sprintf("❌ *Sub-agent failed*\n`%s`\n\nError: %s", childKey, errStr)
}
