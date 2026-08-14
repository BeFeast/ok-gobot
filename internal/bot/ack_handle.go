package bot

import (
	"sync"

	"gopkg.in/telebot.v4"
)

// AckHandle holds the ⏳ placeholder message sent immediately on inbound Telegram message receipt.
// The placeholder message ID is stored here for subsequent live-edit updates once the AI response is ready.
type AckHandle struct {
	Message *telebot.Message
	ChatID  int64
	JobID   string

	mu     sync.Mutex
	editor *LiveStreamEditor
}

// AttachEditor links the live editor that updates this placeholder, so any
// terminal status writer (including the queue-interrupt path on another
// goroutine) can stop it before performing the final edit.
func (h *AckHandle) AttachEditor(editor *LiveStreamEditor) {
	h.mu.Lock()
	h.editor = editor
	h.mu.Unlock()
}

// DetachEditor returns and clears the attached live editor, if any.
func (h *AckHandle) DetachEditor() *LiveStreamEditor {
	h.mu.Lock()
	defer h.mu.Unlock()
	editor := h.editor
	h.editor = nil
	return editor
}

// AckHandleManager manages per-chat ack handles with thread-safe access.
type AckHandleManager struct {
	mu      sync.Mutex
	handles map[int64]*AckHandle
}

// NewAckHandleManager creates a new AckHandleManager.
func NewAckHandleManager() *AckHandleManager {
	return &AckHandleManager{
		handles: make(map[int64]*AckHandle),
	}
}

// Set stores an ack handle for a chat, replacing any existing one.
func (m *AckHandleManager) Set(chatID int64, handle *AckHandle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handles[chatID] = handle
}

// Exists returns true if an ack handle already exists for the chat.
func (m *AckHandleManager) Exists(chatID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.handles[chatID]
	return ok
}

// Take retrieves and removes the ack handle for a chat (consume-once semantics).
// Returns nil if no handle exists.
func (m *AckHandleManager) Take(chatID int64) *AckHandle {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.handles[chatID]
	delete(m.handles, chatID)
	return h
}

// Peek returns the ack handle for a chat without removing it.
// Returns nil if no handle exists.
func (m *AckHandleManager) Peek(chatID int64) *AckHandle {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handles[chatID]
}
