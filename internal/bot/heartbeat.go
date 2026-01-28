package bot

import (
	"context"
	"fmt"
	"log"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
)

// startHeartbeat starts the periodic heartbeat checker
func (b *Bot) startHeartbeat() {
	// Wait a bit before first check
	time.Sleep(30 * time.Second)

	ticker := time.NewTicker(5 * time.Minute) // Check every 5 minutes
	defer ticker.Stop()

	heartbeat, err := agent.NewHeartbeat("")
	if err != nil {
		log.Printf("Failed to initialize heartbeat: %v", err)
		return
	}

	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result, err := heartbeat.Check(ctx)
		cancel()

		if err != nil {
			log.Printf("Heartbeat check failed: %v", err)
			continue
		}

		// Send notification if there's something important
		if result.ShouldNotify() {
			message := result.FormatNotification()

			// Send to admin if configured
			if b.adminID != 0 {
				chat := &telebot.Chat{ID: b.adminID}
				if _, err := b.api.Send(chat, fmt.Sprintf("💓 *Heartbeat*\n\n%s", message),
					&telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
					log.Printf("Failed to send heartbeat notification: %v", err)
				}
			} else {
				// Just log it if no admin configured
				log.Printf("Heartbeat: %s", message)
			}
		}
	}
}
