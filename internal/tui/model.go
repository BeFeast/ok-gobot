package tui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	controlserver "ok-gobot/internal/control"
)

// screen tracks which overlay is visible.
type screen int

const (
	screenChat     screen = iota
	screenSessions        // session picker overlay
	screenModels          // model picker overlay
	screenApproval        // approval prompt overlay
	screenSpawn           // sub-agent spawn dialog
)

// chatEntry is one logical item in the chat log.
type chatEntry struct {
	role      string // "user", "assistant", "tool", "error"
	content   string
	toolName  string
	toolArgs  string
	toolRes   string
	toolErr   string
	streaming bool // true while tokens are still arriving
}

// Model is the root Bubble Tea model.
type Model struct {
	// layout
	width  int
	height int

	// state
	screen     screen
	conn       *wsConn
	serverAddr string

	// session management
	sessions      []controlserver.TUISessionInfo
	activeSession string
	running       bool

	// chat log
	entries   []chatEntry
	streamBuf strings.Builder // accumulates live tokens
	streamIdx int             // index in entries of the streaming entry (-1 if none)

	// pending approval
	approvalID  string
	approvalCmd string
	approvalSel int // 0 = yes, 1 = no

	// spawn dialog
	spawnDialog SpawnDialog

	// UI components
	viewport viewport.Model
	input    textarea.Model

	// session/model pickers
	sessionCursor int
	modelCursor   int
	modelList     []string

	// misc
	statusMsg string
	statusAt  time.Time
	lastErr   string
	tick      int
}

// serverMsgReceived carries a decoded ServerMsg into the Bubble Tea loop.
type serverMsgReceived struct {
	msg controlserver.ServerMsg
}

// serverError carries a WebSocket read error.
type serverError struct{ err error }

// tickMsg drives cursor blinking / status timeout.
type tickMsg time.Time

// --- Init ---

func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		m.listenCmd(),
		tickEvery(),
		textarea.Blink,
	)
}

// --- Update ---

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeComponents()
		return m, nil

	case tickMsg:
		m.tick++
		cmds = append(cmds, tickEvery())
		// clear status after 4s
		if !m.statusAt.IsZero() && time.Since(m.statusAt) > 4*time.Second {
			m.statusMsg = ""
		}

	case serverError:
		m.lastErr = fmt.Sprintf("connection error: %v", msg.err)
		// keep listening even after an error
		cmds = append(cmds, m.listenCmd())
		return m, tea.Batch(cmds...)

	case serverMsgReceived:
		cmds = append(cmds, m.handleServerMsg(msg.msg))
		// re-register listener for the next message
		cmds = append(cmds, m.listenCmd())

	case spawnConfirmedMsg:
		m.sendSpawnCmd(msg.req)
		m.screen = screenChat
		m.setStatus("Sub-agent spawned: " + msg.req.Task)
		return m, tea.Batch(cmds...)

	case spawnCancelledMsg:
		m.screen = screenChat
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		return m.handleKey(msg, cmds)
	}

	// Forward key events to input when in chat mode.
	if m.screen == screenChat {
		var inputCmd tea.Cmd
		m.input, inputCmd = m.input.Update(msg)
		cmds = append(cmds, inputCmd)
	}

	// Forward all messages to spawn dialog when it is active.
	if m.screen == screenSpawn {
		var spawnCmd tea.Cmd
		m.spawnDialog, spawnCmd = m.spawnDialog.Update(msg)
		cmds = append(cmds, spawnCmd)
	}

	// Update viewport
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

// handleKey dispatches key presses based on the active screen.
func (m *Model) handleKey(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenApproval:
		return m.handleApprovalKey(msg, cmds)
	case screenSessions:
		return m.handleSessionKey(msg, cmds)
	case screenModels:
		return m.handleModelKey(msg, cmds)
	case screenSpawn:
		return m.handleSpawnKey(msg, cmds)
	default:
		return m.handleChatKey(msg, cmds)
	}
}

