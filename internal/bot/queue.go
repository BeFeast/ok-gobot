package bot

import (
	"context"
	"log"
	"sync"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/logger"

	"gopkg.in/telebot.v4"
)

// QueueMode defines how incoming messages are handled during an active AI run
type QueueMode string

const (
	// QueueCollect accumulates messages without auto-reply until run finishes
	QueueCollect QueueMode = "collect"
	// QueueSteer sends new messages as steering input to the current run
	QueueSteer QueueMode = "steer"
	// QueueInterrupt cancels current run and starts fresh with new message
	QueueInterrupt QueueMode = "interrupt"
)

// QueuedMessage holds a buffered message and the ID of its acknowledgment message
type QueuedMessage struct {
	Content  string
	AckMsgID int
}

// QueueManager manages per-chat message queuing with different modes
type QueueManager struct {
	mu         sync.Mutex
	activeRuns map[int64]bool
	queued     map[int64][]QueuedMessage
}

// NewQueueManager creates a new queue manager
func NewQueueManager() *QueueManager {
	return &QueueManager{
		activeRuns: make(map[int64]bool),
		queued:     make(map[int64][]QueuedMessage),
	}
}

// IsRunning checks if a chat has an active AI run
func (qm *QueueManager) IsRunning(chatID int64) bool {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	return qm.activeRuns[chatID]
}

// StartRun marks a chat as having an active run
func (qm *QueueManager) StartRun(chatID int64) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.activeRuns[chatID] = true
}

// EndRun marks a chat's run as complete and returns any queued messages
func (qm *QueueManager) EndRun(chatID int64) []QueuedMessage {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.activeRuns[chatID] = false
	queued := qm.queued[chatID]
	delete(qm.queued, chatID)
	return queued
}

// Enqueue adds a message to the chat's queue along with the ID of the sent acknowledgment message
func (qm *QueueManager) Enqueue(chatID int64, content string, ackMsgID int) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.queued[chatID] = append(qm.queued[chatID], QueuedMessage{Content: content, AckMsgID: ackMsgID})
}

// GetQueueDepth returns the number of queued messages for a chat
func (qm *QueueManager) GetQueueDepth(chatID int64) int {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	return len(qm.queued[chatID])
}

// handleWithQueueMode processes a message according to the queue mode.
// Returns true if the message was handled (queued/steered), false if it should proceed normally.
func (b *Bot) handleWithQueueMode(ctx context.Context, sessionKey agent.SessionKey, chatID int64, content string) bool {
	if !b.queueManager.IsRunning(chatID) {
		return false // No active run, proceed normally
	}

	mode := b.getQueueMode(chatID)
	logger.Debugf("Bot: queue mode=%s for chat=%d (run active)", mode, chatID)

	switch mode {
	case QueueSteer:
		// Steer: add to queue; send immediate acknowledgment
		ackMsgID := b.sendQueuedAck(c)
		b.queueManager.Enqueue(chatID, content, ackMsgID)
		logger.Debugf("Bot: steered message to active run, queue depth=%d", b.queueManager.GetQueueDepth(chatID))
		return true

	case QueueInterrupt:
		// Interrupt: cancel the active run via the hub, then let the new message proceed.
		b.hub.Cancel(sessionKey)
		log.Printf("[bot] interrupted active run for session %s", sessionKey)
		return false // Let the message proceed normally after interrupt

	case QueueCollect:
		// Collect: buffer the message and send immediate acknowledgment
		ackMsgID := b.sendQueuedAck(c)
		b.queueManager.Enqueue(chatID, content, ackMsgID)
		logger.Debugf("Bot: collected message, queue depth=%d", b.queueManager.GetQueueDepth(chatID))
		return true

	default:
		return false
	}
}

// sendQueuedAck sends an immediate "queued" acknowledgment to the user and returns
// the sent message ID (0 on failure).
func (b *Bot) sendQueuedAck(c telebot.Context) int {
	depth := b.queueManager.GetQueueDepth(c.Chat().ID)
	var text string
	if depth == 0 {
		text = "⏳ queued — previous run in progress"
	} else {
		text = "⏳ queued — previous run in progress"
	}
	msg, err := b.api.Send(c.Chat(), text)
	if err != nil {
		log.Printf("Bot: failed to send queue ack: %v", err)
		return 0
	}
	return msg.ID
}

func (b *Bot) getQueueMode(chatID int64) QueueMode {
	mode, err := b.store.GetSessionOption(chatID, "queue_mode")
	if err != nil || mode == "" {
		return QueueCollect
	}
	return QueueMode(mode)
}
