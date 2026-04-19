package diffviewer

import (
	"fmt"
	"image/color"
	"path"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/review/agents"
	"github.com/blakewilliams/ghq/internal/review/comments"
	"github.com/blakewilliams/ghq/internal/ui/components"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	"github.com/blakewilliams/ghq/internal/ui/uictx"
)

const scrollMargin = 5

// LayoutInfo carries optional metadata for the header/footer chrome.
// Callers that don't set fields get sensible defaults (no mode, no branch, etc).
type LayoutInfo struct {
	ModeName    string      // e.g. "Unstaged", "Staged", "Branch"
	ModeColor   color.Color // derived from mode; nil = no mode shown
	BranchName  string      // shown in footer under file tree
	PR          *github.PullRequest
}

// CopilotPendingInfo tracks a single pending Copilot reply.
type CopilotPendingInfo struct {
	Path string
	Line int
	Side string
}

// DiffViewer holds the shared state for a file-tree + diff-viewport layout.
// Both localdiff and prdetail embed this struct.
type DiffViewer struct {
	Ctx    *uictx.Context
	Width  int
	Height int

	// Viewport
	VP      viewport.Model
	VPReady bool

	// Files
	Files                []github.PullRequestFile
	HighlightedFiles     []components.HighlightedDiff
	RenderedFiles        []string
	FileDiffs            [][]components.DiffLine
	FileDiffOffsets      [][]int
	FilesHighlighted     int
	FilesLoading         bool
	FilesListLoaded      bool
	CurrentFileIdx       int // -1 = overview

	// File tree
	Tree components.FileTree

	// Diff cursor
	DiffCursor      int
	SelectionAnchor int
	ThreadCursor    int

	// Comment composing
	Composing        bool
	CommentInput     textarea.Model
	CommentFile      string
	CommentLine      int
	CommentSide      string
	CommentStartLine int
	CommentStartSide string

	// Copilot state
	Agent        *agents.Client
	CopilotState CopilotState

	// Comment source — set by outer model. Returns base comments for a file
	// (without copilot pending, which DiffViewer appends itself).
	Comments CommentSource

	// Render body callback for markdown in comment threads.
	RenderBody func(body string, width int, bg string) string

	// Render lists per file (structural render items).
	FileRenderLists []*components.FileRenderList

	// Loading spinner (shown while highlighting is in progress for current file).
	Spinner       spinner.Model
	SpinnerActive bool

	// Internal
	WaitingG    bool // true after first "g" keypress, waiting for second "g" to trigger gg (go to top)
	LastContent string
}

// CommentSource provides comments for a file. Each view implements this
// to return comments from its backing store (local CommentStore, GitHub API, etc).
type CommentSource interface {
	CommentsForFile(filename string) []github.ReviewComment
}

// BlockSource is an optional extension of CommentSource that provides
// content blocks for comments that have them (e.g. copilot replies with
// tool calls). The returned map is keyed by ReviewComment.ID.
// If a CommentSource also implements BlockSource, blocks are preserved
// during rendering instead of being lost in the ReviewComment conversion.
type BlockSource interface {
	BlocksForFile(filename string) map[int][]comments.ContentBlock
}

// --- Layout helpers ---

func (d DiffViewer) RightPanelWidth() int {
	return d.Width - d.Tree.Width
}

func (d DiffViewer) RightPanelInnerWidth() int {
	return d.RightPanelWidth() - 1 // separator column
}

func (d DiffViewer) ContentWidth() int {
	return d.RightPanelInnerWidth() - 1 // -1 for scrollbar column
}

func (d DiffViewer) ViewportHeight() int {
	return d.Height - 2 // header + separator (footer only affects file tree side)
}

func (d DiffViewer) BorderStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(d.Ctx.DiffColors.BorderColor)
}

func (d DiffViewer) AuthorName() string {
	if d.Ctx.Username != "" {
		return d.Ctx.Username
	}
	return "you"
}

// --- Diff cursor ---

func (d DiffViewer) HasDiffLines() bool {
	idx := d.CurrentFileIdx
	return idx >= 0 && idx < len(d.FileDiffs) && len(d.FileDiffs[idx]) > 0
}

// MoveDiffCursor moves the cursor by delta, skipping hunk lines.
func (d *DiffViewer) MoveDiffCursor(delta int) {
	if d.CurrentFileIdx < 0 || d.CurrentFileIdx >= len(d.FileDiffs) {
		return
	}
	d.ThreadCursor = 0
	lines := d.FileDiffs[d.CurrentFileIdx]
	newPos := d.DiffCursor + delta

	for newPos >= 0 && newPos < len(lines) && lines[newPos].Type == components.LineHunk {
		newPos += delta
	}

	if newPos < 0 || newPos >= len(lines) {
		return
	}
	d.DiffCursor = newPos
	d.ScrollToDiffCursor()
}

// MoveDiffCursorBy jumps the cursor by delta lines, clamped, skipping hunks.
func (d *DiffViewer) MoveDiffCursorBy(delta int) {
	if d.CurrentFileIdx < 0 || d.CurrentFileIdx >= len(d.FileDiffs) {
		return
	}
	d.ThreadCursor = 0
	lines := d.FileDiffs[d.CurrentFileIdx]
	newPos := d.DiffCursor + delta

	if newPos < 0 {
		newPos = 0
	}
	if newPos >= len(lines) {
		newPos = len(lines) - 1
	}

	if newPos >= 0 && newPos < len(lines) && lines[newPos].Type == components.LineHunk {
		dir := 1
		if delta < 0 {
			dir = -1
		}
		found := false
		for p := newPos + dir; p >= 0 && p < len(lines); p += dir {
			if lines[p].Type != components.LineHunk {
				newPos = p
				found = true
				break
			}
		}
		if !found {
			for p := newPos - dir; p >= 0 && p < len(lines); p -= dir {
				if lines[p].Type != components.LineHunk {
					newPos = p
					found = true
					break
				}
			}
		}
		if !found {
			return
		}
	}

	d.DiffCursor = newPos
	d.ScrollToDiffCursor()
}

