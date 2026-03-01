package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ok-gobot/internal/runtime"
)

// SessionStatus describes the current state of a session.
type SessionStatus string

const (
	StatusIdle   SessionStatus = "idle"
	StatusActive SessionStatus = "active"
	StatusQueued SessionStatus = "queued"
	// StatusDone is set on a sub-agent session after its run completes.
	StatusDone SessionStatus = "done"
)

// HubEventMsg wraps a runtime.RuntimeEvent for delivery to the Bubble Tea model.
type HubEventMsg runtime.RuntimeEvent

// listenHub returns a tea.Cmd that blocks until the next event arrives on ch,
// then delivers it as a HubEventMsg. Returns nil when the channel is closed.
func listenHub(ch <-chan runtime.RuntimeEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return HubEventMsg(ev)
	}
}

// SessionEntry represents a single session in the session picker.
type SessionEntry struct {
	Key        string // canonical session key
	Label      string // human-readable label
	Status     SessionStatus
	IsSubagent bool
	SpawnedAt  time.Time
}

// AppModel is the root Bubble Tea model for the ok-gobot TUI.
// It combines a session picker list with an optional spawn dialog overlay.
type AppModel struct {
	sessions    []SessionEntry
	selectedIdx int
	showDialog  bool
	dialog      SpawnDialog
	width       int
	height      int
	statusMsg   string
	hubCh       <-chan runtime.RuntimeEvent // nil if no hub connected
}

// NewAppModel creates an AppModel pre-populated with an initial session list.
func NewAppModel(sessions []SessionEntry) AppModel {
	if len(sessions) == 0 {
		sessions = []SessionEntry{
			{
				Key:    "agent:default:main",
				Label:  "main",
				Status: StatusIdle,
			},
		}
	}
	return AppModel{sessions: sessions}
}

// WithHub returns a copy of m subscribed to hub for runtime events.
// The caller is responsible for calling hub.Unsubscribe with the returned
// channel when the TUI exits, if cleanup is required.
func (m AppModel) WithHub(hub *runtime.Hub) AppModel {
	ch := make(chan runtime.RuntimeEvent, 64)
	hub.Subscribe(ch)
	m.hubCh = ch
	return m
}

// Init implements tea.Model.
func (m AppModel) Init() tea.Cmd {
	if m.hubCh != nil {
		return listenHub(m.hubCh)
	}
	return nil
}

// Update implements tea.Model.
func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// When the spawn dialog is open, forward all messages to it.
	if m.showDialog {
		return m.updateDialog(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "n":
			m.dialog = NewSpawnDialog()
			m.showDialog = true
			return m, m.dialog.Init()
		case "j", "down":
			if m.selectedIdx < len(m.sessions)-1 {
				m.selectedIdx++
			}
		case "k", "up":
			if m.selectedIdx > 0 {
				m.selectedIdx--
			}
		case "g":
			m.selectedIdx = 0
		case "G":
			m.selectedIdx = len(m.sessions) - 1
		}

	case HubEventMsg:
		m = m.applyHubEvent(runtime.RuntimeEvent(msg))
		if m.hubCh != nil {
			return m, listenHub(m.hubCh)
		}
		return m, nil
	}

	return m, nil
}

// applyHubEvent updates session statuses and parent notifications based on a
// runtime event. It returns the updated model.
func (m AppModel) applyHubEvent(ev runtime.RuntimeEvent) AppModel {
	switch ev.Type {
	case runtime.EventActive:
		m.sessions = setSessionStatus(m.sessions, ev.SessionKey, StatusActive)

	case runtime.EventQueued:
		m.sessions = setSessionStatus(m.sessions, ev.SessionKey, StatusQueued)

	case runtime.EventDone, runtime.EventError:
		m.sessions = setSessionStatus(m.sessions, ev.SessionKey, StatusIdle)

	case runtime.EventChildDone:
		payload, ok := ev.Payload.(runtime.ChildCompletionPayload)
		if !ok {
			break
		}
		// Mark child session as done (it remains browsable in the picker).
		m.sessions = setSessionStatus(m.sessions, payload.ChildSessionKey, StatusDone)
		// Insert a completion card after the parent session entry.
		card := SessionEntry{
			Key:        "notify:" + ev.SessionKey + ":" + payload.ChildSessionKey,
			Label:      "✓ Sub-agent completed: " + shortSessionKey(payload.ChildSessionKey),
			Status:     StatusDone,
			IsSubagent: false,
			SpawnedAt:  time.Now(),
		}
		m.sessions = insertAfterKey(m.sessions, ev.SessionKey, card)
		m.statusMsg = fmt.Sprintf("✓ Sub-agent done: %s", shortSessionKey(payload.ChildSessionKey))

	case runtime.EventChildFailed:
		payload, ok := ev.Payload.(runtime.ChildCompletionPayload)
		if !ok {
			break
		}
		m.sessions = setSessionStatus(m.sessions, payload.ChildSessionKey, StatusDone)
		errStr := "unknown error"
		if payload.Err != nil {
			errStr = payload.Err.Error()
		}
		card := SessionEntry{
			Key:        "notify:" + ev.SessionKey + ":" + payload.ChildSessionKey,
			Label:      "✗ Sub-agent failed: " + shortSessionKey(payload.ChildSessionKey),
			Status:     StatusDone,
			IsSubagent: false,
			SpawnedAt:  time.Now(),
		}
		m.sessions = insertAfterKey(m.sessions, ev.SessionKey, card)
		m.statusMsg = fmt.Sprintf("✗ Sub-agent failed (%s): %s", shortSessionKey(payload.ChildSessionKey), errStr)
	}
	return m
}

