package bot

import (
	"context"
	"fmt"
	"log"
	"sync"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/control"
	"ok-gobot/internal/logger"
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

// QueueManager manages per-chat message queuing with different modes
type QueueManager struct {
	mu         sync.Mutex
	activeRuns map[int64]string
	queued     map[int64][]string
	nextRunID  uint64
}

// NewQueueManager creates a new queue manager
func NewQueueManager() *QueueManager {
	return &QueueManager{
		activeRuns: make(map[int64]string),
		queued:     make(map[int64][]string),
	}
}

// IsRunning checks if a chat has an active AI run
func (qm *QueueManager) IsRunning(chatID int64) bool {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	_, ok := qm.activeRuns[chatID]
	return ok
}

// StartRun marks a chat as having an active run
func (qm *QueueManager) StartRun(chatID int64) string {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.nextRunID++
	token := fmt.Sprintf("run-%d", qm.nextRunID)
	qm.activeRuns[chatID] = token
	return token
}

// EndRun marks a chat's run as complete and returns any queued messages
func (qm *QueueManager) EndRun(chatID int64, token string) []string {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	if current, ok := qm.activeRuns[chatID]; !ok || current != token {
		return nil
	}
	delete(qm.activeRuns, chatID)
	queued := qm.queued[chatID]
	delete(qm.queued, chatID)
	return queued
}

// Enqueue adds a message to the chat's queue
func (qm *QueueManager) Enqueue(chatID int64, msg string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.queued[chatID] = append(qm.queued[chatID], msg)
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
		// Steer: add to queue, the active run will pick it up
		b.queueManager.Enqueue(chatID, content)
		logger.Debugf("Bot: steered message to active run, queue depth=%d", b.queueManager.GetQueueDepth(chatID))
		if b.controlHub != nil {
			b.controlHub.Emit(control.EvtSessionQueued, control.SessionInfo{
				ChatID: chatID,
				State:  "queued",
			})
		}
		return true

	case QueueInterrupt:
		// Interrupt: cancel the active run via the hub, then let the new message proceed.
		b.hub.Cancel(sessionKey)
		if ack := b.takeAckHandle(chatID); ack != nil {
			b.updateAckStatus(ack, jobStatusCancelled, "Interrupted by a newer message.")
		}
		log.Printf("[bot] interrupted active run for session %s", sessionKey)
		return false // Let the message proceed normally after interrupt

	case QueueCollect:
		// Collect: buffer the message silently
		b.queueManager.Enqueue(chatID, content)
		logger.Debugf("Bot: collected message, queue depth=%d", b.queueManager.GetQueueDepth(chatID))
		if b.controlHub != nil {
			b.controlHub.Emit(control.EvtSessionQueued, control.SessionInfo{
				ChatID: chatID,
				State:  "queued",
			})
		}
		return true

	default:
		return false
	}
}

func (b *Bot) getQueueMode(chatID int64) QueueMode {
	mode, err := b.store.GetSessionOption(chatID, "queue_mode")
	if err != nil || mode == "" {
		return QueueInterrupt
	}
	return QueueMode(mode)
}
