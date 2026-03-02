package bot

import (
	"context"
	"fmt"
	"log"

	"ok-gobot/internal/ai"

	"gopkg.in/telebot.v4"
)

// handleStreamingRequestWithProfile processes message with streaming response using active agent.
// NOTE: This function is not used in the main message path (tool calling is always used instead).
// It is retained for potential future use.
func (b *Bot) handleStreamingRequestWithProfile(ctx context.Context, c telebot.Context, content, session string) error {
	chatID := c.Chat().ID

	// Build messages for AI
	messages := []ai.Message{
		{Role: "system", Content: b.personality.GetSystemPrompt()},
	}
	if session != "" {
		messages = append(messages, ai.Message{Role: "assistant", Content: session})
	}
	messages = append(messages, ai.Message{Role: "user", Content: content})

	// Send initial "thinking" message
	thinkingMsg, err := b.api.Send(c.Chat(), "💭 Thinking...")
	if err != nil {
		return err
	}

	// Create stream editor
	editor := NewStreamEditor(b.api, c.Chat(), thinkingMsg)

	// Start streaming
	streamCh := b.streamingAI.CompleteStream(ctx, messages)

	for chunk := range streamCh {
		if chunk.Error != nil {
			log.Printf("Stream error: %v", chunk.Error)
			b.api.Edit(thinkingMsg, "❌ Sorry, I encountered an error.")
			return chunk.Error
		}

		if chunk.Content != "" {
			editor.Append(chunk.Content)
		}

		if chunk.Done {
			break
		}
	}

	// Final update
	finalContent := editor.Finish()

	// Save to memory
	if err := b.memory.AppendToToday(fmt.Sprintf("Assistant: %s", finalContent)); err != nil {
		log.Printf("Failed to save to memory: %v", err)
	}

	// Save session
	if err := b.store.SaveSession(chatID, finalContent); err != nil {
		log.Printf("Failed to save session: %v", err)
	}

	return nil
}