func (d DiffViewer) FirstNonHunkLine(fileIdx int) int {
	if fileIdx < 0 || fileIdx >= len(d.FileDiffs) {
		return 0
	}
	for i, dl := range d.FileDiffs[fileIdx] {
		if dl.Type != components.LineHunk {
			return i
		}
	}
	return 0
}

func (d DiffViewer) LastNonHunkLine(fileIdx int) int {
	if fileIdx < 0 || fileIdx >= len(d.FileDiffs) {
		return 0
	}
	lines := d.FileDiffs[fileIdx]
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].Type != components.LineHunk {
			return i
		}
	}
	return len(lines) - 1
}

// DiffLineIdxForComment finds the diff line index for a given side/line.
// Returns -1 if not found.
func (d DiffViewer) DiffLineIdxForComment(fileIdx int, side string, line int) int {
	if fileIdx < 0 || fileIdx >= len(d.FileDiffs) {
		return -1
	}
	for i, dl := range d.FileDiffs[fileIdx] {
		if side == "LEFT" && dl.Type == components.LineDel && dl.OldLineNo == line {
			return i
		}
		if side == "RIGHT" && dl.Type != components.LineDel && dl.NewLineNo == line {
			return i
		}
	}
	return -1
}

// ScrollToDiffCursor adjusts the viewport so the cursor line is visible.
func (d *DiffViewer) ScrollToDiffCursor() {
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileDiffOffsets) {
		return
	}
	if d.DiffCursor >= len(d.FileDiffOffsets[idx]) {
		return
	}
	vpH := d.ViewportHeight()
	absLine := d.FileDiffOffsets[idx][d.DiffCursor]
	top := d.VP.YOffset()
	bottom := top + vpH - 1

	if absLine < top+scrollMargin {
		target := absLine - scrollMargin
		if target < 0 {
			target = 0
		}
		d.VP.SetYOffset(target)
	} else if absLine > bottom-scrollMargin {
		d.VP.SetYOffset(absLine - vpH + scrollMargin + 1)
	}
}

// ScrollAndSyncCursor scrolls the viewport by delta, keeping the cursor
// at the same screen-relative position (vim ctrl+d/u behavior).
func (d *DiffViewer) ScrollAndSyncCursor(delta int) {
	d.ThreadCursor = 0
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileDiffOffsets) {
		return
	}

	cursorAbs := 0
	if d.DiffCursor < len(d.FileDiffOffsets[idx]) {
		cursorAbs = d.FileDiffOffsets[idx][d.DiffCursor]
	}
	relPos := cursorAbs - d.VP.YOffset()

	d.VP.SetYOffset(d.VP.YOffset() + delta)

	targetAbs := d.VP.YOffset() + relPos
	offsets := d.FileDiffOffsets[idx]
	diffs := d.FileDiffs[idx]
	best := -1
	bestDist := 0
	for i := 0; i < len(offsets); i++ {
		if i < len(diffs) && diffs[i].Type == components.LineHunk {
			continue
		}
		dist := offsets[i] - targetAbs
		if dist < 0 {
			dist = -dist
		}
		if best == -1 || dist < bestDist {
			best = i
			bestDist = dist
		}
	}
	if best >= 0 {
		d.DiffCursor = best
	}
}

// SyncDiffCursorToViewport moves the diff cursor to the line closest to
// the center of the viewport. Used after viewport-only scrolling.
func (d *DiffViewer) SyncDiffCursorToViewport() {
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileDiffOffsets) || len(d.FileDiffOffsets[idx]) == 0 {
		return
	}
	center := d.VP.YOffset() + d.ViewportHeight()/2
	offsets := d.FileDiffOffsets[idx]
	diffs := d.FileDiffs[idx]
	best := -1
	bestDist := 0
	for i := 0; i < len(offsets); i++ {
		if i < len(diffs) && diffs[i].Type == components.LineHunk {
			continue
		}
		dist := offsets[i] - center
		if dist < 0 {
			dist = -dist
		}
		if best == -1 || dist < bestDist {
			best = i
			bestDist = dist
		}
	}
	if best >= 0 {
		d.DiffCursor = best
	}
}

// CursorViewportLine returns the cursor's Y position in the viewport, or -1 if not visible.
func (d DiffViewer) CursorViewportLine() int {
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileDiffOffsets) {
		return -1
	}
	if d.DiffCursor >= len(d.FileDiffOffsets[idx]) {
		return -1
	}
	absLine := d.FileDiffOffsets[idx][d.DiffCursor]
	rel := absLine - d.VP.YOffset()
	if rel < 0 || rel >= d.ViewportHeight() {
		return -1
	}
	return rel
}

// DiffCursorFromScreenY maps a screen Y coordinate to the nearest diff line
// index, accounting for chrome rows and viewport scroll. Returns -1 if no
// valid diff line is found.
func (d *DiffViewer) DiffCursorFromScreenY(y int) int {
	chromeRows := 2
	row := y - chromeRows
	if row < 0 || row >= d.ViewportHeight() {
		return -1
	}
	absLine := d.VP.YOffset() + row

	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileDiffOffsets) {
		return -1
	}
	offsets := d.FileDiffOffsets[idx]
	diffs := d.FileDiffs[idx]

	best := -1
	bestDist := 0
	for i, off := range offsets {
		if i < len(diffs) && diffs[i].Type == components.LineHunk {
			continue
		}
		dist := off - absLine
		if dist < 0 {
			dist = -dist
		}
		if best == -1 || dist < bestDist {
			best = i
			bestDist = dist
		}
	}
	return best
}