func (m *Model) handleChatKey(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "esc":
		// do nothing in chat mode, esc closes overlays
		return m, tea.Batch(cmds...)

	case "ctrl+s", "enter":
		if msg.String() == "enter" && !msg.Alt {
			// Enter without Alt sends message (textarea handles Alt+Enter for newlines)
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				break
			}
			if strings.HasPrefix(text, "/abort") {
				m.sendCmd(controlserver.ClientMsg{
					Type:      controlserver.CmdAbort,
					SessionID: m.activeSession,
				})
				m.setStatus("Abort sent")
			} else if strings.HasPrefix(text, "/new") {
				parts := strings.Fields(text)
				name := ""
				if len(parts) > 1 {
					name = strings.Join(parts[1:], " ")
				}
				m.sendCmd(controlserver.ClientMsg{
					Type: controlserver.CmdNewSession,
					Name: name,
				})
			} else if strings.HasPrefix(text, "/model") {
				parts := strings.Fields(text)
				if len(parts) == 2 {
					m.sendCmd(controlserver.ClientMsg{
						Type:      controlserver.CmdSetModel,
						SessionID: m.activeSession,
						Model:     parts[1],
					})
					m.setStatus("Model override set to " + parts[1])
				} else {
					m.screen = screenModels
					m.modelCursor = 0
				}
			} else if isBotCommand(text) {
				m.sendCmd(controlserver.ClientMsg{
					Type:      controlserver.CmdBotCommand,
					SessionID: m.activeSession,
					Text:      text,
				})
			} else {
				m.sendCmd(controlserver.ClientMsg{
					Type:      controlserver.CmdSend,
					SessionID: m.activeSession,
					Text:      text,
				})
			}
			m.input.Reset()
			return m, tea.Batch(cmds...)
		}

	case "ctrl+n":
		// Open sub-agent spawn dialog
		m.spawnDialog = NewSpawnDialog()
		m.screen = screenSpawn
		return m, tea.Batch(append(cmds, m.spawnDialog.Init())...)

	case "ctrl+p":
		// Open session picker
		m.screen = screenSessions
		m.sessionCursor = 0
		return m, tea.Batch(cmds...)

	case "ctrl+m":
		// Open model picker
		m.screen = screenModels
		m.modelCursor = 0
		return m, tea.Batch(cmds...)

	case "ctrl+a":
		// Abort shortcut
		m.sendCmd(controlserver.ClientMsg{
			Type:      controlserver.CmdAbort,
			SessionID: m.activeSession,
		})
		m.setStatus("Abort sent")
		return m, tea.Batch(cmds...)

	case "ctrl+y":
		// Copy last assistant message to clipboard
		if text := m.lastAssistantText(); text != "" {
			if err := copyToClipboard(text); err != nil {
				m.setStatus("Copy failed: " + err.Error())
			} else {
				m.setStatus("✓ Copied to clipboard")
			}
		}
		return m, tea.Batch(cmds...)

	case "pgup":
		m.viewport.HalfViewUp()
	case "pgdown":
		m.viewport.HalfViewDown()
	}

	// Forward to textarea
	var inputCmd tea.Cmd
	m.input, inputCmd = m.input.Update(msg)
	cmds = append(cmds, inputCmd)
	return m, tea.Batch(cmds...)
}

func (m *Model) handleApprovalKey(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "h":
		m.approvalSel = 0
	case "right", "l":
		m.approvalSel = 1
	case "y", "Y":
		m.approvalSel = 0
		m.submitApproval()
	case "n", "N", "esc":
		m.approvalSel = 1
		m.submitApproval()
	case "enter":
		m.submitApproval()
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) handleSessionKey(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+p":
		m.screen = screenChat
	case "up", "k":
		if m.sessionCursor > 0 {
			m.sessionCursor--
		}
	case "down", "j":
		if m.sessionCursor < len(m.sessions)-1 {
			m.sessionCursor++
		}
	case "enter":
		if m.sessionCursor < len(m.sessions) {
			target := m.sessions[m.sessionCursor]
			m.sendCmd(controlserver.ClientMsg{
				Type:      controlserver.CmdSwitch,
				SessionID: target.ID,
			})
		}
		m.screen = screenChat
	case "n":
		m.sendCmd(controlserver.ClientMsg{
			Type: controlserver.CmdNewSession,
			Name: fmt.Sprintf("Chat %d", len(m.sessions)+1),
		})
		m.screen = screenChat
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) handleModelKey(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+m":
		m.screen = screenChat
	case "up", "k":
		if m.modelCursor > 0 {
			m.modelCursor--
		}
	case "down", "j":
		if m.modelCursor < len(m.modelList)-1 {
			m.modelCursor++
		}
	case "enter":
		if m.modelCursor < len(m.modelList) {
			model := m.modelList[m.modelCursor]
			m.sendCmd(controlserver.ClientMsg{
				Type:      controlserver.CmdSetModel,
				SessionID: m.activeSession,
				Model:     model,
			})
			m.setStatus("Model set to " + model)
		}
		m.screen = screenChat
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, tea.Batch(cmds...)
}