// setSessionStatus returns a copy of entries with the status of the entry
// matching key updated to status. No-op if the key is not found.
func setSessionStatus(entries []SessionEntry, key string, status SessionStatus) []SessionEntry {
	for i, s := range entries {
		if s.Key == key {
			entries[i].Status = status
			return entries
		}
	}
	return entries
}

// insertAfterKey inserts card immediately after the first entry with Key == afterKey.
// If afterKey is not found, card is appended to the end.
func insertAfterKey(entries []SessionEntry, afterKey string, card SessionEntry) []SessionEntry {
	for i, s := range entries {
		if s.Key == afterKey {
			result := make([]SessionEntry, 0, len(entries)+1)
			result = append(result, entries[:i+1]...)
			result = append(result, card)
			result = append(result, entries[i+1:]...)
			return result
		}
	}
	return append(entries, card)
}

// shortSessionKey returns the last segment of a colon-separated session key,
// truncated to 30 characters.
func shortSessionKey(key string) string {
	parts := strings.Split(key, ":")
	short := parts[len(parts)-1]
	if len(short) > 30 {
		short = short[:27] + "..."
	}
	return short
}

// updateDialog handles messages when the spawn dialog is active.
func (m AppModel) updateDialog(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case spawnConfirmedMsg:
		req := msg.(spawnConfirmedMsg).req
		entry := m.sessionFromRequest(req)
		m.sessions = append(m.sessions, entry)
		m.selectedIdx = len(m.sessions) - 1
		m.showDialog = false
		m.statusMsg = fmt.Sprintf("Spawned: %s", entry.Label)
		return m, nil

	case spawnCancelledMsg:
		m.showDialog = false
		m.statusMsg = "Spawn cancelled"
		return m, nil
	}

	// Forward to dialog.
	var cmd tea.Cmd
	m.dialog, cmd = m.dialog.Update(msg)
	return m, cmd
}

// sessionFromRequest creates a SessionEntry from a spawn request.
func (m AppModel) sessionFromRequest(req SubagentSpawnRequest) SessionEntry {
	slug := fmt.Sprintf("run-%d", time.Now().UnixMilli())
	label := req.Task
	if len(label) > 40 {
		label = label[:37] + "..."
	}
	if label == "" {
		label = slug
	}
	return SessionEntry{
		Key:        fmt.Sprintf("agent:default:subagent:%s", slug),
		Label:      label,
		Status:     StatusActive,
		IsSubagent: true,
		SpawnedAt:  time.Now(),
	}
}

// View implements tea.Model.
func (m AppModel) View() string {
	if m.showDialog {
		return m.dialogOverlay()
	}
	return m.sessionListView()
}

// sessionListView renders the session picker.
func (m AppModel) sessionListView() string {
	var sb strings.Builder

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("212")).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		Width(60).
		MarginBottom(1)

	sb.WriteString(headerStyle.Render("ok-gobot — Sessions"))
	sb.WriteString("\n")

	selectedStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("212")).
		Bold(true).
		PaddingLeft(1).PaddingRight(1)

	normalStyle := lipgloss.NewStyle().
		PaddingLeft(1).PaddingRight(1)

	subagentPrefixStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("33"))

	for i, s := range m.sessions {
		label := s.Label
		if s.IsSubagent {
			label = subagentPrefixStyle.Render("↳ ") + label
		}

		statusBadge := statusBadge(s.Status)
		line := fmt.Sprintf("%-44s %s", label, statusBadge)

		if i == m.selectedIdx {
			sb.WriteString(selectedStyle.Render(line))
		} else {
			sb.WriteString(normalStyle.Render(line))
		}
		sb.WriteString("\n")
	}

	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		MarginTop(1)

	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("n spawn  j/k navigate  q quit"))

	if m.statusMsg != "" {
		msgStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).MarginTop(1)
		sb.WriteString("\n")
		sb.WriteString(msgStyle.Render(m.statusMsg))
	}

	return sb.String()
}

// dialogOverlay renders the spawn dialog centred over the session list.
func (m AppModel) dialogOverlay() string {
	dialogContent := m.dialog.View()

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("212")).
		Padding(1, 2).
		Width(66)

	box := boxStyle.Render(dialogContent)

	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height,
			lipgloss.Center, lipgloss.Center, box)
	}

	return box
}

// statusBadge returns a coloured status indicator string.
func statusBadge(s SessionStatus) string {
	switch s {
	case StatusActive:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("● active")
	case StatusQueued:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("◌ queued")
	case StatusDone:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("33")).Render("✓ done")
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("○ idle")
	}
}

// Run launches the TUI, blocking until the user quits.
func Run(initial []SessionEntry) error {
	m := NewAppModel(initial)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
