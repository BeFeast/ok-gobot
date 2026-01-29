package bot

import (
	"sync"
	"time"
)

const (
	// fragmentStartThreshold - messages longer than this might be split by Telegram
	fragmentStartThreshold = 4000
	// fragmentMaxParts - maximum number of fragments to buffer
	fragmentMaxParts = 12
	// fragmentMaxChars - maximum total characters to buffer
	fragmentMaxChars = 50000
	// fragmentTimeGap - max time between fragments to consider them related
	fragmentTimeGap = 1500 * time.Millisecond
	// fragmentIDGap - max message ID gap between fragments
	fragmentIDGap = 1
)

// FragmentBuffer buffers text fragments that Telegram splits from long pastes
type FragmentBuffer struct {
	mu       sync.Mutex
	buffers  map[int64]*fragmentEntry
	timers   map[int64]*time.Timer
}

type fragmentEntry struct {
	parts     []string
	lastMsgID int
	lastTime  time.Time
	totalLen  int
	userID    int64
}

// NewFragmentBuffer creates a new fragment buffer
func NewFragmentBuffer() *FragmentBuffer {
	return &FragmentBuffer{
		buffers: make(map[int64]*fragmentEntry),
		timers:  make(map[int64]*time.Timer),
	}
}

// TryBuffer attempts to buffer a message fragment. Returns:
// - (combined, true) if the buffer is complete and should be processed
// - ("", false) if the message was buffered and more fragments are expected
// - (original, true) if the message doesn't look like a fragment
func (fb *FragmentBuffer) TryBuffer(chatID int64, userID int64, msgID int, text string, callback func(combined string)) {
	fb.mu.Lock()
	defer fb.mu.Unlock()

	entry, exists := fb.buffers[chatID]

	// Check if this could be a continuation of a previous fragment
	if exists && entry.userID == userID {
		idGap := msgID - entry.lastMsgID
		timeGap := time.Since(entry.lastTime)

		if idGap <= fragmentIDGap+1 && timeGap <= fragmentTimeGap &&
			entry.totalLen+len(text) <= fragmentMaxChars &&
			len(entry.parts) < fragmentMaxParts {

			// This is a continuation fragment
			entry.parts = append(entry.parts, text)
			entry.lastMsgID = msgID
			entry.lastTime = time.Now()
			entry.totalLen += len(text)

			// Reset flush timer
			if timer, ok := fb.timers[chatID]; ok {
				timer.Stop()
			}
			fb.timers[chatID] = time.AfterFunc(fragmentTimeGap, func() {
				fb.flush(chatID, callback)
			})
			return
		}
	}

	// Not a continuation - flush any existing buffer first
	if exists {
		fb.flushLocked(chatID, callback)
	}

	// Check if this message might be the start of a fragment sequence
	if len(text) >= fragmentStartThreshold {
		fb.buffers[chatID] = &fragmentEntry{
			parts:     []string{text},
			lastMsgID: msgID,
			lastTime:  time.Now(),
			totalLen:  len(text),
			userID:    userID,
		}
		fb.timers[chatID] = time.AfterFunc(fragmentTimeGap, func() {
			fb.flush(chatID, callback)
		})
		return
	}

	// Regular short message - pass through immediately
	callback(text)
}

// flush sends the buffered content and cleans up (acquires lock)
func (fb *FragmentBuffer) flush(chatID int64, callback func(string)) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.flushLocked(chatID, callback)
}

// flushLocked sends the buffered content (caller must hold lock)
func (fb *FragmentBuffer) flushLocked(chatID int64, callback func(string)) {
	entry, exists := fb.buffers[chatID]
	if !exists {
		return
	}

	// Combine all fragments
	combined := ""
	for i, part := range entry.parts {
		if i > 0 {
			combined += part
		} else {
			combined = part
		}
	}

	// Clean up
	delete(fb.buffers, chatID)
	if timer, ok := fb.timers[chatID]; ok {
		timer.Stop()
		delete(fb.timers, chatID)
	}

	// Call callback outside lock would deadlock, but we already hold the lock
	// Release lock, call callback, re-acquire would be complex - just call directly
	// since the callback (debouncer) has its own lock
	go callback(combined)
}

// Stop cleans up all buffers and timers
func (fb *FragmentBuffer) Stop() {
	fb.mu.Lock()
	defer fb.mu.Unlock()

	for _, timer := range fb.timers {
		timer.Stop()
	}
	fb.buffers = make(map[int64]*fragmentEntry)
	fb.timers = make(map[int64]*time.Timer)
}