// handleSpawnKey forwards key events to the spawn dialog.
func (m *Model) handleSpawnKey(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	var spawnCmd tea.Cmd
	m.spawnDialog, spawnCmd = m.spawnDialog.Update(msg)
	return m, tea.Batch(append(cmds, spawnCmd)...)
}

// handleServerMsg processes an incoming server event.
func (m *Model) handleServerMsg(msg controlserver.ServerMsg) tea.Cmd {
	switch msg.Type {
	case controlserver.MsgTypeConnected:
		m.activeSession = msg.SessionID
		if len(msg.Sessions) > 0 {
			m.sessions = msg.Sessions
		}

	case controlserver.MsgTypeSessions:
		m.sessions = msg.Sessions

	case controlserver.MsgTypeError:
		m.addEntry(chatEntry{role: "error", content: msg.Message})
		m.refreshViewport()

	case controlserver.MsgTypeEvent:
		return m.handleEvent(msg)
	}
	return nil
}

// handleEvent processes an event-type server message.
func (m *Model) handleEvent(msg controlserver.ServerMsg) tea.Cmd {
	switch msg.Kind {
	case controlserver.KindRunStart:
		m.running = true
		// start a streaming entry
		m.streamBuf.Reset()
		m.streamIdx = len(m.entries)
		m.entries = append(m.entries, chatEntry{
			role:      "assistant",
			streaming: true,
		})
		m.refreshViewport()

	case controlserver.KindRunEnd:
		m.running = false
		// finalise the streaming entry
		if m.streamIdx >= 0 && m.streamIdx < len(m.entries) {
			m.entries[m.streamIdx].streaming = false
		}
		m.streamIdx = -1
		m.refreshViewport()

	case controlserver.KindToken:
		m.streamBuf.WriteString(msg.Content)
		if m.streamIdx >= 0 && m.streamIdx < len(m.entries) {
			m.entries[m.streamIdx].content = m.streamBuf.String()
		}
		m.refreshViewport()

	case controlserver.KindMessage:
		if msg.Role == "assistant" {
			if m.streamIdx >= 0 {
				// finalise the active streaming entry
				if m.streamIdx < len(m.entries) {
					m.entries[m.streamIdx].content = msg.Content
					m.entries[m.streamIdx].streaming = false
				}
				m.streamIdx = -1
			} else {
				// direct (non-streaming) assistant message — e.g. /status response
				m.addEntry(chatEntry{role: "assistant", content: msg.Content})
			}
		} else if msg.Role == "user" {
			m.addEntry(chatEntry{role: "user", content: msg.Content})
		}
		m.refreshViewport()

	case controlserver.KindToolStart:
		m.addEntry(chatEntry{
			role:     "tool",
			toolName: msg.ToolName,
			toolArgs: msg.ToolArgs,
		})
		m.refreshViewport()

	case controlserver.KindToolEnd:
		// Find the matching tool entry and add result
		for i := len(m.entries) - 1; i >= 0; i-- {
			if m.entries[i].role == "tool" && m.entries[i].toolName == msg.ToolName {
				m.entries[i].toolRes = msg.ToolResult
				m.entries[i].toolErr = msg.ToolError
				break
			}
		}
		m.refreshViewport()

	case controlserver.KindError:
		m.addEntry(chatEntry{role: "error", content: msg.Message})
		m.refreshViewport()

	case controlserver.KindApproval:
		m.approvalID = msg.ApprovalID
		m.approvalCmd = msg.Command
		m.approvalSel = 0
		m.screen = screenApproval

	case controlserver.KindChildDone:
		label := msg.ChildSessionKey
		if label == "" {
			label = "sub-agent"
		}
		m.addEntry(chatEntry{role: "system", content: fmt.Sprintf("Sub-agent completed: %s", label)})
		m.refreshViewport()

	case controlserver.KindChildFailed:
		label := msg.ChildSessionKey
		if label == "" {
			label = "sub-agent"
		}
		errText := msg.Message
		if errText == "" {
			errText = "unknown error"
		}
		m.addEntry(chatEntry{role: "error", content: fmt.Sprintf("Sub-agent failed (%s): %s", label, errText)})
		m.refreshViewport()
	}
	return nil
}

// --- View ---

