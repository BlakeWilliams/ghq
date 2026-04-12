package prdetail

import (
	"fmt"
	"strings"

	"github.com/blakewilliams/ghq/internal/ui/components"
	xansi "github.com/charmbracelet/x/ansi"
	"charm.land/lipgloss/v2"
)

// Ensure imports are used.
var _ = xansi.Truncate
var _ = fmt.Sprintf
func (m Model) View() string {
	if !m.vpReady {
		return ""
	}

	// Right panel content with cursor overlay.
	rightView := m.vp.View()
	if m.currentFileIdx >= 0 {
		rightView = m.overlayDiffCursor(rightView)
	}

	// Compose: tree | divider | right panel.
	view := m.renderLayout(rightView)

	// Modal overlay on top.
	if m.showSidebar {
		view = m.renderModal(view)
	}
	return view
}

// renderLayout renders the tree + divider + right panel.
func (m Model) renderLayout(rightView string) string {
	treeW := m.treeWidth
	innerTreeW := treeW - 2 // inside the │ side borders
	innerTreeH := m.height - 2 // inside top/bottom borders

	bc := m.borderStyle()
	var borderFocused lipgloss.Style
	if m.treeFocused {
		borderFocused = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	} else {
		borderFocused = bc
	}

	// Build tree border frame.
	titleStr := " " + lipgloss.NewStyle().Bold(true).Render("Files") + " "
	titleW := lipgloss.Width(titleStr)
	fillW := treeW - 3 - titleW // ╭─ + title + fill + ╮
	if fillW < 0 {
		fillW = 0
	}
	topBorder := borderFocused.Render("╭─") + titleStr + borderFocused.Render(strings.Repeat("─", fillW)+"╮")
	bw := treeW - 2
	if bw < 0 {
		bw = 0
	}
	bottomBorder := borderFocused.Render("╰" + strings.Repeat("─", bw) + "╯")
	sideBorderL := borderFocused.Render("│")
	sideBorderR := borderFocused.Render("│")

	treeContentLines := components.RenderFileTree(m.treeEntries, m.files, m.treeCursor, m.currentFileIdx, innerTreeW, innerTreeH)
	rightLines := strings.Split(rightView, "\n")

	// Right panel border.
	rightW := m.rightPanelWidth()
	innerRightW := rightW - 2
	var rightBorderStyle lipgloss.Style
	if !m.treeFocused {
		rightBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	} else {
		rightBorderStyle = bc
	}

	// Right panel title.
	var rightTitle string
	if m.currentFileIdx >= 0 && m.currentFileIdx < len(m.files) {
		rightTitle = " " + lipgloss.NewStyle().Bold(true).Render(m.files[m.currentFileIdx].Filename) + " "
	} else {
		rightTitle = " " + lipgloss.NewStyle().Bold(true).Render("Description") + " "
	}
	rtW := lipgloss.Width(rightTitle)
	rtFill := rightW - 3 - rtW
	if rtFill < 0 {
		rtFill = 0
	}
	rightTop := rightBorderStyle.Render("╭─") + rightTitle + rightBorderStyle.Render(strings.Repeat("─", rtFill)+"╮")
	rbw := rightW - 2
	if rbw < 0 {
		rbw = 0
	}
	rightBottom := rightBorderStyle.Render("╰" + strings.Repeat("─", rbw) + "╯")
	rightSideL := rightBorderStyle.Render("│")
	rightSideR := rightBorderStyle.Render("│")

	var b strings.Builder
	for i := 0; i < m.height; i++ {
		// Tree column.
		var treeLine string
		if i == 0 {
			treeLine = topBorder
		} else if i == m.height-1 {
			treeLine = bottomBorder
		} else {
			tIdx := i - 1
			cl := ""
			if tIdx < len(treeContentLines) {
				cl = treeContentLines[tIdx]
			}
			treeLine = sideBorderL + cl + sideBorderR
		}

		// Right column.
		var rightLine string
		if i == 0 {
			rightLine = rightTop
		} else if i == m.height-1 {
			rightLine = rightBottom
		} else {
			rIdx := i - 1
			rl := ""
			if rIdx < len(rightLines) {
				rl = rightLines[rIdx]
			}
			// Pad to inner width.
			rlW := lipgloss.Width(rl)
			if rlW < innerRightW {
				rl += strings.Repeat(" ", innerRightW-rlW)
			}
			rightLine = rightSideL + rl + rightSideR
		}

		b.WriteString(treeLine + rightLine)
		if i < m.height-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// cursorViewportLine returns the line index within the visible viewport
// that corresponds to the current diff cursor, or -1 if not visible.
func (m Model) cursorViewportLine() int {
	fileIdx := m.currentFileIdx
	if fileIdx < 0 || fileIdx >= len(m.fileDiffOffsets) {
		return -1
	}
	if m.diffCursor >= len(m.fileDiffOffsets[fileIdx]) {
		return -1
	}
	absLine := m.fileDiffOffsets[fileIdx][m.diffCursor]
	rel := absLine - m.vp.YOffset()
	if rel < 0 || rel >= m.height {
		return -1
	}
	return rel
}

// overlayDiffCursor applies the cursor highlight to the one visible line,
// or highlights all lines in the selection range when shift-selecting.
func (m Model) overlayDiffCursor(view string) string {
	if !m.filesListLoaded || !m.hasDiffLines() {
		return view
	}

	// When there's a multi-line selection, highlight all lines in the range.
	if m.selectionAnchor >= 0 && m.selectionAnchor != m.diffCursor {
		return m.overlaySelectionRange(view)
	}

	vLine := m.cursorViewportLine()
	if vLine < 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	if vLine < len(lines) {
		lines[vLine] = m.applyCursorHighlight(lines[vLine])
	}
	return strings.Join(lines, "\n")
}

// overlaySelectionRange highlights all diff lines in the selection range
// that are visible in the viewport.
func (m Model) overlaySelectionRange(view string) string {
	fileIdx := m.currentFileIdx
	if fileIdx < 0 || fileIdx >= len(m.fileDiffOffsets) {
		return view
	}

	selStart, selEnd := m.selectionAnchor, m.diffCursor
	if selStart > selEnd {
		selStart, selEnd = selEnd, selStart
	}

	offsets := m.fileDiffOffsets[fileIdx]
	diffs := m.fileDiffs[fileIdx]
	vpTop := m.vp.YOffset()

	lines := strings.Split(view, "\n")

	for i := selStart; i <= selEnd; i++ {
		if i >= len(offsets) || i >= len(diffs) {
			continue
		}
		if diffs[i].Type == components.LineHunk {
			continue
		}
		absLine := offsets[i]
		rel := absLine - vpTop
		if rel < 0 || rel >= len(lines) {
			continue
		}
		lines[rel] = m.applySelectionHighlight(lines[rel], diffs[i])
	}

	return strings.Join(lines, "\n")
}

// applySelectionHighlight applies the selected background to a line in the
// selection range. Similar to applyCursorHighlight but takes an explicit DiffLine.
func (m Model) applySelectionHighlight(line string, dl components.DiffLine) string {
	if dl.Type == components.LineHunk {
		return line
	}

	prefix, inner, suffix := splitDiffBorders(line)

	// Replace the bold +/- gutter marker with > to indicate selection.
	inner = strings.Replace(inner, "\033[1m+\033[0m", "\033[1m>\033[0m", 1)
	inner = strings.Replace(inner, "\033[1m-\033[0m", "\033[1m>\033[0m", 1)

	colors := m.ctx.DiffColors
	var selBg string
	switch dl.Type {
	case components.LineAdd:
		selBg = colors.SelectedAddBg
	case components.LineDel:
		selBg = colors.SelectedDelBg
	default:
		selBg = colors.SelectedCtxBg
	}

	if selBg != "" {
		if colors.AddBg != "" {
			inner = strings.ReplaceAll(inner, colors.AddBg, selBg)
		}
		if colors.DelBg != "" {
			inner = strings.ReplaceAll(inner, colors.DelBg, selBg)
		}
		inner = strings.ReplaceAll(inner, "\033[0m", "\033[0m"+selBg)
		inner = strings.ReplaceAll(inner, "\033[m", "\033[m"+selBg)
		inner = selBg + inner + "\033[0m"
	}

	return prefix + inner + suffix
}

// splitDiffBorders splits a rendered diff line of the form
// border + inner + border into its three parts. The border is a styled "│"
// character whose ANSI byte length varies by terminal color profile, so we
// locate the "│" characters instead of assuming a fixed byte offset.
func splitDiffBorders(line string) (prefix, inner, suffix string) {
	const borderChar = "│"

	firstIdx := strings.Index(line, borderChar)
	if firstIdx < 0 {
		return "", line, ""
	}

	lastIdx := strings.LastIndex(line, borderChar)
	if lastIdx == firstIdx {
		return "", line, ""
	}

	// Prefix ends after the first │ and any trailing ANSI reset sequence.
	prefixEnd := firstIdx + len(borderChar)
	if prefixEnd < len(line) && line[prefixEnd] == '\033' {
		if i := strings.IndexByte(line[prefixEnd:], 'm'); i >= 0 {
			prefixEnd += i + 1
		}
	}

	// Suffix starts at the ESC introducing the last │'s foreground sequence.
	suffixStart := lastIdx
	for i := lastIdx - 1; i >= prefixEnd; i-- {
		if line[i] == '\033' {
			suffixStart = i
			break
		}
	}

	return line[:prefixEnd], line[prefixEnd:suffixStart], line[suffixStart:]
}

func (m Model) renderWithLeftSidebarFrom(view string) string {
	treeW := m.treeWidth
	divider := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("│")

	treeLines := components.RenderFileTree(m.treeEntries, m.files, m.treeCursor, m.currentFileIdx, treeW, m.height)
	mainLines := strings.Split(view, "\n")

	var b strings.Builder
	for i := 0; i < m.height; i++ {
		tl := ""
		if i < len(treeLines) {
			tl = treeLines[i]
		}
		ml := ""
		if i < len(mainLines) {
			ml = mainLines[i]
		}
		b.WriteString(tl + divider + ml)
		if i < m.height-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderModal overlays the sidebar content as a centered modal window on top
// of the dimmed background. The modal has a fixed rounded border frame; the
// viewport scrolls inside it.
func (m Model) renderModal(view string) string {
	const pad = 4
	modalW := m.width - pad*2
	modalH := m.height - pad*2
	if modalW < 20 {
		modalW = 20
	}
	if modalH < 5 {
		modalH = 5
	}
	contentPad := 2      // padding inside the │ borders
	innerW := modalW - 2 - contentPad*2 // usable content width

	// Build the modal title for the top border.
	var title string
	switch m.sidebarType {
	case sidebarComments:
		title = iconComments + " Comments"
		if len(m.comments) > 0 {
			title += fmt.Sprintf(" (%d)", len(m.comments))
		}
	case sidebarReviews:
		title = iconReview + " Reviews"
	case sidebarChecks:
		title = iconChecks + " Checks"
		if len(m.checkRuns) > 0 {
			title += fmt.Sprintf(" (%d)", len(m.checkRuns))
		}
	}

	titleStr := " " + lipgloss.NewStyle().Bold(true).Render(title) + " "
	titleW := lipgloss.Width(titleStr)
	fillW := modalW - 3 - titleW // ╭─ + title + fill + ╮
	if fillW < 0 {
		fillW = 0
	}
	bc := m.borderStyle()
	topBorder := bc.Render("╭─") + titleStr + bc.Render(strings.Repeat("─", fillW)+"╮")
	bw := modalW - 2
	if bw < 0 {
		bw = 0
	}
	bottomBorder := bc.Render("╰" + strings.Repeat("─", bw) + "╯")
	sideBorder := bc.Render("│")

	// The viewport content lines (scrolled).
	vpLines := strings.Split(m.sidebarVP.View(), "\n")

	bgLines := strings.Split(view, "\n")

	// 1-cell black bg margin around the modal.
	shadow := "\033[40m" // black bg
	shadowReset := "\033[0m"
	shadowW := modalW + 2 // modal + 1 cell each side
	shadowBlank := shadow + strings.Repeat(" ", shadowW) + shadowReset
	shadowL := shadow + " " + shadowReset
	shadowR := shadow + " " + shadowReset
	spliceOffset := pad - 1 // 1 cell left of modal

	var b strings.Builder
	for i := 0; i < m.height; i++ {
		bg := ""
		if i < len(bgLines) {
			bg = bgLines[i]
		}

		if i == pad-1 || i == pad+modalH {
			// Shadow row above/below the modal.
			b.WriteString(spliceModal(bg, shadowBlank, spliceOffset, shadowW, m.width))
		} else if i == pad {
			b.WriteString(spliceModal(bg, shadowL+topBorder+shadowR, spliceOffset, shadowW, m.width))
		} else if i == pad+modalH-1 {
			b.WriteString(spliceModal(bg, shadowL+bottomBorder+shadowR, spliceOffset, shadowW, m.width))
		} else if i > pad && i < pad+modalH-1 {
			vpIdx := i - pad - 1
			cl := ""
			if vpIdx >= 0 && vpIdx < len(vpLines) {
				cl = vpLines[vpIdx]
			}
			clW := lipgloss.Width(cl)
			if clW < innerW {
				cl += strings.Repeat(" ", innerW-clW)
			}
			iPad := strings.Repeat(" ", contentPad)
			modalLine := shadowL + sideBorder + iPad + cl + iPad + sideBorder + shadowR
			b.WriteString(spliceModal(bg, modalLine, spliceOffset, shadowW, m.width))
		} else {
			b.WriteString(bg)
		}
		if i < m.height-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// spliceModal replaces the middle portion of a background line with modal content,
// preserving the original background on the left and right.
func spliceModal(bg, modal string, leftOffset, modalW, totalW int) string {
	left := xansi.Truncate(bg, leftOffset, "")
	leftW := lipgloss.Width(left)
	if leftW < leftOffset {
		left += strings.Repeat(" ", leftOffset-leftW)
	}

	rightStart := leftOffset + modalW
	bgW := lipgloss.Width(bg)
	right := ""
	if bgW > rightStart {
		right = xansi.Cut(bg, rightStart, bgW)
	}

	return left + "\033[0m" + modal + "\033[0m" + right
}

