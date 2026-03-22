package bot

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/tools"
)

// LiveStreamEditor manages rate-limited placeholder edits combining live token streaming
// with tool lifecycle status lines. It is the single source of truth for the ⏳
// placeholder message while a run is active.
//
// Edits are triggered when:
//   - streamTokenThreshold new tokens have accumulated since the last edit, OR
//   - streamEditInterval has elapsed since the last edit (time-based flush for tool events)
//
// After the run completes, call Stop() before performing the final message edit so
// that any in-flight or pending streaming edit does not overwrite the final content.
type LiveStreamEditor struct {
	bot *telebot.Bot
	msg *telebot.Message

	jobID         string
	lifecycle     telegramJobStatus
	mu            sync.Mutex
	toolLines     []toolStatusLine
	content       strings.Builder
	lastEdit      time.Time
	lastEditLen   int // content length at last edit
	pendingTokens int // token-appends since last edit
	pending       bool
	stopped       bool
}

// NewLiveStreamEditor creates a LiveStreamEditor that updates msg as the run progresses.
func NewLiveStreamEditor(bot *telebot.Bot, msg *telebot.Message, jobID string) *LiveStreamEditor {
	return &LiveStreamEditor{
		bot:       bot,
		msg:       msg,
		jobID:     jobID,
		lifecycle: jobStatusRunning,
	}
}

// OnToolEvent records a tool lifecycle event and schedules a time-based placeholder edit.
func (e *LiveStreamEditor) OnToolEvent(event agent.ToolEvent) {
	e.mu.Lock()
	switch event.Type {
	case agent.ToolEventStarted:
		e.toolLines = append(e.toolLines, toolStatusLine{name: event.ToolName})
	case agent.ToolEventFinished:
		for i := len(e.toolLines) - 1; i >= 0; i-- {
			if e.toolLines[i].name == event.ToolName && !e.toolLines[i].done && !e.toolLines[i].failed {
				if event.Err != nil {
					e.toolLines[i].failed = true
					if denial := tools.IsToolDenial(event.Err); denial != nil {
						e.toolLines[i].denialMsg = denial.FormatTelegram()
					}
				} else {
					e.toolLines[i].done = true
				}
				break
			}
		}
	}
	if !e.pending && !e.stopped {
		e.pending = true
		go e.scheduleEdit()
	}
	e.mu.Unlock()
}

// AppendDelta appends a streamed text token and schedules an edit.
// When streamTokenThreshold tokens have accumulated an immediate edit is triggered.
func (e *LiveStreamEditor) AppendDelta(delta string) {
	e.mu.Lock()
	e.content.WriteString(delta)
	e.pendingTokens++
	if !e.pending && !e.stopped {
		e.pending = true
		go e.scheduleEdit()
	}
	e.mu.Unlock()
}

// ResetContent discards accumulated streaming content.
// Called when the model emits tool calls after some streamed text so the
// intermediate text does not persist in the placeholder.
func (e *LiveStreamEditor) ResetContent() {
	e.mu.Lock()
	e.content.Reset()
	e.pendingTokens = 0
	e.lastEditLen = 0
	e.mu.Unlock()
}

// Stop signals the editor to cease scheduling new edits.
// It should be called before performing the final message edit so a pending
// streaming update does not overwrite the finalized content.
// Stop does not wait for any in-progress Telegram API call to complete; callers
// that need strict ordering should add a small delay or use a WaitGroup.
func (e *LiveStreamEditor) Stop() {
	e.mu.Lock()
	e.stopped = true
	e.pending = false
	e.mu.Unlock()
}

// HasAny reports whether at least one tool event has been recorded.
func (e *LiveStreamEditor) HasAny() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.toolLines) > 0
}

// formatLocked returns the current display text.
// Must be called with e.mu held.
func (e *LiveStreamEditor) formatLocked() string {
	header := formatTelegramJobHeader(e.jobID, e.lifecycle)
	statusPart := e.formatStatusLocked()
	contentPart := e.content.String()

	var body string
	switch {
	case contentPart == "" && statusPart == "":
		body = "💭 Working…"
	case contentPart == "":
		body = statusPart
	case statusPart != "":
		body = statusPart + "\n\n" + contentPart
	default:
		body = contentPart
	}

	if header == "" {
		return body
	}
	return header + "\n\n" + body
}

// formatStatusLocked formats the tool status lines.
// Must be called with e.mu held.
func (e *LiveStreamEditor) formatStatusLocked() string {
	if len(e.toolLines) == 0 {
		return ""
	}
	start := 0
	if len(e.toolLines) > maxToolStatusLines {
		start = len(e.toolLines) - maxToolStatusLines
	}
	var sb strings.Builder
	for i := start; i < len(e.toolLines); i++ {
		l := e.toolLines[i]
		switch {
		case l.denialMsg != "":
			sb.WriteString(fmt.Sprintf("🚫 %s\n", l.denialMsg))
		case l.failed:
			sb.WriteString(fmt.Sprintf("❌ %s\n", l.name))
		case l.done:
			sb.WriteString(fmt.Sprintf("✅ %s\n", l.name))
		default:
			sb.WriteString(fmt.Sprintf("⚙️ %s…\n", l.name))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// scheduleEdit performs rate-limited edits of the placeholder message.
// It re-runs until there is no pending content left to push.
func (e *LiveStreamEditor) scheduleEdit() {
	for {
		e.mu.Lock()
		if !e.pending || e.stopped {
			e.mu.Unlock()
			return
		}

		elapsed := time.Since(e.lastEdit)
		tokensBurst := e.pendingTokens >= streamTokenThreshold

		// Wait for the interval unless the token threshold has been reached.
		if !tokensBurst && elapsed < streamEditInterval {
			e.mu.Unlock()
			time.Sleep(streamEditInterval - elapsed)
			continue
		}

		// Snapshot state and clear pending counters.
		content := e.formatLocked()
		e.pending = false
		e.lastEdit = time.Now()
		e.lastEditLen = e.content.Len()
		e.pendingTokens = 0
		stopped := e.stopped
		msg := e.msg
		e.mu.Unlock()

		// Perform the Telegram edit without holding the lock.
		if !stopped && msg != nil && content != "" {
			e.bot.Edit(msg, content) //nolint:errcheck
		}

		// Re-schedule if new content arrived during the edit.
		e.mu.Lock()
		if e.stopped || e.content.Len() <= e.lastEditLen {
			e.mu.Unlock()
			return
		}
		e.pending = true
		e.mu.Unlock()
	}
}

// Flush performs an immediate lifecycle/status edit without waiting for the scheduler.
func (e *LiveStreamEditor) Flush() {
	e.mu.Lock()
	defer e.mu.Unlock()

	content := e.formatLocked()
	if e.msg != nil && content != "" {
		e.bot.Edit(e.msg, content) //nolint:errcheck
	}
}
