package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors — adaptive: dark terminal / light terminal
	colorPrimary = lipgloss.AdaptiveColor{Dark: "39", Light: "24"}   // blue
	colorAccent  = lipgloss.AdaptiveColor{Dark: "41", Light: "28"}   // green
	colorWarning = lipgloss.AdaptiveColor{Dark: "220", Light: "130"} // yellow/brown
	colorError   = lipgloss.AdaptiveColor{Dark: "196", Light: "160"} // red
	colorMuted   = lipgloss.AdaptiveColor{Dark: "240", Light: "245"} // grey
	colorUser    = lipgloss.AdaptiveColor{Dark: "75", Light: "19"}   // blue
	colorBot     = lipgloss.AdaptiveColor{Dark: "87", Light: "22"}   // cyan / dark green
	colorTool    = lipgloss.AdaptiveColor{Dark: "214", Light: "130"} // orange
	colorSubtle  = lipgloss.AdaptiveColor{Dark: "238", Light: "250"} // subtle

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
				Foreground(lipgloss.AdaptiveColor{Dark: "252", Light: "235"})

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
			Foreground(lipgloss.AdaptiveColor{Dark: "252", Light: "235"})

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
