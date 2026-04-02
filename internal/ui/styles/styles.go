package styles

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

var (
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.BrightWhite)

	SubtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.BrightBlack)

	statusBadgeText = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Black).Inline(true)

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

	// Diff gutter: colored bg on the marker column + line numbers.
	DiffAddGutter = lipgloss.NewStyle().
			Foreground(lipgloss.Black).
			Background(lipgloss.Green)

	DiffDelGutter = lipgloss.NewStyle().
			Foreground(lipgloss.Black).
			Background(lipgloss.Red)

	DiffHunkGutter = lipgloss.NewStyle().
			Foreground(lipgloss.Black).
			Background(lipgloss.Blue)

	DiffAddMarker = lipgloss.NewStyle().
			Foreground(lipgloss.Green).
			Bold(true)

	DiffDelMarker = lipgloss.NewStyle().
			Foreground(lipgloss.Red).
			Bold(true)

	DiffHunk = lipgloss.NewStyle().
			Foreground(lipgloss.BrightWhite).
			Background(lipgloss.Blue).
			Bold(true)

	DiffLineNum = lipgloss.NewStyle().
			Foreground(lipgloss.BrightBlack)

	HeaderBar = lipgloss.NewStyle()

	HeaderRepo = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Cyan)

	HeaderSep = lipgloss.NewStyle().
			Foreground(lipgloss.BrightBlack)

	HeaderSection = lipgloss.NewStyle().
			Foreground(lipgloss.BrightWhite)

	HeaderActive = lipgloss.NewStyle().
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

// PRStatusBadge returns the appropriate styled badge for a PR's state,
// rendered as a single line with nerdfont rounded caps.
func PRStatusBadge(state string, draft, merged bool) string {
	var label string
	var bg color.Color

	switch {
	case merged:
		label = "Merged"
		bg = lipgloss.Magenta
	case state == "closed":
		label = "Closed"
		bg = lipgloss.Red
	case draft:
		label = "Drafted"
		bg = lipgloss.Yellow
	default:
		label = "Opened"
		bg = lipgloss.Green
	}

	left := lipgloss.NewStyle().Foreground(bg).Inline(true).Render("\ue0b6")
	mid := statusBadgeText.Background(bg).Render(label)
	right := lipgloss.NewStyle().Foreground(bg).Inline(true).Render("\ue0b4")
	return left + mid + right
}

