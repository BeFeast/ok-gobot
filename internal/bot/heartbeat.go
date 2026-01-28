package bot

import (
	"context"
	"log"
	"time"

	"ok-gobot/internal/agent"
)

// startHeartbeat starts the periodic heartbeat checker
func (b *Bot) startHeartbeat() {
	ticker := time.NewTicker(5 * time.Minute) // Check every 5 minutes
	defer ticker.Stop()

	heartbeat, err := agent.NewHeartbeat("")
	if err != nil {
		return // Silently fail if heartbeat can't be initialized
	}

	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result, err := heartbeat.Check(ctx)
		cancel()

		if err != nil {
			continue // Silently fail
		}

		// Send notification if there's something important
		if result.ShouldNotify() {
			// In a real implementation, this would send to the admin
			// For now, just log it
			log.Printf("Heartbeat: %s", result.FormatNotification())
		}
	}
}
