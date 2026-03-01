package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SessionStatus describes the current state of a session.
type SessionStatus string

const (
	StatusIdle   SessionStatus = "idle"
	StatusActive SessionStatus = "active"
	StatusQueued SessionStatus = "queued"
)

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

// Init implements tea.Model.
func (m AppModel) Init() tea.Cmd {
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
	}

	return m, nil
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