// --- Cursor overlay rendering ---

// OverlayDiffCursor applies cursor or selection highlighting to the viewport content.
func (d DiffViewer) OverlayDiffCursor(view string) string {
	if !d.FilesListLoaded || !d.HasDiffLines() {
		return view
	}

	if d.SelectionAnchor >= 0 && d.SelectionAnchor != d.DiffCursor {
		return d.overlaySelectionRange(view)
	}

	vLine := d.CursorViewportLine()
	if vLine < 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	if vLine < len(lines) {
		lines[vLine] = d.ApplyCursorHighlight(lines[vLine])
	}
	return strings.Join(lines, "\n")
}

// ApplyCursorHighlight applies the cursor background to a single rendered line.
func (d DiffViewer) ApplyCursorHighlight(line string) string {
	idx := d.CurrentFileIdx
	if idx >= len(d.FileDiffs) || d.DiffCursor >= len(d.FileDiffs[idx]) {
		return line
	}
	dl := d.FileDiffs[idx][d.DiffCursor]
	if dl.Type == components.LineHunk {
		return line
	}

	prefix, inner, suffix := SplitDiffBorders(line)

	inner = strings.Replace(inner, "\033[1m+\033[0m", "\033[1m>\033[0m", 1)
	inner = strings.Replace(inner, "\033[1m-\033[0m", "\033[1m>\033[0m", 1)

	colors := d.Ctx.DiffColors
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
		inner = replaceBackground(inner, colors, selBg)
	}

	return prefix + inner + suffix
}

func (d DiffViewer) overlaySelectionRange(view string) string {
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileDiffOffsets) {
		return view
	}

	selStart, selEnd := d.SelectionAnchor, d.DiffCursor
	if selStart > selEnd {
		selStart, selEnd = selEnd, selStart
	}

	offsets := d.FileDiffOffsets[idx]
	diffs := d.FileDiffs[idx]
	vpTop := d.VP.YOffset()

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
		lines[rel] = d.ApplySelectionHighlight(lines[rel], diffs[i])
	}

	return strings.Join(lines, "\n")
}

// ApplySelectionHighlight applies selection background to a diff line.
func (d DiffViewer) ApplySelectionHighlight(line string, dl components.DiffLine) string {
	if dl.Type == components.LineHunk {
		return line
	}

	prefix, inner, suffix := SplitDiffBorders(line)

	inner = strings.Replace(inner, "\033[1m+\033[0m", "\033[1m>\033[0m", 1)
	inner = strings.Replace(inner, "\033[1m-\033[0m", "\033[1m>\033[0m", 1)

	colors := d.Ctx.DiffColors
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
		inner = replaceBackground(inner, colors, selBg)
	}

	return prefix + inner + suffix
}

// replaceBackground swaps diff bg colors for selected bg in an ANSI string.
func replaceBackground(inner string, colors styles.DiffColors, selBg string) string {
	if colors.AddBg != "" {
		inner = strings.ReplaceAll(inner, colors.AddBg, selBg)
	}
	if colors.DelBg != "" {
		inner = strings.ReplaceAll(inner, colors.DelBg, selBg)
	}
	inner = strings.ReplaceAll(inner, "\033[0m", "\033[0m"+selBg)
	inner = strings.ReplaceAll(inner, "\033[m", "\033[m"+selBg)
	inner = selBg + inner + "\033[0m"
	return inner
}

// --- Layout rendering ---