func (m *Model) View() string {
	if m.width == 0 {
		return "Loading…"
	}

	header := m.renderHeader()
	status := m.renderStatus()

	// Reserve lines for header (1), status (1), input border + textarea
	inputHeight := m.inputAreaHeight()
	chatHeight := m.height - lipgloss.Height(header) - lipgloss.Height(status) - inputHeight

	if chatHeight < 2 {
		chatHeight = 2
	}
	m.viewport.Height = chatHeight

	chat := m.viewport.View()
	input := m.renderInput()

	base := lipgloss.JoinVertical(lipgloss.Left,
		header,
		chat,
		input,
		status,
	)

	// Overlay screens
	switch m.screen {
	case screenApproval:
		return m.overlayApproval(base)
	case screenSessions:
		return m.overlaySessionList(base)
	case screenModels:
		return m.overlayModelList(base)
	case screenSpawn:
		return m.overlaySpawnDialog(base)
	}

	return base
}

// renderHeader renders the top status bar.
func (m *Model) renderHeader() string {
	// Model info
	model := "unknown"
	for _, s := range m.sessions {
		if s.ID == m.activeSession {
			model = s.Model
			break
		}
	}

	runIndicator := ""
	if m.running {
		spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		runIndicator = " " + spinner[m.tick%len(spinner)]
	}

	left := headerStyle.Render("🦞 ok-gobot" + runIndicator)
	mid := headerDimStyle.Render("model: " + model)
	right := headerDimStyle.Render("Ctrl+P sessions · Ctrl+M model · Ctrl+A abort")

	midWidth := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if midWidth < 0 {
		midWidth = 0
	}
	mid = lipgloss.NewStyle().
		Background(lipgloss.Color("17")).
		Foreground(lipgloss.Color("240")).
		Width(midWidth).
		Render(mid)

	return lipgloss.JoinHorizontal(lipgloss.Top, left, mid, right)
}

// renderStatus renders the bottom status bar.
func (m *Model) renderStatus() string {
	sessionName := ""
	for _, s := range m.sessions {
		if s.ID == m.activeSession {
			sessionName = s.Name
			break
		}
	}

	left := statusKeyStyle.Render("session")
	leftVal := statusValueStyle.Render(" " + sessionName + " ")

	var errPart string
	if m.lastErr != "" {
		errPart = inlineErrorStyle.Render(" " + m.lastErr)
	}

	statusText := m.statusMsg
	if statusText == "" {
		statusText = "/abort · /new · /commands · Ctrl+Y copy · Ctrl+N spawn · enter to send"
	}
	hint := statusBarStyle.Width(m.width - lipgloss.Width(left) - lipgloss.Width(leftVal) - lipgloss.Width(errPart)).
		Render(statusText)

	return lipgloss.JoinHorizontal(lipgloss.Top, left, leftVal, hint, errPart)
}

// renderInput renders the text input area.
func (m *Model) renderInput() string {
	borderStyle := inputBorderStyle
	if m.screen == screenChat {
		borderStyle = inputBorderFocusStyle
	}
	return borderStyle.Width(m.width - 2).Render(m.input.View())
}

// renderChatLog builds the full chat log string for the viewport.
func (m *Model) renderChatLog() string {
	var sb strings.Builder
	for i, e := range m.entries {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(m.renderEntry(e))
	}
	return sb.String()
}

// renderEntry renders one chat entry.
func (m *Model) renderEntry(e chatEntry) string {
	switch e.role {
	case "user":
		label := userLabelStyle.Render("You")
		msg := userMsgStyle.Render(wrapText(e.content, m.width-6))
		return label + "\n" + msg

	case "assistant":
		label := botLabelStyle.Render("Bot")
		text := e.content
		cursor := ""
		if e.streaming {
			cursor = streamingCursorStyle.Render("█")
		}
		msg := botMsgStyle.Render(wrapText(text, m.width-6))
		return label + "\n" + msg + cursor

	case "tool":
		return m.renderToolCard(e)

	case "system":
		return systemMsgStyle.Render("ℹ " + e.content)

	case "error":
		return inlineErrorStyle.Render("⚠ " + e.content)
	}
	return e.content
}

