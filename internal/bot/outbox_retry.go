package bot

import (
	"context"
	"log"
	"time"

	"gopkg.in/telebot.v4"
)

// Redelivery of answers that were computed but never reached anyone.
//
// The outbox commits a finished result before any send is attempted, so a
// crash between "work done" and "message sent" no longer destroys it. This is
// the other half: on startup, and periodically after that, anything still owed
// gets sent. Without this the row survives and simply sits there — which is
// better than losing it, but still not an answer.

const (
	outboxRetryInterval = 30 * time.Second
	outboxRetryBatch    = 20
)

// StartOutboxRetry drains undelivered replies until ctx is done. It runs one
// pass immediately so a restart delivers straight away rather than after the
// first tick.
func (b *Bot) StartOutboxRetry(ctx context.Context) {
	if b.store == nil {
		return
	}
	go func() {
		// Rows left claimed by a process that died mid-send are nobody's until
		// they are released; without this they would sit as 'sending' forever.
		if n, err := b.store.ReclaimStaleOutbox(5); err != nil {
			log.Printf("[outbox] could not reclaim abandoned replies: %v", err)
		} else if n > 0 {
			log.Printf("[outbox] reclaimed %d reply(ies) abandoned mid-send", n)
		}
		b.drainOutbox()
		ticker := time.NewTicker(outboxRetryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.drainOutbox()
			}
		}
	}()
}

func (b *Bot) drainOutbox() {
	pending, err := b.store.PendingOutbox(outboxRetryBatch)
	if err != nil {
		log.Printf("[outbox] could not read pending replies: %v", err)
		return
	}
	for _, msg := range pending {
		// Claim before sending. The inline completion path and this loop can
		// see the same row; whoever wins the claim sends it, the other skips.
		won, err := b.store.ClaimOutbox(msg.ID)
		if err != nil {
			log.Printf("[outbox] could not claim id=%d: %v", msg.ID, err)
			continue
		}
		if !won {
			continue // someone else is already sending this one
		}

		// A redelivered answer may be a duplicate of one that did arrive: the
		// process can die after Telegram accepted the message but before the
		// row was marked. Saying so is better than a silent repeat.
		text := msg.Text
		if msg.Attempts > 0 {
			text = "🔁 Redelivered (this may be a duplicate)\n\n" + msg.Text
		}

		recipient := telebot.ChatID(msg.ChatID)
		sent, sendErr := sendMarkdownWithPlainFallback(b.api, recipient, text)
		if sendErr != nil {
			if err := b.store.RecordOutboxFailure(msg.ID, sendErr.Error()); err != nil {
				log.Printf("[outbox] could not record failure id=%d: %v", msg.ID, err)
			}
			log.Printf("[outbox] redelivery failed id=%d chat=%d attempt=%d: %v",
				msg.ID, msg.ChatID, msg.Attempts+1, sendErr)
			continue
		}

		var sentID int64
		if sent != nil {
			sentID = int64(sent.ID)
		}
		if err := b.store.MarkOutboxDelivered(msg.ID, sentID); err != nil {
			log.Printf("[outbox] could not mark delivered id=%d: %v", msg.ID, err)
			continue
		}
		log.Printf("[outbox] redelivered id=%d chat=%d after %d failed attempt(s)",
			msg.ID, msg.ChatID, msg.Attempts)
	}
}
