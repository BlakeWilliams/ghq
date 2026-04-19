package diffviewer

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/blakewilliams/gg/internal/ui/components"
)

// HandleSearchKey processes key events while the search popup is open.
// Returns KeyHandled if the key was consumed.
func (d *DiffViewer) HandleSearchKey(key string, text string) KeyResult {
	switch key {
	case "enter":
		d.Searching = false
		if d.SearchQuery == "" {
			d.ClearSearch()
		}
		return KeyHandled
	case "esc":
		d.CancelSearch()
		return KeyHandled
	case "backspace":
		if len(d.SearchQuery) > 0 {
			d.SearchQuery = d.SearchQuery[:len(d.SearchQuery)-1]
		}
		d.updateIncrementalSearch()
		return KeyHandled
	default:
		// Append typed text.
		if text != "" {
			d.SearchQuery += text
		}
		d.updateIncrementalSearch()
		return KeyHandled
	}
}

// updateIncrementalSearch recompiles the pattern from SearchQuery and
// re-runs the search so highlights update live as the user types.
func (d *DiffViewer) updateIncrementalSearch() {
	if d.SearchQuery == "" {
		d.SearchPattern = nil
		d.SearchMatches = nil
		d.SearchMatchIdx = -1
		return
	}
	// Smart case: case-insensitive unless query contains uppercase.
	prefix := "(?i)"
	for _, r := range d.SearchQuery {
		if r >= 'A' && r <= 'Z' {
			prefix = ""
			break
		}
	}
	re, err := regexp.Compile(prefix + d.SearchQuery)
	if err != nil {
		// Invalid regex mid-typing — clear matches but keep query.
		d.SearchPattern = nil
		d.SearchMatches = nil
		d.SearchMatchIdx = -1
		return
	}
	d.SearchPattern = re
	d.RunSearch()
}

// StartSearch opens the search popup.
func (d *DiffViewer) StartSearch() {
	d.Searching = true
	d.SearchQuery = ""
}

// CancelSearch closes the search popup without changing matches.
func (d *DiffViewer) CancelSearch() {
	d.Searching = false
	d.SearchQuery = ""
}

// ClearSearch removes all search state (used on file change).
func (d *DiffViewer) ClearSearch() {
	d.Searching = false
	d.SearchQuery = ""
	d.SearchPattern = nil
	d.SearchMatches = nil
	d.SearchMatchIdx = -1
}

// RunSearch scans the current file's diff lines for the compiled pattern.
func (d *DiffViewer) RunSearch() {
	d.SearchMatches = nil
	d.SearchMatchIdx = -1
	if d.SearchPattern == nil {
		return
	}
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileDiffs) {
		return
	}
	for i, dl := range d.FileDiffs[idx] {
		if dl.Type == components.LineHunk {
			continue
		}
		if d.SearchPattern.MatchString(dl.Content) {
			d.SearchMatches = append(d.SearchMatches, i)
		}
	}
}

// SearchNext jumps the diff cursor to the next match strictly after the
// current cursor line, wrapping to the first match if needed.
func (d *DiffViewer) SearchNext() {
	if len(d.SearchMatches) == 0 {
		return
	}
	for i, m := range d.SearchMatches {
		if m > d.DiffCursor {
			d.SearchMatchIdx = i
			d.DiffCursor = m
			d.ScrollToDiffCursor()
			return
		}
	}
	d.SearchMatchIdx = 0
	d.DiffCursor = d.SearchMatches[0]
	d.ScrollToDiffCursor()
}

// SearchPrev jumps the diff cursor to the previous match strictly before the
// current cursor line, wrapping to the last match if needed.
func (d *DiffViewer) SearchPrev() {
	if len(d.SearchMatches) == 0 {
		return
	}
	for i := len(d.SearchMatches) - 1; i >= 0; i-- {
		if d.SearchMatches[i] < d.DiffCursor {
			d.SearchMatchIdx = i
			d.DiffCursor = d.SearchMatches[i]
			d.ScrollToDiffCursor()
			return
		}
	}
	d.SearchMatchIdx = len(d.SearchMatches) - 1
	d.DiffCursor = d.SearchMatches[d.SearchMatchIdx]
	d.ScrollToDiffCursor()
}

// RenderSearchPopup composites a centered search modal over the given background.
func (d DiffViewer) RenderSearchPopup(bg string, bgHeight int) string {
	if !d.Searching {
		return bg
	}

	bc := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	promptStyle := lipgloss.NewStyle().Foreground(lipgloss.Magenta).Bold(true)

	// Build content: / query /
	content := promptStyle.Render("/") + " " + d.SearchQuery + " " + promptStyle.Render("/")
	contentW := lipgloss.Width(content)

	// Use same width formula as the ctrl+p picker modal.
	modalW := d.Width / 2
	if modalW < 40 {
		modalW = 40
	}
	if modalW > d.Width-4 {
		modalW = d.Width - 4
	}
	innerW := modalW - 4

	// Center vertically — bias toward top third (like picker).
	modalH := 3 // top border + content + bottom border
	padY := (bgHeight - modalH) / 3
	if padY < 1 {
		padY = 1
	}

	// Borders.
	titleStr := " " + lipgloss.NewStyle().Bold(true).Render("Search") + " "
	titleW := lipgloss.Width(titleStr)
	fillW := modalW - 3 - titleW
	if fillW < 0 {
		fillW = 0
	}
	topBorder := bc.Render("╭─") + titleStr + bc.Render(strings.Repeat("─", fillW)+"╮")
	bw := modalW - 2
	if bw < 0 {
		bw = 0
	}
	bottomBorder := bc.Render("╰" + strings.Repeat("─", bw) + "╯")
	side := bc.Render("│")

	// Pad content to inner width.
	pad := innerW - contentW
	if pad < 0 {
		pad = 0
	}
	contentLine := side + " " + content + strings.Repeat(" ", pad) + " " + side

	modalLines := []string{topBorder, contentLine, bottomBorder}

	// Splice modal onto background (same pattern as picker).
	bgLines := strings.Split(bg, "\n")
	padX := (d.Width - modalW) / 2

	for i, ml := range modalLines {
		row := padY + i
		if row < 0 || row >= len(bgLines) {
			continue
		}
		bgLine := bgLines[row]
		bgW := lipgloss.Width(bgLine)
		rightStart := padX + modalW

		left := ""
		if padX > 0 {
			left = xansi.Truncate(bgLine, padX, "")
			leftW := lipgloss.Width(left)
			if leftW < padX {
				left += strings.Repeat(" ", padX-leftW)
			}
		}

		right := ""
		if bgW > rightStart {
			right = xansi.Cut(bgLine, rightStart, bgW)
		}

		bgLines[row] = left + "\033[0m" + ml + "\033[0m" + right
	}

	return strings.Join(bgLines, "\n")
}
