package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors - fixed dark-palette values.
	// Using fixed colors avoids lipgloss/termenv background probing (OSC 11),
	// which can delay startup and leak escape responses into input on some terminals.
	colorPrimary = lipgloss.Color("39")  // blue
	colorAccent  = lipgloss.Color("41")  // green
	colorWarning = lipgloss.Color("220") // yellow
	colorError   = lipgloss.Color("196") // red
	colorMuted   = lipgloss.Color("240") // grey
	colorUser    = lipgloss.Color("75")  // blue
	colorBot     = lipgloss.Color("87")  // cyan
	colorTool    = lipgloss.Color("214") // orange
	colorSubtle  = lipgloss.Color("238") // subtle

	// Status bar at the bottom
	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(lipgloss.Color("250")).
			Padding(0, 1)

	statusKeyStyle = lipgloss.NewStyle().
			Background(colorPrimary).
			Foreground(lipgloss.Color("230")).
			Padding(0, 1).
			Bold(true)

	statusValueStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("252")).
				Padding(0, 1)

	statusRunIdleStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("252")).
				Padding(0, 1)

	statusRunBusyStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("52")).
				Foreground(lipgloss.Color("230")).
				Padding(0, 1).
				Bold(true)

	// Header bar
	headerStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("17")).
			Foreground(lipgloss.Color("255")).
			Padding(0, 1).
			Bold(true)

	headerDimStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("17")).
			Foreground(lipgloss.Color("240")).
			Padding(0, 1)

	// Chat messages
	userMsgStyle = lipgloss.NewStyle().
			Foreground(colorUser).
			Bold(true)

	botMsgStyle = lipgloss.NewStyle().
			Foreground(colorBot)

	// Message prefix labels
	userLabelStyle = lipgloss.NewStyle().
			Foreground(colorUser).
			Bold(true)

	botLabelStyle = lipgloss.NewStyle().
			Foreground(colorBot).
			Bold(true)

	messageTimeStyle = lipgloss.NewStyle().
				Foreground(colorMuted)

	messageMetaStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Italic(true)

	// Tool event card
	toolCardBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorTool).
				Padding(0, 1)

	toolNameStyle = lipgloss.NewStyle().
			Foreground(colorTool).
			Bold(true)

	toolArgStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	toolResultStyle = lipgloss.NewStyle().
			Foreground(colorAccent)

	toolErrorStyle = lipgloss.NewStyle().
			Foreground(colorError)

	// Collapsed tool card (single-line, no border)
	toolCardCollapsedStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Padding(0, 1)

	// Focused tool card in navigation mode (collapsed)
	toolCardFocusedStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent).
				Foreground(colorTool).
				Padding(0, 1)

	// Focused tool card in navigation mode (expanded)
	toolCardExpandedFocusedStyle = lipgloss.NewStyle().
					Border(lipgloss.RoundedBorder()).
					BorderForeground(colorAccent).
					Padding(0, 1)

	// Approval dialog
	approvalBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.DoubleBorder()).
				BorderForeground(colorWarning).
				Padding(1, 2)

	approvalTitleStyle = lipgloss.NewStyle().
				Foreground(colorWarning).
				Bold(true)

	approvalCmdStyle = lipgloss.NewStyle().
				Foreground(colorError).
				Background(lipgloss.Color("52")).
				Padding(0, 1)

	approvalYesStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("0")).
				Background(colorAccent).
				Padding(0, 2).
				Bold(true)

	approvalNoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(colorError).
			Padding(0, 2).
			Bold(true)

	// Session list overlay
	sessionListBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorSubtle).
				Padding(1, 2)

	sessionItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252"))

	sessionItemActiveStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true).
				Padding(0, 1)

	// Sessions sidebar
	sidebarTitleStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Bold(true).
				Padding(0, 1)

	sidebarTitleFocusStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true).
				Padding(0, 1)

	sidebarItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).
				Padding(0, 1)

	sidebarItemActiveStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true).
				Padding(0, 1)

	sidebarHintStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Padding(0, 1)

	sidebarSepStyle = lipgloss.NewStyle().
			Foreground(colorSubtle)

	sessionRunningStyle = lipgloss.NewStyle().
				Foreground(colorWarning)

	// Input area
	inputBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(colorPrimary).
				Padding(0, 1)

	inputBorderFocusStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(colorAccent).
				Padding(0, 1)

	// Slash command completion popup
	completionPopupStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("243")).
				Background(lipgloss.Color("235")).
				Padding(0, 1)

	completionTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("250")).
				Background(lipgloss.Color("236")).
				Padding(0, 1).
				Bold(true)

	completionItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).
				Padding(0, 1)

	completionItemSelectedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("255")).
					Background(lipgloss.Color("24")).
					Padding(0, 1)

	completionCommandStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("87")).
				Bold(true)

	completionCommandSelectedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("230")).
					Bold(true)

	completionDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))

	completionDescSelectedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("252"))

	completionEmptyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245")).
				Italic(true)

	// Streaming cursor
	streamingCursorStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	// Error message inline
	inlineErrorStyle = lipgloss.NewStyle().
				Foreground(colorError).
				Italic(true)

	// Model picker
	modelListBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorSubtle).
				Padding(1, 2)

	modelItemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	modelItemActiveStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	// Dialog shared styles (reusable across overlay dialogs)
	dialogTitleStyle = lipgloss.NewStyle().
				Foreground(colorPrimary).
				Bold(true)

	dialogHelpStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	dialogFilterPromptStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	dialogFilterPlaceholderStyle = lipgloss.NewStyle().
					Foreground(colorMuted).
					Italic(true)

	// Spawn sub-agent dialog
	spawnDialogBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("212")).
				Padding(1, 2)

	// System / info message (e.g. sub-agent completion notifications)
	systemMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Italic(true)

	// Subtle divider
	_ = lipgloss.NewStyle().
		Foreground(colorSubtle)

	// Sidebar
	sidebarStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, true, false, false). // right border only
			BorderForeground(colorSubtle).
			Padding(0, 1)

	sidebarFocusStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, true, false, false).
				BorderForeground(colorAccent).
				Padding(0, 1)

	sidebarItemSelectedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("255")).
					Background(lipgloss.Color("238"))

	sidebarNewSessionStyle = lipgloss.NewStyle().
				Foreground(colorMuted)
)