// RenderLayout composes the file tree (left) and diff view (right) into the final output.
func (d DiffViewer) RenderLayout(rightView string, rightTitle string, info LayoutInfo) string {
	treeW := d.Tree.Width
	chromeRows := 2 // header + separator (footer only affects left panel)

	// Chrome color: use bar background from terminal, fall back to BrightBlack.
	var chromeClr color.Color = lipgloss.BrightBlack
	if d.Ctx.ChromeColor != nil {
		chromeClr = d.Ctx.ChromeColor
	}
	chrome := lipgloss.NewStyle().Foreground(chromeClr)
	dim := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	bold := lipgloss.NewStyle().Bold(true)

	// Active panel gets bright title, inactive gets dim.
	var treeTitleStyle, rightTitleStyle lipgloss.Style
	if d.Tree.Focused {
		treeTitleStyle = bold.Foreground(lipgloss.BrightWhite)
		rightTitleStyle = dim
	} else {
		treeTitleStyle = dim
		rightTitleStyle = bold.Foreground(lipgloss.BrightWhite)
	}

	sep := chrome.Render("│")
	rightW := d.RightPanelWidth()

	// === Header: left panel ===
	// " N Files ... MODE_TEXT "
	var treeLabel string
	n := len(d.Files)
	if n == 0 {
		treeLabel = treeTitleStyle.Render("Files")
	} else if n == 1 {
		treeLabel = treeTitleStyle.Render(fmt.Sprintf("%d File", n))
	} else {
		treeLabel = treeTitleStyle.Render(fmt.Sprintf("%d Files", n))
	}

	var modeLabel string
	if info.ModeName != "" && info.ModeColor != nil {
		modeStyle := lipgloss.NewStyle().Foreground(info.ModeColor)
		modeLabel = modeStyle.Render(strings.ToUpper(info.ModeName))
	}

	treeLabelW := lipgloss.Width(treeLabel)
	modeLabelW := lipgloss.Width(modeLabel)
	treeHeaderPad := treeW - 1 - treeLabelW - modeLabelW - 1 // -1 leading space, -1 for sep
	if treeHeaderPad < 0 {
		treeHeaderPad = 0
	}
	treeHeader := " " + treeLabel + strings.Repeat(" ", treeHeaderPad) + modeLabel

	// === Header: right panel ===
	// " dir/filename ... +N -M  PR #42  ◀ 42% "
	var rightLabel string
	var rightTrailer string // right-aligned: stats + PR + scroll
	if rightTitle == "Overview" {
		rightLabel = rightTitleStyle.Render("Overview")
	} else {
		dir, file := path.Split(rightTitle)
		if dir != "" {
			rightLabel = dim.Render(dir) + rightTitleStyle.Render(file)
		} else {
			rightLabel = rightTitleStyle.Render(rightTitle)
		}
	}

	// Build right-aligned trailer parts.
	var trailerParts []string

	// File stats (+N -M).
	if d.CurrentFileIdx >= 0 && d.CurrentFileIdx < len(d.Files) {
		f := d.Files[d.CurrentFileIdx]
		green := lipgloss.NewStyle().Foreground(lipgloss.Green)
		red := lipgloss.NewStyle().Foreground(lipgloss.Red)
		var statParts []string
		if f.Additions > 0 {
			statParts = append(statParts, green.Render(fmt.Sprintf("+%d", f.Additions)))
		}
		if f.Deletions > 0 {
			statParts = append(statParts, red.Render(fmt.Sprintf("-%d", f.Deletions)))
		}
		if len(statParts) > 0 {
			trailerParts = append(trailerParts, strings.Join(statParts, " "))
		}
	}

	// PR badge — rendered in footer next to branch name, not in header.

	if len(trailerParts) > 0 {
		rightTrailer = strings.Join(trailerParts, dim.Render("  "))
	}

	rightLabelW := lipgloss.Width(rightLabel)
	rightTrailerW := lipgloss.Width(rightTrailer)
	rightHeaderGap := rightW - 2 - rightLabelW - rightTrailerW // -2 for leading/trailing space
	if rightHeaderGap < 0 {
		rightHeaderGap = 0
	}
	rightHeader := " " + rightLabel + strings.Repeat(" ", rightHeaderGap) + rightTrailer + " "
	headerLine := treeHeader + sep + rightHeader

	// Separator row: thin horizontal rule.
	treeFill := treeW - 1
	if treeFill < 0 {
		treeFill = 0
	}
	rightFill := rightW - 1
	if rightFill < 0 {
		rightFill = 0
	}
	separatorLine := chrome.Render(strings.Repeat("─", treeFill) + "┼" + strings.Repeat("─", rightFill))

	// Content area.
	contentH := d.Height - chromeRows

	// Copilot session status: 2 extra footer rows when sessions are running.
	copilotCount := len(d.CopilotState.Pending)
	copilotFooterRows := 0
	if copilotCount > 0 {
		copilotFooterRows = 2 // separator + status line
	}

	tree := d.Tree
	tree.Width = treeW - 1
	tree.Height = contentH - 2 - copilotFooterRows // tree loses rows for footer(s)
	tree.CurrentFileIdx = d.CurrentFileIdx
	treeContentLines := tree.View()
	rightLines := strings.Split(rightView, "\n")

	innerRightW := rightW - 1

	// Scrollbar: compute thumb position and size.
	totalLines := d.VP.TotalLineCount()
	vpH := contentH
	var thumbStart, thumbLen int
	if totalLines <= vpH || totalLines == 0 {
		// Content fits — no scrollbar needed.
		thumbStart = -1
		thumbLen = 0
	} else {
		thumbLen = vpH * vpH / totalLines
		if thumbLen < 1 {
			thumbLen = 1
		}
		scrollable := totalLines - vpH
		offset := d.VP.YOffset()
		if offset > scrollable {
			offset = scrollable
		}
		thumbStart = offset * (vpH - thumbLen) / scrollable
	}
	// Scrollbar styles: track matches border color, thumb slightly lighter.
	trackChar := chrome.Render("│")
	thumbColor := uictx.BrightnessModify(chromeClr, 40)
	thumbChar := lipgloss.NewStyle().Foreground(thumbColor).Render("┃")

	var b strings.Builder
	b.WriteString(headerLine)
	b.WriteString("\n")
	b.WriteString(separatorLine)
	b.WriteString("\n")

	// Footer row indices (counted from bottom of contentH).
	branchRow := contentH - 1
	branchSepRow := contentH - 2
	copilotSepRow := branchSepRow - 2 // only used when copilotFooterRows > 0
	copilotRow := branchSepRow - 1    // only used when copilotFooterRows > 0
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Yellow)

	for i := 0; i < contentH; i++ {
		// Left panel: tree content for most rows, footer rows at the bottom.
		var leftPart string
		if copilotFooterRows > 0 && i == copilotSepRow {
			// Separator above copilot status line.
			leftPart = chrome.Render(strings.Repeat("─", treeW-1))
		} else if copilotFooterRows > 0 && i == copilotRow {
			// Copilot session status line.
			label := " 1 Copilot session running"
			if copilotCount > 1 {
				label = fmt.Sprintf(" %d Copilot sessions running", copilotCount)
			}
			copilotText := yellow.Render(label)
			textW := lipgloss.Width(copilotText)
			pad := treeW - 1 - textW
			if pad < 0 {
				pad = 0
			}
			leftPart = copilotText + strings.Repeat(" ", pad)
		} else if i == branchSepRow {
			// Footer separator (only on tree side).
			leftPart = chrome.Render(strings.Repeat("─", treeW-1))
		} else if i == branchRow {
			// Branch name (left) + PR badge (right).
			prText := ""
			prW := 0
			if info.PR != nil {
				prURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d", info.PR.RepoOwner(), info.PR.RepoName(), info.PR.Number)
				prStyle := lipgloss.NewStyle().
					Foreground(lipgloss.Cyan).
					Hyperlink(prURL)
				prText = prStyle.Render(fmt.Sprintf("PR#%d", info.PR.Number)) + " "
				prW = lipgloss.Width(prText)
			}
			branchText := ""
			if info.BranchName != "" {
				name := info.BranchName
				// usable = treeW-1; branchText = " "+name; need gap>=1 when PR present
				minGap := 0
				if prW > 0 {
					minGap = 1
				}
				maxW := treeW - 1 - prW - minGap - 1 // -1 for leading space in branchText
				if maxW < 4 {
					maxW = 4
				}
				if len(name) > maxW {
					name = name[:maxW-1] + "…"
				}
				branchText = dim.Render(" " + name)
			}
			branchW := lipgloss.Width(branchText)
			gap := treeW - 1 - branchW - prW
			if gap < 0 {
				gap = 0
			}
			leftPart = branchText + strings.Repeat(" ", gap) + prText
		} else {
			tl := ""
			if i < len(treeContentLines) {
				tl = treeContentLines[i]
			}
			tlW := lipgloss.Width(tl)
			treePad := treeW - 1 - tlW
			if treePad < 0 {
				treePad = 0
			}
			leftPart = tl + strings.Repeat(" ", treePad)
		}

		rl := ""
		if i < len(rightLines) {
			rl = rightLines[i]
		}
		rlW := lipgloss.Width(rl)
		contentW := innerRightW - 1 // -1 for scrollbar column
		if rlW < contentW {
			rl += strings.Repeat(" ", contentW-rlW)
		}

		// Scrollbar column.
		var scrollCol string
		if thumbStart < 0 {
			scrollCol = " " // no scrollbar needed
		} else if i >= thumbStart && i < thumbStart+thumbLen {
			scrollCol = thumbChar
		} else {
			scrollCol = trackChar
		}

		b.WriteString(leftPart + sep + rl + scrollCol)
		if i < contentH-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}

