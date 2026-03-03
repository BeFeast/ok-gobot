package bot

import (
	"strings"
	"sync"
	"time"

	"gopkg.in/telebot.v4"
)

const (
	// streamEditInterval is the minimum time between edits (Telegram rate limit policy)
	streamEditInterval = 1500 * time.Millisecond
	// streamTokenThreshold triggers an immediate edit after this many new tokens
	streamTokenThreshold = 20
)

// StreamEditor handles rate-limited message editing for streaming responses.
// Edits are throttled to at most once per streamEditInterval, but are also
// flushed eagerly after streamTokenThreshold new tokens arrive.
type StreamEditor struct {
	bot           *telebot.Bot
	chat          *telebot.Chat
	message       *telebot.Message
	content       strings.Builder
	mu            sync.Mutex
	lastEdit      time.Time
	lastEditLen   int // content length at last edit
	pendingTokens int // tokens accumulated since last edit
	pending       bool
	done          chan struct{}
}

// NewStreamEditor creates a new stream editor for a chat
func NewStreamEditor(bot *telebot.Bot, chat *telebot.Chat, initialMsg *telebot.Message) *StreamEditor {
	return &StreamEditor{
		bot:     bot,
		chat:    chat,
		message: initialMsg,
		done:    make(chan struct{}),
	}
}

// Append adds content (one token batch) and schedules an edit.
// If streamTokenThreshold tokens have accumulated since the last edit,
// an immediate edit is triggered regardless of the time interval.
func (e *StreamEditor) Append(text string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.content.WriteString(text)
	e.pendingTokens++

	if !e.pending {
		e.pending = true
		go e.scheduleEdit()
	}
}

// scheduleEdit waits for the rate limit or token threshold and performs the edit
func (e *StreamEditor) scheduleEdit() {
	for {
		e.mu.Lock()
		if !e.pending {
			e.mu.Unlock()
			return
		}

		elapsed := time.Since(e.lastEdit)
		tokensBurst := e.pendingTokens >= streamTokenThreshold

		// Edit if the token threshold is reached OR if the interval has passed
		if !tokensBurst && elapsed < streamEditInterval {
			e.mu.Unlock()
			// Sleep until interval expires, then re-check
			time.Sleep(streamEditInterval - elapsed)
			continue
		}

		// Snapshot state and reset counters
		content := e.content.String()
		e.pending = false
		e.lastEdit = time.Now()
		e.lastEditLen = len(content)
		e.pendingTokens = 0
		e.mu.Unlock()

		// Perform the edit — AI output is already markdown, don't escape it
		if content != "" && e.message != nil {
			e.bot.Edit(e.message, content, &telebot.SendOptions{
				ParseMode: telebot.ModeMarkdown,
			})
		}

		// Check if more content arrived during the edit
		e.mu.Lock()
		if e.content.Len() > e.lastEditLen {
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

	// Final edit — AI output is already markdown, don't escape it
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
