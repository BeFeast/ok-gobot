package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type completionCommand struct {
	name        string
	description string
}

type completionState struct {
	visible  bool
	query    string
	items    []completionCommand
	selected int
}

var commandCompletions = []completionCommand{
	{name: "/status", description: "bot status, model, uptime"},
	{name: "/usage", description: "token usage stats"},
	{name: "/context", description: "context window info"},
	{name: "/whoami", description: "your user info"},
	{name: "/commands", description: "show available commands"},
	{name: "/think", description: "set thinking level"},
	{name: "/compact", description: "compact context window"},
	{name: "/model", description: "set or pick model"},
	{name: "/new", description: "start new session"},
	{name: "/abort", description: "abort active run"},
	{name: "/stop", description: "alias for /abort"},
}

func (m *Model) updateCompletion() {
	query, ok := parseCompletionQuery(m.input.Value())
	if !ok {
		m.hideCompletion()
		return
	}

	filtered := filterCommandCompletions(query)
	m.completion.visible = true
	m.completion.query = query
	m.completion.items = filtered
	if len(filtered) == 0 {
		m.completion.selected = 0
		return
	}
	if m.completion.selected >= len(filtered) {
		m.completion.selected = 0
	}
}

func (m *Model) hideCompletion() {
	m.completion.visible = false
	m.completion.query = ""
	m.completion.items = nil
	m.completion.selected = 0
}

func (m *Model) moveCompletion(delta int) {
	if !m.completion.visible || len(m.completion.items) == 0 {
		return
	}
	next := m.completion.selected + delta
	if next < 0 {
		next = len(m.completion.items) - 1
	}
	if next >= len(m.completion.items) {
		next = 0
	}
	m.completion.selected = next
}

func (m *Model) applyCompletion() bool {
	if !m.completion.visible || len(m.completion.items) == 0 {
		return false
	}
	value := m.completion.items[m.completion.selected].name + " "
	m.input.SetValue(value)
	m.input.CursorEnd()
	m.hideCompletion()
	return true
}

func (m *Model) renderCompletionPopup() string {
	if m.screen != screenChat || !m.completion.visible {
		return ""
	}

	innerWidth := m.width - 6
	if innerWidth < 20 {
		innerWidth = 20
	}

	title := completionTitleStyle.Width(innerWidth).Render("Commands")

	var rows []string
	if len(m.completion.items) == 0 {
		rows = append(rows, completionEmptyStyle.Width(innerWidth).Render("No matching commands"))
	} else {
		cmdWidth := 0
		for _, item := range m.completion.items {
			if w := lipgloss.Width(item.name); w > cmdWidth {
				cmdWidth = w
			}
		}
		descWidth := innerWidth - cmdWidth - 2
		if descWidth < 10 {
			descWidth = 10
		}

		for i, item := range m.completion.items {
			cmdStyle := completionCommandStyle
			descStyle := completionDescStyle
			rowStyle := completionItemStyle
			if i == m.completion.selected {
				cmdStyle = completionCommandSelectedStyle
				descStyle = completionDescSelectedStyle
				rowStyle = completionItemSelectedStyle
			}

			cmd := cmdStyle.Width(cmdWidth).Render(item.name)
			desc := descStyle.Width(descWidth).Render(truncate(item.description, descWidth))
			row := rowStyle.Width(innerWidth).Render(lipgloss.JoinHorizontal(lipgloss.Top, cmd, "  ", desc))
			rows = append(rows, row)
		}
	}

	content := lipgloss.JoinVertical(lipgloss.Left, append([]string{title}, rows...)...)
	return completionPopupStyle.Width(m.width - 2).Render(content)
}

func parseCompletionQuery(input string) (string, bool) {
	text := strings.TrimLeft(input, " \t")
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	if strings.ContainsAny(text, " \n\r\t") {
		return "", false
	}
	return strings.ToLower(strings.TrimPrefix(text, "/")), true
}

func filterCommandCompletions(query string) []completionCommand {
	if query == "" {
		return append([]completionCommand(nil), commandCompletions...)
	}
	items := make([]completionCommand, 0, len(commandCompletions))
	for _, cmd := range commandCompletions {
		name := strings.TrimPrefix(strings.ToLower(cmd.name), "/")
		if strings.HasPrefix(name, query) {
			items = append(items, cmd)
		}
	}
	return items
}