// --- File formatting ---

// renderContext builds a RenderContext for the current viewer state using
// the given diff lines (needed for gutter column width).
func (d *DiffViewer) renderContext(diffLines []components.DiffLine) components.RenderContext {
	colW := components.GutterColWidth(diffLines)
	return components.RenderContext{
		Width:      d.ContentWidth(),
		Colors:     d.Ctx.DiffColors,
		ColW:       colW,
		RenderBody: d.RenderBody,
		AnimFrame:  d.CopilotState.Dots,
	}
}

// RenderContextForFile returns a RenderContext for the given file index.
func (d *DiffViewer) RenderContextForFile(index int) components.RenderContext {
	return d.renderContext(d.FileDiffs[index])
}

// CommentsForFile gathers base comments for a file from the CommentSource.
// Does not include pending copilot comments — use CopilotState.PendingRenderComments
// for those (they carry block data that can't be expressed as ReviewComment).
func (d DiffViewer) CommentsForFile(filename string) []github.ReviewComment {
	var comments []github.ReviewComment
	if d.Comments != nil {
		comments = d.Comments.CommentsForFile(filename)
	}
	return comments
}

// blocksForFile returns a block lookup for the current file if the CommentSource
// supports it, or nil otherwise.
func (d DiffViewer) blocksForFile(filename string) map[int][]comments.ContentBlock {
	if bs, ok := d.Comments.(BlockSource); ok {
		return bs.BlocksForFile(filename)
	}
	return nil
}

// syncFromRenderList derives RenderedFiles and FileDiffOffsets
// from the render list for the given file index.
func (d *DiffViewer) syncFromRenderList(index int, rc components.RenderContext) {
	list := d.FileRenderLists[index]
	if index < len(d.RenderedFiles) {
		d.RenderedFiles[index] = list.String(rc)
	}
	if index < len(d.FileDiffOffsets) {
		numDiffLines := len(d.FileDiffs[index])
		d.FileDiffOffsets[index] = list.DiffLineOffsets(numDiffLines, rc)
	}
}

// FormatFile renders a file and caches the result via the render list.
// Uses the CommentSource to gather comments automatically.
func (d *DiffViewer) FormatFile(index int) {
	if index < 0 || index >= len(d.HighlightedFiles) || d.HighlightedFiles[index].File.Filename == "" {
		return
	}
	hl := d.HighlightedFiles[index]
	filename := d.Files[index].Filename
	fileComments := d.CommentsForFile(filename)

	var opts components.DiffFormatOptions
	if d.RenderBody != nil {
		opts.RenderBody = d.RenderBody
	}
	if pending := d.CopilotState.PendingRenderComments(filename); len(pending) > 0 {
		opts.PendingComments = pending
	}
	// Use block-aware threading if the source supports it.
	if blockLookup := d.blocksForFile(filename); blockLookup != nil {
		opts.ThreadedComments = components.BuildThreadedRenderComments(fileComments, blockLookup)
	}

	diffLines := make([]components.DiffLine, len(hl.DiffLines))
	copy(diffLines, hl.DiffLines)
	colW := components.GutterColWidth(diffLines)
	components.FormatDiffLinesFromHL(diffLines, hl.HlLines, hl.HlLinesOld, hl.Filename, d.ContentWidth(), d.Ctx.DiffColors, colW)

	// Build the render list and derive all cached fields from it.
	if index < len(d.FileRenderLists) {
		d.FileRenderLists[index] = components.BuildRenderList(diffLines, fileComments, opts)
		rc := d.renderContext(diffLines)
		d.syncFromRenderList(index, rc)
	}
}

