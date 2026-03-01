package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	colorPrimary = lipgloss.Color("63")  // purple-ish
	colorAccent  = lipgloss.Color("86")  // green
	colorWarning = lipgloss.Color("220") // yellow
	colorError   = lipgloss.Color("196") // red
	colorMuted   = lipgloss.Color("240") // dark grey
	colorUser    = lipgloss.Color("75")  // blue
	colorBot     = lipgloss.Color("213") // pink/magenta
	colorTool    = lipgloss.Color("214") // orange
	colorSubtle  = lipgloss.Color("238") // very dark

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
				BorderForeground(colorPrimary).
				Padding(1, 2)

	sessionItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252"))

	sessionItemActiveStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

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
				BorderForeground(colorAccent).
				Padding(1, 2)

	modelItemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	modelItemActiveStyle = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	// Subtle divider
	_ = lipgloss.NewStyle().
		Foreground(colorSubtle)
)
