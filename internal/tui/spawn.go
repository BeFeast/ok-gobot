// Package tui provides a terminal user interface for ok-gobot session management.
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SubagentSpawnRequest holds all fields required to spawn a sub-agent session.
type SubagentSpawnRequest struct {
	Task          string   // task description for the sub-agent
	Model         string   // model identifier, e.g. "anthropic/claude-3-5-sonnet"
	ThinkingLevel string   // "off", "low", "medium", "high"
	AllowedTools  []string // tool names the sub-agent is permitted to use
	WorkspaceRoot string   // absolute path to the workspace root
}

// spawnConfirmedMsg is sent when the user submits the spawn form.
type spawnConfirmedMsg struct {
	req SubagentSpawnRequest
}

// spawnCancelledMsg is sent when the user cancels the spawn form.
type spawnCancelledMsg struct{}

// field indices in the spawn dialog.
const (
	fieldTask = iota
	fieldModel
	fieldThinking
	fieldTools
	fieldWorkspace
	fieldCount
)

var fieldLabels = [fieldCount]string{
	"Task description",
	"Model",
	"Thinking level (off/low/medium/high)",
	"Allowed tools (comma-separated)",
	"Workspace root",
}

var thinkingLevels = []string{"off", "low", "medium", "high"}

// SpawnDialog is the Bubble Tea model for the sub-agent spawn form dialog.
type SpawnDialog struct {
	inputs  [fieldCount]textinput.Model
	focused int
}

// NewSpawnDialog creates a new SpawnDialog with sensible defaults.
func NewSpawnDialog() SpawnDialog {
	var inputs [fieldCount]textinput.Model
	for i := range inputs {
		t := textinput.New()
		t.CharLimit = 512
		inputs[i] = t
	}

	inputs[fieldTask].Placeholder = "Describe the task for the sub-agent"
	inputs[fieldTask].Focus()

	inputs[fieldModel].Placeholder = "anthropic/claude-3-5-sonnet"

	inputs[fieldThinking].Placeholder = "off"

	inputs[fieldTools].Placeholder = "local,file,grep (leave empty for all)"

	inputs[fieldWorkspace].Placeholder = "/path/to/workspace"

	return SpawnDialog{
		inputs:  inputs,
		focused: fieldTask,
	}
}

// Init implements tea.Model.
func (d SpawnDialog) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (d SpawnDialog) Update(msg tea.Msg) (SpawnDialog, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			return d, func() tea.Msg { return spawnCancelledMsg{} }

		case tea.KeyTab, tea.KeyDown:
			d.inputs[d.focused].Blur()
			d.focused = (d.focused + 1) % fieldCount
			d.inputs[d.focused].Focus()
			return d, textinput.Blink

		case tea.KeyShiftTab, tea.KeyUp:
			d.inputs[d.focused].Blur()
			d.focused = (d.focused - 1 + fieldCount) % fieldCount
			d.inputs[d.focused].Focus()
			return d, textinput.Blink

		case tea.KeyEnter:
			if d.focused < fieldCount-1 {
				// advance to next field
				d.inputs[d.focused].Blur()
				d.focused++
				d.inputs[d.focused].Focus()
				return d, textinput.Blink
			}
			// last field — submit
			return d, d.submit()
		}
	}

	// Forward key events to the focused input.
	var cmd tea.Cmd
	d.inputs[d.focused], cmd = d.inputs[d.focused].Update(msg)
	return d, cmd
}

// submit constructs the SubagentSpawnRequest and emits spawnConfirmedMsg.
func (d SpawnDialog) submit() tea.Cmd {
	req := SubagentSpawnRequest{
		Task:          strings.TrimSpace(d.inputs[fieldTask].Value()),
		Model:         strings.TrimSpace(d.inputs[fieldModel].Value()),
		ThinkingLevel: strings.TrimSpace(d.inputs[fieldThinking].Value()),
		WorkspaceRoot: strings.TrimSpace(d.inputs[fieldWorkspace].Value()),
	}

	// Validate / normalise ThinkingLevel.
	req.ThinkingLevel = normaliseThinkingLevel(req.ThinkingLevel)

	// Parse tool allowlist.
	rawTools := strings.TrimSpace(d.inputs[fieldTools].Value())
	if rawTools != "" {
		parts := strings.Split(rawTools, ",")
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				req.AllowedTools = append(req.AllowedTools, t)
			}
		}
	}

	return func() tea.Msg {
		return spawnConfirmedMsg{req: req}
	}
}

// View implements tea.Model.
func (d SpawnDialog) View() string {
	var sb strings.Builder

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("212")).
		MarginBottom(1)

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))

	focusedLabelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("212")).
		Bold(true)

	sb.WriteString(titleStyle.Render("Spawn Sub-Agent"))
	sb.WriteString("\n")

	for i, input := range d.inputs {
		label := fieldLabels[i]
		if i == d.focused {
			sb.WriteString(focusedLabelStyle.Render("> "+label) + "\n")
		} else {
			sb.WriteString(labelStyle.Render("  "+label) + "\n")
		}
		sb.WriteString("  " + input.View() + "\n\n")
	}

	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	sb.WriteString(helpStyle.Render("Tab/↓ next  Shift+Tab/↑ prev  Enter confirm  Esc cancel"))

	return sb.String()
}

// normaliseThinkingLevel returns a valid thinking level or "off".
func normaliseThinkingLevel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, v := range thinkingLevels {
		if s == v {
			return s
		}
	}
	return "off"
}