// FormatFileWithComments renders a file with explicit comments (no CommentSource).
// Used when the caller has already gathered comments.
func (d *DiffViewer) FormatFileWithComments(index int, fileComments []github.ReviewComment) {
	if index >= len(d.HighlightedFiles) {
		return
	}
	hl := d.HighlightedFiles[index]
	if hl.File.Filename == "" {
		return
	}

	var opts components.DiffFormatOptions
	if d.RenderBody != nil {
		opts.RenderBody = d.RenderBody
	}

	diffLines := make([]components.DiffLine, len(hl.DiffLines))
	copy(diffLines, hl.DiffLines)
	colW := components.GutterColWidth(diffLines)
	components.FormatDiffLinesFromHL(diffLines, hl.HlLines, hl.HlLinesOld, hl.Filename, d.ContentWidth(), d.Ctx.DiffColors, colW)

	if index < len(d.FileRenderLists) {
		d.FileRenderLists[index] = components.BuildRenderList(diffLines, fileComments, opts)
		rc := d.renderContext(diffLines)
		d.syncFromRenderList(index, rc)
	}
}

// ReformatAllFiles invalidates all caches and re-renders the current file.
func (d *DiffViewer) ReformatAllFiles() {
	for i := range d.RenderedFiles {
		d.RenderedFiles[i] = ""
	}
	for _, list := range d.FileRenderLists {
		if list != nil {
			list.InvalidateAll()
		}
	}
	if d.CurrentFileIdx >= 0 {
		d.FormatFile(d.CurrentFileIdx)
	}
}

// SpliceThreadForComment re-renders a single comment thread in the render list.
func (d *DiffViewer) SpliceThreadForComment(fileIdx int, side string, line int) {
	d.SpliceThreadWithHighlight(fileIdx, side, line, false, 0)
}

// SpliceThreadWithHighlight replaces a comment thread in the render list with
// updated content (e.g. highlight state change, new reply).
func (d *DiffViewer) SpliceThreadWithHighlight(fileIdx int, side string, line int, highlighted bool, hlIdx int) {
	if fileIdx < 0 || fileIdx >= len(d.FileRenderLists) || d.FileRenderLists[fileIdx] == nil {
		d.FormatFile(fileIdx)
		return
	}
	list := d.FileRenderLists[fileIdx]
	ti := list.FindThread(side, line)
	if ti < 0 {
		d.FormatFile(fileIdx)
		return
	}

	old, ok := list.Items[ti].(*components.CommentThreadItem)
	if !ok {
		d.FormatFile(fileIdx)
		return
	}

	filename := d.Files[fileIdx].Filename
	fileComments := d.CommentsForFile(filename)
	threadComments := components.CommentsForThread(fileComments, side, line)

	// Convert to RenderComments, preserving blocks if available.
	var rendered []components.RenderComment
	if blockLookup := d.blocksForFile(filename); blockLookup != nil {
		for _, c := range threadComments {
			if blocks, ok := blockLookup[c.ID]; ok && len(blocks) > 0 {
				rendered = append(rendered, components.RenderComment{
					ID:        c.ID,
					Author:    c.User.Login,
					CreatedAt: c.CreatedAt,
					Blocks:    blocks,
				})
			} else {
				rendered = append(rendered, components.ReviewCommentToRender(c))
			}
		}
	} else {
		rendered = components.ReviewCommentsToRender(threadComments)
	}

	// Append any pending copilot comments for this thread.
	if pending := d.CopilotState.PendingRenderComments(filename); len(pending) > 0 {
		pk := components.CommentKey{Side: side, Line: line}
		rendered = append(rendered, pending[pk]...)
	}

	replacement := components.NewCommentThreadItem(old.DiffIdx(), side, line, rendered, old.ParentLineType)
	replacement.Highlighted = highlighted
	replacement.HlIdx = hlIdx
	replacement.OpenBottom = old.OpenBottom
	list.ReplaceThread(side, line, replacement)

	rc := d.renderContext(d.FileDiffs[fileIdx])
	d.syncFromRenderList(fileIdx, rc)
}

// RemoveThread removes a comment thread from the render list.
// Returns true if the thread was found and removed.
func (d *DiffViewer) RemoveThread(fileIdx int, side string, line int) bool {
	if fileIdx < 0 || fileIdx >= len(d.FileRenderLists) || d.FileRenderLists[fileIdx] == nil {
		return false
	}
	list := d.FileRenderLists[fileIdx]
	if !list.RemoveThread(side, line) {
		return false
	}

	rc := d.renderContext(d.FileDiffs[fileIdx])
	d.syncFromRenderList(fileIdx, rc)
	return true
}

// InsertThread inserts a new comment thread into the render list.
// Returns true if the thread was successfully inserted.
func (d *DiffViewer) InsertThread(fileIdx int, diffLineIdx int, side string, line int, comments []github.ReviewComment) bool {
	if fileIdx < 0 || fileIdx >= len(d.FileRenderLists) || d.FileRenderLists[fileIdx] == nil {
		return false
	}

	lt := components.LineAdd
	if diffLineIdx < len(d.FileDiffs[fileIdx]) {
		lt = d.FileDiffs[fileIdx][diffLineIdx].Type
	}

	item := components.NewCommentThreadItem(diffLineIdx, side, line, components.ReviewCommentsToRender(comments), lt)
	d.FileRenderLists[fileIdx].InsertAfterDiffLine(diffLineIdx, item)

	rc := d.renderContext(d.FileDiffs[fileIdx])
	d.syncFromRenderList(fileIdx, rc)
	return true
}

