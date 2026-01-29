package bot

import (
	"strings"
	"sync"
	"time"

	"gopkg.in/telebot.v4"
)

// StreamEditor handles rate-limited message editing for streaming responses
type StreamEditor struct {
	bot         *telebot.Bot
	chat        *telebot.Chat
	message     *telebot.Message
	content     strings.Builder
	mu          sync.Mutex
	lastEdit    time.Time
	minInterval time.Duration
	pending     bool
	done        chan struct{}
}

// NewStreamEditor creates a new stream editor for a chat
func NewStreamEditor(bot *telebot.Bot, chat *telebot.Chat, initialMsg *telebot.Message) *StreamEditor {
	return &StreamEditor{
		bot:         bot,
		chat:        chat,
		message:     initialMsg,
		minInterval: 1 * time.Second, // Telegram rate limit
		done:        make(chan struct{}),
	}
}

// Append adds content and schedules an edit
func (e *StreamEditor) Append(text string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.content.WriteString(text)

	if !e.pending {
		e.pending = true
		go e.scheduleEdit()
	}
}

// scheduleEdit waits for rate limit and performs the edit
func (e *StreamEditor) scheduleEdit() {
	for {
		e.mu.Lock()
		if !e.pending {
			e.mu.Unlock()
			return
		}

		// Check if we can edit now
		elapsed := time.Since(e.lastEdit)
		if elapsed < e.minInterval {
			e.mu.Unlock()
			time.Sleep(e.minInterval - elapsed)
			continue
		}

		// Get current content and reset pending flag
		content := e.content.String()
		e.pending = false
		e.lastEdit = time.Now()
		e.mu.Unlock()

		// Perform the edit
		if content != "" && e.message != nil {
			e.bot.Edit(e.message, content, &telebot.SendOptions{
				ParseMode: telebot.ModeMarkdown,
			})
		}

		// Check if there's more content that arrived during edit
		e.mu.Lock()
		if e.content.Len() > len(content) {
			e.pending = true
			e.mu.Unlock()
			continue
		}
		e.mu.Unlock()
		return
	}
}

// Finish performs a final edit with the complete content
func (e *StreamEditor) Finish() string {
	e.mu.Lock()
	defer e.mu.Unlock()

	content := e.content.String()

	// Final edit
	if content != "" && e.message != nil {
		e.bot.Edit(e.message, content, &telebot.SendOptions{
			ParseMode: telebot.ModeMarkdown,
		})
	}

	return content
}

// GetContent returns the current accumulated content
func (e *StreamEditor) GetContent() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.content.String()
}

// SetMessage updates the message being edited (useful if initial send returns new message)
func (e *StreamEditor) SetMessage(msg *telebot.Message) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.message = msg
}
