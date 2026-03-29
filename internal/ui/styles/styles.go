package styles

import "charm.land/lipgloss/v2"

var (
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.BrightWhite)

	SubtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.BrightBlack)

	StatusOpen = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Black).
			Background(lipgloss.Green).
			Padding(0, 1)

	StatusDraft = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Black).
			Background(lipgloss.Yellow).
			Padding(0, 1)

	TabActive = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.BrightWhite).
			Padding(0, 1).
			BorderBottom(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Magenta)

	TabInactive = lipgloss.NewStyle().
			Foreground(lipgloss.BrightBlack).
			Padding(0, 1).
			BorderBottom(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.BrightBlack)

	DiffAdd = lipgloss.NewStyle().
		Foreground(lipgloss.Green)

	DiffDel = lipgloss.NewStyle().
		Foreground(lipgloss.Red)

	DiffHunk = lipgloss.NewStyle().
			Foreground(lipgloss.Cyan).
			Bold(true)

	DiffFileHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Magenta).
			Padding(1, 0, 0, 0)

	HeaderRepo = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Magenta)

	HeaderSep = lipgloss.NewStyle().
			Foreground(lipgloss.BrightBlack)

	HeaderSection = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.BrightWhite)

	StatusBar = lipgloss.NewStyle().
			Foreground(lipgloss.BrightBlack)

	StatusBarMode = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Black).
			Background(lipgloss.Magenta).
			Inline(true)

	StatusBarKey = lipgloss.NewStyle().
			Foreground(lipgloss.Magenta)

	StatusBarHint = lipgloss.NewStyle().
			Foreground(lipgloss.BrightBlack)
)