// FileIndexForPath returns the index of the file with the given path, or -1.
func (d DiffViewer) FileIndexForPath(path string) int {
	for i, f := range d.Files {
		if f.Filename == path {
			return i
		}
	}
	return -1
}

// ThreadEndOffset returns the rendered line offset immediately after the
// comment thread at (side, line) for the given file index.
// Returns -1 if the thread or render list is not found.
func (d *DiffViewer) ThreadEndOffset(fileIdx int, side string, line int) int {
	if fileIdx < 0 || fileIdx >= len(d.FileRenderLists) || d.FileRenderLists[fileIdx] == nil {
		return -1
	}
	if fileIdx >= len(d.FileDiffs) {
		return -1
	}
	rc := d.renderContext(d.FileDiffs[fileIdx])
	return d.FileRenderLists[fileIdx].ThreadEndOffset(side, line, rc)
}

// SetThreadOpenBottom sets or clears the OpenBottom flag on a thread, invalidates
// it, and re-syncs the render list so that RenderedFiles and offsets are updated.
func (d *DiffViewer) SetThreadOpenBottom(fileIdx int, side string, line int, open bool) {
	if fileIdx < 0 || fileIdx >= len(d.FileRenderLists) || d.FileRenderLists[fileIdx] == nil {
		return
	}
	list := d.FileRenderLists[fileIdx]
	ti := list.FindThread(side, line)
	if ti < 0 {
		return
	}
	ct, ok := list.Items[ti].(*components.CommentThreadItem)
	if !ok {
		return
	}
	ct.OpenBottom = open
	ct.Invalidate()
	list.MarkDirty()
	rc := d.renderContext(d.FileDiffs[fileIdx])
	d.syncFromRenderList(fileIdx, rc)
}

// ThreadParentLineType returns the ParentLineType of the comment thread at (side, line)
// for the given file. Returns LineContext if the thread is not found.
func (d *DiffViewer) ThreadParentLineType(fileIdx int, side string, line int) components.LineType {
	if fileIdx < 0 || fileIdx >= len(d.FileRenderLists) || d.FileRenderLists[fileIdx] == nil {
		return components.LineContext
	}
	list := d.FileRenderLists[fileIdx]
	ti := list.FindThread(side, line)
	if ti < 0 {
		return components.LineContext
	}
	ct, ok := list.Items[ti].(*components.CommentThreadItem)
	if !ok {
		return components.LineContext
	}
	return ct.ParentLineType
}

// --- Viewport helpers ---

// RebuildContent sets the viewport content using the provided builders.
// buildOverview is called when CurrentFileIdx == -1, buildFile otherwise.
func (d *DiffViewer) RebuildContent(buildOverview func(w int) string, buildFile func(w int) string) {
	innerW := d.RightPanelInnerWidth()
	innerH := d.Height - 2

	if !d.VPReady {
		d.VP = viewport.New()
		d.VPReady = true
	}
	d.VP.SetWidth(innerW)
	d.VP.SetHeight(innerH)

	var content string
	if d.CurrentFileIdx == -1 {
		content = buildOverview(innerW)
	} else {
		content = buildFile(innerW)
	}
	d.VP.SetContent(content)
}

// RebuildContentIfChanged only updates the viewport if the content changed.
func (d *DiffViewer) RebuildContentIfChanged(buildOverview func(w int) string, buildFile func(w int) string) {
	innerW := d.RightPanelInnerWidth()
	innerH := d.Height - 2

	if !d.VPReady {
		d.VP = viewport.New()
		d.VPReady = true
	}
	d.VP.SetWidth(innerW)
	d.VP.SetHeight(innerH)

	var content string
	if d.CurrentFileIdx == -1 {
		content = buildOverview(innerW)
	} else {
		content = buildFile(innerW)
	}
	if content != d.LastContent {
		d.LastContent = content
		d.VP.SetContent(content)
	}
}

// InitFileSlices allocates the per-file slices for a new set of files.
func (d *DiffViewer) InitFileSlices(n int) {
	d.HighlightedFiles = make([]components.HighlightedDiff, n)
	d.RenderedFiles = make([]string, n)
	d.FileDiffs = make([][]components.DiffLine, n)
	d.FileDiffOffsets = make([][]int, n)
	d.FileRenderLists = make([]*components.FileRenderList, n)
}

// --- Spinner helpers ---

// InitSpinner creates the spinner model.
func (d *DiffViewer) InitSpinner() {
	d.Spinner = spinner.New(spinner.WithSpinner(spinner.MiniDot))
	d.Spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
}

// SpinnerView renders a centered loading spinner for the diff viewport.
func (d DiffViewer) SpinnerView() string {
	h := d.ViewportHeight()
	w := d.RightPanelInnerWidth()
	label := d.Spinner.View() + " Highlighting…"
	labelW := lipgloss.Width(label)

	var b strings.Builder
	topPad := h/2 - 1
	if topPad < 0 {
		topPad = 0
	}
	for i := 0; i < topPad; i++ {
		b.WriteString("\n")
	}
	leftPad := (w - labelW) / 2
	if leftPad < 0 {
		leftPad = 0
	}
	b.WriteString(strings.Repeat(" ", leftPad) + label)
	return b.String()
}

// --- Standalone helpers ---