// renderToolCard renders a tool invocation card.
func (m *Model) renderToolCard(e chatEntry) string {
	var sb strings.Builder
	// Show spinner for in-progress tools (no result and no error yet)
	inProgress := e.toolRes == "" && e.toolErr == ""
	prefix := "⚙ "
	if inProgress {
		spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		prefix = spinners[m.tick%len(spinners)] + " "
	}
	sb.WriteString(toolNameStyle.Render(prefix + e.toolName))
	if e.toolArgs != "" {
		sb.WriteString("\n" + toolArgStyle.Render("  args: "+truncate(e.toolArgs, 120)))
	}
	if inProgress {
		sb.WriteString("\n" + toolArgStyle.Render("  running…"))
	}
	if e.toolRes != "" {
		sb.WriteString("\n" + toolResultStyle.Render("  → "+truncate(e.toolRes, 200)))
	}
	if e.toolErr != "" {
		sb.WriteString("\n" + toolErrorStyle.Render("  ✗ "+e.toolErr))
	}
	inner := sb.String()
	return toolCardBorderStyle.Width(m.width - 4).Render(inner)
}

// overlayApproval renders the approval dialog over the base view.
func (m *Model) overlayApproval(base string) string {
	yes := approvalYesStyle.Render("  Yes  ")
	no := approvalNoStyle.Render("  No  ")
	if m.approvalSel == 1 {
		yes = approvalNoStyle.Render("  Yes  ")
		no = approvalYesStyle.Render("  No  ")
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Top, yes, "  ", no)

	content := lipgloss.JoinVertical(lipgloss.Left,
		approvalTitleStyle.Render("Approval Required"),
		"",
		"Command:",
		approvalCmdStyle.Render("  "+m.approvalCmd+"  "),
		"",
		buttons,
		"",
		lipgloss.NewStyle().Foreground(colorMuted).Render("← → to select · Enter to confirm · Esc to deny"),
	)

	box := approvalBoxStyle.Render(content)
	return placeOverlay(m.width, m.height, base, box)
}

// overlaySessionList renders the session picker overlay.
func (m *Model) overlaySessionList(base string) string {
	var items []string
	for i, s := range m.sessions {
		prefix := "  "
		style := sessionItemStyle
		if i == m.sessionCursor {
			prefix = "▶ "
			style = sessionItemActiveStyle
		}
		running := ""
		if s.Running {
			running = sessionRunningStyle.Render(" ●")
		}
		label := prefix + s.Name + " [" + s.Model + "]" + running
		if s.ID == m.activeSession {
			label += " ★"
		}
		items = append(items, style.Render(label))
	}
	items = append(items, lipgloss.NewStyle().Foreground(colorMuted).Render("  [n] new session"))

	content := lipgloss.JoinVertical(lipgloss.Left,
		sessionItemActiveStyle.Render("Sessions (Ctrl+P / Esc to close)"),
		"",
	)
	content += strings.Join(items, "\n")

	box := sessionListBorderStyle.Render(content)
	return placeOverlay(m.width, m.height, base, box)
}

// overlayModelList renders the model picker overlay.
func (m *Model) overlayModelList(base string) string {
	var items []string
	for i, model := range m.modelList {
		prefix := "  "
		style := modelItemStyle
		if i == m.modelCursor {
			prefix = "▶ "
			style = modelItemActiveStyle
		}
		items = append(items, style.Render(prefix+model))
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		modelItemActiveStyle.Render("Select Model (Ctrl+M / Esc to close)"),
		"",
	)
	content += strings.Join(items, "\n")

	box := modelListBorderStyle.Render(content)
	return placeOverlay(m.width, m.height, base, box)
}

// overlaySpawnDialog renders the sub-agent spawn form over the base view.
func (m *Model) overlaySpawnDialog(base string) string {
	content := m.spawnDialog.View()
	box := spawnDialogBoxStyle.Render(content)
	return placeOverlay(m.width, m.height, base, box)
}

// sendSpawnCmd sends a CmdSpawnSubagent message to the control server.
func (m *Model) sendSpawnCmd(req SubagentSpawnRequest) {
	m.sendCmd(controlserver.ClientMsg{
		Type:          controlserver.CmdSpawnSubagent,
		SessionID:     m.activeSession,
		Task:          req.Task,
		Model:         req.Model,
		Thinking:      req.ThinkingLevel,
		ToolAllowlist: req.AllowedTools,
		WorkspaceRoot: req.WorkspaceRoot,
		DeliverBack:   true,
	})
}

// --- Helpers ---

// listenCmd returns a command that reads from the WebSocket and sends messages into Update.
func (m *Model) listenCmd() tea.Cmd {
	return func() tea.Msg {
		msg, err := m.conn.readMsg()
		if err != nil {
			return serverError{err: err}
		}
		return serverMsgReceived{msg: msg}
	}
}

