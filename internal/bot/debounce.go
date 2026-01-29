package bot

import (
	"strings"
	"sync"
	"time"
)

// Debouncer accumulates messages from the same chat within a time window
// and fires a callback with all accumulated text when the window expires
type Debouncer struct {
	window   time.Duration
	mu       sync.Mutex
	timers   map[int64]*time.Timer
	buffers  map[int64][]string
	stopped  bool
}

// NewDebouncer creates a new debouncer with the specified window duration
func NewDebouncer(window time.Duration) *Debouncer {
	if window <= 0 {
		window = 1500 * time.Millisecond // default 1.5 seconds
	}
	return &Debouncer{
		window:  window,
		timers:  make(map[int64]*time.Timer),
		buffers: make(map[int64][]string),
	}
}

// Debounce accumulates a message for the given chat ID and schedules callback execution
// If multiple messages arrive within the window, they are combined with newlines
func (d *Debouncer) Debounce(chatID int64, text string, callback func(combined string)) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.stopped {
		return
	}

	// Add text to buffer
	d.buffers[chatID] = append(d.buffers[chatID], text)

	// Cancel existing timer if any
	if timer, exists := d.timers[chatID]; exists {
		timer.Stop()
	}

	// Create new timer
	d.timers[chatID] = time.AfterFunc(d.window, func() {
		d.mu.Lock()
		defer d.mu.Unlock()

		if d.stopped {
			return
		}

		// Get accumulated messages
		buffer := d.buffers[chatID]
		if len(buffer) == 0 {
			return
		}

		// Combine messages with newlines
		combined := strings.Join(buffer, "\n")

		// Clear buffer and timer
		delete(d.buffers, chatID)
		delete(d.timers, chatID)

		// Unlock before callback to avoid deadlock
		d.mu.Unlock()
		callback(combined)
		d.mu.Lock()
	})
}

// Stop stops all timers and prevents further debouncing
func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.stopped = true

	// Stop all active timers
	for _, timer := range d.timers {
		timer.Stop()
	}

	// Clear state
	d.timers = make(map[int64]*time.Timer)
	d.buffers = make(map[int64][]string)
}

// GetPendingCount returns the number of chats with pending debounced messages
func (d *Debouncer) GetPendingCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.buffers)
}
