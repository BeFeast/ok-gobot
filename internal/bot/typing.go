package bot

import (
	"context"
	"sync"
	"time"

	"gopkg.in/telebot.v4"
)

// TypingIndicator manages the "typing..." action for a chat
type TypingIndicator struct {
	bot      *telebot.Bot
	chat     *telebot.Chat
	interval time.Duration
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewTypingIndicator creates and starts a typing indicator for the given chat.
// It sends ChatTyping action immediately and then every 4 seconds until Stop is called.
// Returns a function that stops the typing indicator.
func NewTypingIndicator(bot *telebot.Bot, chat *telebot.Chat) func() {
	ctx, cancel := context.WithCancel(context.Background())

	ti := &TypingIndicator{
		bot:      bot,
		chat:     chat,
		interval: 4 * time.Second, // Refresh every 4s (Telegram typing expires ~5s)
		ctx:      ctx,
		cancel:   cancel,
	}

	ti.wg.Add(1)
	go ti.run()

	return ti.Stop
}

// run sends typing action periodically
func (ti *TypingIndicator) run() {
	defer ti.wg.Done()

	// Send typing immediately
	ti.bot.Notify(ti.chat, telebot.Typing)

	ticker := time.NewTicker(ti.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ti.ctx.Done():
			return
		case <-ticker.C:
			ti.bot.Notify(ti.chat, telebot.Typing)
		}
	}
}

// Stop stops the typing indicator and waits for the goroutine to finish
func (ti *TypingIndicator) Stop() {
	ti.cancel()
	ti.wg.Wait()
}