// SplitDiffBorders splits a rendered diff line into border prefix, inner content, and border suffix.
func SplitDiffBorders(line string) (prefix, inner, suffix string) {
	const borderChar = "│"

	firstIdx := strings.Index(line, borderChar)
	if firstIdx < 0 {
		return "", line, ""
	}

	lastIdx := strings.LastIndex(line, borderChar)
	if lastIdx == firstIdx {
		return "", line, ""
	}

	prefixEnd := firstIdx + len(borderChar)
	if prefixEnd < len(line) && line[prefixEnd] == '\033' {
		if i := strings.IndexByte(line[prefixEnd:], 'm'); i >= 0 {
			prefixEnd += i + 1
		}
	}

	suffixStart := lastIdx
	for i := lastIdx - 1; i >= prefixEnd; i-- {
		if line[i] == '\033' {
			suffixStart = i
			break
		}
	}

	return line[:prefixEnd], line[prefixEnd:suffixStart], line[suffixStart:]
}

// KeyResult is returned by HandleNavKey to indicate what happened.
type KeyResult int

const (
	KeyNotHandled KeyResult = iota
	KeyHandled
)

// HandleNavKey handles common navigation keys (j/k/J/K/ctrl+d/u/f/b/G/gg/f/h/l).
// Returns KeyHandled if the key was processed, KeyNotHandled otherwise.
// Caller should check blocked (e.g. sidebar open) before calling.
func (d *DiffViewer) HandleNavKey(key string) KeyResult {
	switch key {
	case "f":
		d.Tree.Focused = !d.Tree.Focused
		return KeyHandled
	case "h", "left":
		d.Tree.Focused = true
		return KeyHandled
	case "l", "right":
		d.Tree.Focused = false
		return KeyHandled
	case "j", "down":
		if d.Tree.Focused {
			d.Tree.MoveCursorBy(1)
			return KeyHandled
		}
		if d.CurrentFileIdx >= 0 && d.HasDiffLines() {
			d.SelectionAnchor = -1
			d.MoveDiffCursor(1)
			return KeyHandled
		}
	case "k", "up":
		if d.Tree.Focused {
			d.Tree.MoveCursorBy(-1)
			return KeyHandled
		}
		if d.CurrentFileIdx >= 0 && d.HasDiffLines() {
			d.SelectionAnchor = -1
			d.MoveDiffCursor(-1)
			return KeyHandled
		}
	case "J", "shift+down":
		if !d.Tree.Focused && d.CurrentFileIdx >= 0 && d.HasDiffLines() {
			if d.SelectionAnchor < 0 {
				d.SelectionAnchor = d.DiffCursor
			}
			d.MoveDiffCursor(1)
			return KeyHandled
		}
	case "K", "shift+up":
		if !d.Tree.Focused && d.CurrentFileIdx >= 0 && d.HasDiffLines() {
			if d.SelectionAnchor < 0 {
				d.SelectionAnchor = d.DiffCursor
			}
			d.MoveDiffCursor(-1)
			return KeyHandled
		}
	case "ctrl+d":
		d.SelectionAnchor = -1
		if d.Tree.Focused {
			d.Tree.MoveCursorBy(d.Height / 2)
		} else if d.CurrentFileIdx >= 0 && d.HasDiffLines() {
			d.ScrollAndSyncCursor(d.Height / 2)
		} else {
			d.VP.SetYOffset(d.VP.YOffset() + d.Height/2)
		}
		return KeyHandled
	case "ctrl+u":
		d.SelectionAnchor = -1
		if d.Tree.Focused {
			d.Tree.MoveCursorBy(-d.Height / 2)
		} else if d.CurrentFileIdx >= 0 && d.HasDiffLines() {
			d.ScrollAndSyncCursor(-d.Height / 2)
		} else {
			d.VP.SetYOffset(d.VP.YOffset() - d.Height/2)
		}
		return KeyHandled
	case "ctrl+f":
		d.SelectionAnchor = -1
		if d.Tree.Focused {
			d.Tree.MoveCursorBy(d.Height)
		} else if d.CurrentFileIdx >= 0 && d.HasDiffLines() {
			d.ScrollAndSyncCursor(d.Height)
		} else {
			d.VP.SetYOffset(d.VP.YOffset() + d.Height)
		}
		return KeyHandled
	case "ctrl+b":
		d.SelectionAnchor = -1
		if d.Tree.Focused {
			d.Tree.MoveCursorBy(-d.Height)
		} else if d.CurrentFileIdx >= 0 && d.HasDiffLines() {
			d.ScrollAndSyncCursor(-d.Height)
		} else {
			d.VP.SetYOffset(d.VP.YOffset() - d.Height)
		}
		return KeyHandled
	case "G":
		d.WaitingG = false
		if d.Tree.Focused {
			totalEntries := 2 + len(d.Tree.Entries)
			d.Tree.MoveCursorBy(totalEntries)
		} else {
			d.VP.GotoBottom()
			if d.CurrentFileIdx >= 0 && d.HasDiffLines() {
				d.SyncDiffCursorToViewport()
			}
		}
		return KeyHandled
	case "g":
		if d.WaitingG {
			d.WaitingG = false
			if d.Tree.Focused {
				d.Tree.Cursor = 0
			} else {
				d.VP.GotoTop()
				if d.CurrentFileIdx >= 0 && d.HasDiffLines() {
					d.DiffCursor = 0
					// Skip past hunk header if line 0 is one.
					lines := d.FileDiffs[d.CurrentFileIdx]
					for d.DiffCursor < len(lines) && lines[d.DiffCursor].Type == components.LineHunk {
						d.DiffCursor++
					}
				}
			}
			return KeyHandled
		}
		d.WaitingG = true
		return KeyHandled
	}
	return KeyNotHandled
}
