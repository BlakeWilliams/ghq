package diffviewer

import (
	"fmt"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/blakewilliams/ghq/internal/ui/components"
)

// OverlaySearchMatches highlights the exact matching text spans with a yellow
// background and appends an X/N badge on the current match line.
func (d DiffViewer) OverlaySearchMatches(view string) string {
	if d.SearchPattern == nil || len(d.SearchMatches) == 0 {
		return view
	}
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileDiffOffsets) || idx >= len(d.FileDiffs) {
		return view
	}
	offsets := d.FileDiffOffsets[idx]
	diffs := d.FileDiffs[idx]
	vpTop := d.VP.YOffset()
	lines := strings.Split(view, "\n")
	yellowBg := d.Ctx.DiffColors.SearchMatchBg
	colors := d.Ctx.DiffColors

	// Gutter width: the inner portion starts with gutter (line nums + sign)
	// before the actual code content that Content represents.
	colW := components.GutterColWidth(diffs)
	gutterW := components.TotalGutterWidth(colW)

	matchSet := make(map[int]int) // diffLineIdx -> match index (1-based)
	for i, m := range d.SearchMatches {
		matchSet[m] = i + 1
	}

	for diffIdx, matchNum := range matchSet {
		if diffIdx >= len(offsets) || diffIdx >= len(diffs) {
			continue
		}
		absLine := offsets[diffIdx]
		// Adjust for comment box insertion (not tracked in offsets).
		if d.CommentBoxInsertPos >= 0 && d.CommentBoxLines > 0 && absLine > d.CommentBoxInsertPos {
			absLine += d.CommentBoxLines
		}
		rel := absLine - vpTop
		if rel < 0 || rel >= len(lines) {
			continue
		}

		prefix, inner, suffix := SplitDiffBorders(lines[rel])

		// Determine the line's own bg so we restore it after the yellow match.
		var lineBg string
		switch diffs[diffIdx].Type {
		case components.LineAdd:
			lineBg = colors.AddBg
		case components.LineDel:
			lineBg = colors.DelBg
		default:
			lineBg = "\033[49m" // default bg for context lines
		}

		raw := diffs[diffIdx].Content
		inner = highlightSearchSpans(inner, raw, d.SearchPattern, gutterW, yellowBg, colors.SearchMatchFg, lineBg)

		lines[rel] = prefix + inner + suffix

		// Show X/N badge on the current match.
		if d.SearchMatchIdx >= 0 && matchNum == d.SearchMatchIdx+1 {
			badge := lipgloss.NewStyle().Foreground(lipgloss.Magenta).Bold(true).
				Render(fmt.Sprintf(" %d/%d", d.SearchMatchIdx+1, len(d.SearchMatches)))
			lines[rel] = lines[rel] + badge
		}
	}

	return strings.Join(lines, "\n")
}

// highlightSearchSpans injects bgCode around regex matches in an
// ANSI-styled inner string. raw is the plain-text content that the pattern
// matches against; gutterW is the number of visual columns at the start of
// inner that precede the code (line numbers + sign character).
// restoreBg is the ANSI code to resume after the match (the line's own bg).
func highlightSearchSpans(inner, raw string, pattern *regexp.Regexp, gutterW int, bgCode, fgCode, restoreBg string) string {
	locs := pattern.FindAllStringIndex(raw, -1)
	if len(locs) == 0 {
		return inner
	}

	// Convert byte offsets in raw to visual column offsets after tab expansion.
	type span struct{ start, end int }
	spans := make([]span, len(locs))
	for i, loc := range locs {
		spans[i] = span{
			start: byteToVisual(raw, loc[0]) + gutterW,
			end:   byteToVisual(raw, loc[1]) + gutterW,
		}
	}

	innerW := lipgloss.Width(inner)

	// Build result left-to-right: for each segment between matches,
	// cut it from the original inner, then cut the match and wrap it.
	var b strings.Builder
	cursor := 0
	for _, sp := range spans {
		if sp.start >= innerW || sp.start == sp.end {
			continue
		}
		end := sp.end
		if end > innerW {
			end = innerW
		}

		// Text before this match (from cursor to sp.start).
		if sp.start > cursor {
			b.WriteString(xansi.Cut(inner, cursor, sp.start))
		}

		// The match portion — xansi.Cut preserves ANSI state, so the cut
		// string includes existing bg/fg codes that would override ours.
		// Strip all styling and re-wrap with highlight bg+fg.
		matchPart := xansi.Cut(inner, sp.start, end)
		matchPlain := xansi.Strip(matchPart)
		b.WriteString(bgCode)
		b.WriteString(fgCode)
		b.WriteString(matchPlain)
		// Restore the line's styling after the match.
		b.WriteString("\033[0m")
		b.WriteString(restoreBg)

		cursor = end
	}

	// Remainder after last match.
	if cursor < innerW {
		b.WriteString(xansi.Cut(inner, cursor, innerW))
	}

	return b.String()
}

// byteToVisual converts a byte offset in a string to a visual column offset,
// accounting for tab expansion (tabs become 4 spaces).
func byteToVisual(s string, byteOff int) int {
	vis := 0
	for i, r := range s {
		if i >= byteOff {
			break
		}
		if r == '\t' {
			vis += 4
		} else {
			vis++
		}
	}
	return vis
}

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
	// Find first match strictly after current cursor.
	for i, m := range d.SearchMatches {
		if m > d.DiffCursor {
			d.SearchMatchIdx = i
			d.DiffCursor = m
			d.ScrollToDiffCursor()
			return
		}
	}
	// Wrap to first match.
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
	// Find last match strictly before current cursor.
	for i := len(d.SearchMatches) - 1; i >= 0; i-- {
		if d.SearchMatches[i] < d.DiffCursor {
			d.SearchMatchIdx = i
			d.DiffCursor = d.SearchMatches[i]
			d.ScrollToDiffCursor()
			return
		}
	}
	// Wrap to last match.
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
