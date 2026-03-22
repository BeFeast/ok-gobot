package bot

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
)

const maxToolStatusLines = 5

// toolStatusLine tracks the state of a single tool invocation
type toolStatusLine struct {
	name       string
	done       bool
	failed     bool
	denied     bool
	denyReason string // short reason shown inline, e.g. "estop active"
}

// ToolStatusTracker records tool started/finished events and formats them as status lines
type ToolStatusTracker struct {
	mu    sync.Mutex
	lines []toolStatusLine
}

// OnStarted records a tool as started
func (t *ToolStatusTracker) OnStarted(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, toolStatusLine{name: name})
}

// OnFinished marks the last pending entry for name as done or failed
func (t *ToolStatusTracker) OnFinished(name string, failed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.lines) - 1; i >= 0; i-- {
		if t.lines[i].name == name && !t.lines[i].done && !t.lines[i].failed {
			t.lines[i].done = !failed
			t.lines[i].failed = failed
			return
		}
	}
}

// HasAny returns true if at least one tool event has been recorded
func (t *ToolStatusTracker) HasAny() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.lines) > 0
}

// Format returns compact status lines, showing at most maxToolStatusLines recent entries
func (t *ToolStatusTracker) Format() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.lines) == 0 {
		return "💭 Working…"
	}

	start := 0
	if len(t.lines) > maxToolStatusLines {
		start = len(t.lines) - maxToolStatusLines
	}

	var sb strings.Builder
	for i := start; i < len(t.lines); i++ {
		l := t.lines[i]
		switch {
		case l.denied:
			if l.denyReason != "" {
				sb.WriteString(fmt.Sprintf("🚫 %s (%s)\n", l.name, l.denyReason))
			} else {
				sb.WriteString(fmt.Sprintf("🚫 %s\n", l.name))
			}
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

// PlaceholderEditor manages rate-limited edits to a placeholder Telegram message
// based on tool execution events. It embeds a ToolStatusTracker.
type PlaceholderEditor struct {
	bot         *telebot.Bot
	msg         *telebot.Message
	tracker     ToolStatusTracker
	mu          sync.Mutex
	lastEdit    time.Time
	minInterval time.Duration
	pending     bool
}

// NewPlaceholderEditor creates a PlaceholderEditor that will update msg as tools run
func NewPlaceholderEditor(bot *telebot.Bot, msg *telebot.Message) *PlaceholderEditor {
	return &PlaceholderEditor{
		bot:         bot,
		msg:         msg,
		minInterval: 1 * time.Second,
	}
}

// OnToolEvent handles a tool lifecycle event and schedules a message update
func (p *PlaceholderEditor) OnToolEvent(event agent.ToolEvent) {
	switch event.Type {
	case agent.ToolEventStarted:
		p.tracker.OnStarted(event.ToolName)
	case agent.ToolEventFinished:
		p.tracker.OnFinished(event.ToolName, event.Err != nil)
	}
	p.schedule()
}

// HasAny returns true if at least one tool event has been received
func (p *PlaceholderEditor) HasAny() bool {
	return p.tracker.HasAny()
}

// schedule queues a rate-limited edit of the placeholder message
func (p *PlaceholderEditor) schedule() {
	p.mu.Lock()
	if p.pending {
		p.mu.Unlock()
		return
	}
	p.pending = true
	p.mu.Unlock()

	go func() {
		p.mu.Lock()
		elapsed := time.Since(p.lastEdit)
		if elapsed < p.minInterval {
			wait := p.minInterval - elapsed
			p.mu.Unlock()
			time.Sleep(wait)
			p.mu.Lock()
		}
		content := p.tracker.Format()
		p.pending = false
		p.lastEdit = time.Now()
		msg := p.msg
		p.mu.Unlock()

		if msg != nil && content != "" {
			p.bot.Edit(msg, content)
		}
	}()
}

// Flush performs a final synchronous edit with the current tracker state
func (p *PlaceholderEditor) Flush() {
	content := p.tracker.Format()
	if p.msg != nil && content != "" {
		p.bot.Edit(p.msg, content)
	}
}