// sendCmd sends a ClientMsg over WebSocket (fire and forget).
func (m *Model) sendCmd(msg controlserver.ClientMsg) {
	if err := m.conn.send(msg); err != nil {
		m.lastErr = fmt.Sprintf("send error: %v", err)
	}
}

// addEntry appends a chat entry.
func (m *Model) addEntry(e chatEntry) {
	m.entries = append(m.entries, e)
}

// refreshViewport re-renders the chat log into the viewport.
func (m *Model) refreshViewport() {
	log := m.renderChatLog()
	m.viewport.SetContent(log)
	m.viewport.GotoBottom()
}

// resizeComponents updates layout-sensitive components after a window resize.
func (m *Model) resizeComponents() {
	inputHeight := m.inputAreaHeight()
	headerH := 1
	statusH := 1
	chatH := m.height - headerH - statusH - inputHeight
	if chatH < 2 {
		chatH = 2
	}
	m.viewport.Width = m.width
	m.viewport.Height = chatH
	m.input.SetWidth(m.width - 4) // account for border padding
	m.refreshViewport()
}

// inputAreaHeight returns the number of rows the input area occupies.
func (m *Model) inputAreaHeight() int {
	// 1 border top + lines + 1 border bottom + padding
	return m.input.Height() + 2
}

// setStatus sets a temporary status message.
func (m *Model) setStatus(s string) {
	m.statusMsg = s
	m.statusAt = time.Now()
}

// submitApproval sends the approval response to the server.
func (m *Model) submitApproval() {
	approved := m.approvalSel == 0
	m.sendCmd(controlserver.ClientMsg{
		Type:       controlserver.CmdApprove,
		SessionID:  m.activeSession,
		ApprovalID: m.approvalID,
		Approved:   approved,
	})
	m.approvalID = ""
	m.approvalCmd = ""
	m.screen = screenChat
}

// tickEvery returns a command that fires after 100ms.
func tickEvery() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// placeOverlay centres a box string over the base.
func placeOverlay(totalW, totalH int, base, box string) string {
	boxW := lipgloss.Width(box)
	boxH := lipgloss.Height(box)

	x := (totalW - boxW) / 2
	y := (totalH - boxH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	baseLines := strings.Split(base, "\n")
	boxLines := strings.Split(box, "\n")

	for i, bLine := range boxLines {
		row := y + i
		if row >= len(baseLines) {
			break
		}
		baseLine := baseLines[row]
		baseRunes := []rune(baseLine)
		// Pad base line if needed
		for len(baseRunes) < totalW {
			baseRunes = append(baseRunes, ' ')
		}
		// Overwrite the portion with boxLine
		bRunes := []rune(bLine)
		for j, r := range bRunes {
			col := x + j
			if col < len(baseRunes) {
				baseRunes[col] = r
			}
		}
		baseLines[row] = string(baseRunes)
	}
	return strings.Join(baseLines, "\n")
}

// wrapText word-wraps text to the given width.
func wrapText(text string, width int) string {
	if width <= 0 {
		return text
	}
	var sb strings.Builder
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(hardWrap(line, width))
	}
	return sb.String()
}

// hardWrap wraps a single line at width, inserting newlines.
func hardWrap(line string, width int) string {
	if utf8.RuneCountInString(line) <= width {
		return line
	}
	runes := []rune(line)
	var sb strings.Builder
	for i := 0; i < len(runes); i += width {
		if i > 0 {
			sb.WriteByte('\n')
		}
		end := i + width
		if end > len(runes) {
			end = len(runes)
		}
		sb.WriteString(string(runes[i:end]))
	}
	return sb.String()
}

// lastAssistantText returns the content of the most recent assistant entry.
func (m *Model) lastAssistantText() string {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].role == "assistant" {
			return m.entries[i].content
		}
	}
	return ""
}

// copyToClipboard writes text to the macOS clipboard via pbcopy.
func copyToClipboard(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// isBotCommand returns true for slash commands that should be routed
// directly to the bot handler rather than the AI.
func isBotCommand(text string) bool {
	botCmds := []string{"/status", "/usage", "/context", "/whoami", "/commands", "/think", "/verbose", "/compact", "/new", "/abort"}
	lower := strings.ToLower(strings.Fields(text)[0])
	for _, c := range botCmds {
		if lower == c {
			return true
		}
	}
	return false
}

// truncate shortens a string to at most n runes.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
