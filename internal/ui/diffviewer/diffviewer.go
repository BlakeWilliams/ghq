package diffviewer

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/review/comments"
	"github.com/blakewilliams/ghq/internal/review/copilot"
	"github.com/blakewilliams/ghq/internal/ui/components"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	"github.com/blakewilliams/ghq/internal/ui/uictx"
)

const scrollMargin = 5

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
	FileCommentPositions [][]components.CommentPosition
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
	Copilot         *copilot.Client
	CopilotReplyBuf map[string]string        // commentID -> accumulated reply content
	CopilotPending  map[string]CopilotPendingInfo // commentID -> pending info
	CopilotDots     int                      // shared animation frame (0-3)

	// Comment source — set by outer model. Returns base comments for a file
	// (without copilot pending, which DiffViewer appends itself).
	Comments CommentSource

	// Render body callback for markdown in comment threads.
	RenderBody func(body string, width int, bg string) string

	// Render cache per file (for splice-based updates).
	FileRenderCache []*components.DiffRenderResult

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

// --- Layout helpers ---

func (d DiffViewer) RightPanelWidth() int {
	return d.Width - d.Tree.Width
}

func (d DiffViewer) RightPanelInnerWidth() int {
	return d.RightPanelWidth() - 2
}

func (d DiffViewer) ContentWidth() int {
	return d.RightPanelInnerWidth()
}

func (d DiffViewer) ViewportHeight() int {
	return d.Height - 2
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
func (d DiffViewer) RenderLayout(rightView string, rightTitle string) string {
	treeW := d.Tree.Width
	innerTreeW := treeW - 2
	innerTreeH := d.Height - 2

	bc := d.BorderStyle()
	var treeBorderStyle lipgloss.Style
	if d.Tree.Focused {
		treeBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	} else {
		treeBorderStyle = bc
	}

	// Tree border.
	titleStr := " " + lipgloss.NewStyle().Bold(true).Render("Files") + " "
	titleW := lipgloss.Width(titleStr)
	fillW := treeW - 3 - titleW
	if fillW < 0 {
		fillW = 0
	}
	topBorder := treeBorderStyle.Render("╭─") + titleStr + treeBorderStyle.Render(strings.Repeat("─", fillW)+"╮")
	bw := treeW - 2
	if bw < 0 {
		bw = 0
	}
	bottomBorder := treeBorderStyle.Render("╰" + strings.Repeat("─", bw) + "╯")
	sideBorderL := treeBorderStyle.Render("│")
	sideBorderR := treeBorderStyle.Render("│")

	// Temporarily set tree dimensions for rendering.
	tree := d.Tree
	tree.Width = innerTreeW
	tree.Height = innerTreeH
	tree.CurrentFileIdx = d.CurrentFileIdx
	treeContentLines := tree.View()
	rightLines := strings.Split(rightView, "\n")

	// Right panel border.
	rightW := d.RightPanelWidth()
	innerRightW := rightW - 2
	var rightBorderStyle lipgloss.Style
	if !d.Tree.Focused {
		rightBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	} else {
		rightBorderStyle = bc
	}

	rtTitle := " " + lipgloss.NewStyle().Bold(true).Render(rightTitle) + " "
	rtW := lipgloss.Width(rtTitle)
	rtFill := rightW - 3 - rtW
	if rtFill < 0 {
		rtFill = 0
	}
	rightTop := rightBorderStyle.Render("╭─") + rtTitle + rightBorderStyle.Render(strings.Repeat("─", rtFill)+"╮")
	rbw := rightW - 2
	if rbw < 0 {
		rbw = 0
	}
	rightBottom := rightBorderStyle.Render("╰" + strings.Repeat("─", rbw) + "╯")
	rightSideL := rightBorderStyle.Render("│")
	rightSideR := rightBorderStyle.Render("│")

	var b strings.Builder
	for i := 0; i < d.Height; i++ {
		var treeLine string
		if i == 0 {
			treeLine = topBorder
		} else if i == d.Height-1 {
			treeLine = bottomBorder
		} else {
			tIdx := i - 1
			cl := ""
			if tIdx < len(treeContentLines) {
				cl = treeContentLines[tIdx]
			}
			treeLine = sideBorderL + cl + sideBorderR
		}

		var rightLine string
		if i == 0 {
			rightLine = rightTop
		} else if i == d.Height-1 {
			rightLine = rightBottom
		} else {
			rIdx := i - 1
			rl := ""
			if rIdx < len(rightLines) {
				rl = rightLines[rIdx]
			}
			rlW := lipgloss.Width(rl)
			if rlW < innerRightW {
				rl += strings.Repeat(" ", innerRightW-rlW)
			}
			rightLine = rightSideL + rl + rightSideR
		}

		b.WriteString(treeLine + rightLine)
		if i < d.Height-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// --- File formatting ---

// CommentsForFile gathers comments for a file: base comments from the
// CommentSource + copilot pending comments.
func (d DiffViewer) CommentsForFile(filename string) []github.ReviewComment {
	var comments []github.ReviewComment
	if d.Comments != nil {
		comments = d.Comments.CommentsForFile(filename)
	}
	return d.AppendCopilotPending(filename, comments)
}

// FormatFile renders a file and caches the result (content + offsets + splice data).
// Uses the CommentSource to gather comments automatically.
func (d *DiffViewer) FormatFile(index int) {
	if index < 0 || index >= len(d.HighlightedFiles) || d.HighlightedFiles[index].File.Filename == "" {
		return
	}
	hl := d.HighlightedFiles[index]
	width := d.ContentWidth()
	fileComments := d.CommentsForFile(d.Files[index].Filename)

	var opts components.DiffFormatOptions
	if d.RenderBody != nil {
		opts.RenderBody = d.RenderBody
	}

	result := components.FormatDiffFile(hl, width, d.Ctx.DiffColors, fileComments, opts)
	if index < len(d.RenderedFiles) {
		d.RenderedFiles[index] = result.Content
	}
	if index < len(d.FileDiffOffsets) {
		d.FileDiffOffsets[index] = result.DiffLineOffsets
	}
	if index < len(d.FileCommentPositions) {
		d.FileCommentPositions[index] = result.CommentPositions
	}
	if index < len(d.FileRenderCache) {
		d.FileRenderCache[index] = &result
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
	width := d.ContentWidth()

	var opts components.DiffFormatOptions
	if d.RenderBody != nil {
		opts.RenderBody = d.RenderBody
	}

	result := components.FormatDiffFile(hl, width, d.Ctx.DiffColors, fileComments, opts)
	if index < len(d.RenderedFiles) {
		d.RenderedFiles[index] = result.Content
	}
	if index < len(d.FileDiffOffsets) {
		d.FileDiffOffsets[index] = result.DiffLineOffsets
	}
	if index < len(d.FileCommentPositions) {
		d.FileCommentPositions[index] = result.CommentPositions
	}
	if index < len(d.FileRenderCache) {
		d.FileRenderCache[index] = &result
	}
}

// ReformatAllFiles invalidates all caches and re-renders the current file.
func (d *DiffViewer) ReformatAllFiles() {
	for i := range d.RenderedFiles {
		d.RenderedFiles[i] = ""
	}
	for i := range d.FileRenderCache {
		d.FileRenderCache[i] = nil
	}
	if d.CurrentFileIdx >= 0 {
		d.FormatFile(d.CurrentFileIdx)
	}
}

// SpliceThreadForComment re-renders a single comment thread and splices it
// into the cached render for the given file. O(thread) instead of O(n).
func (d *DiffViewer) SpliceThreadForComment(fileIdx int, side string, line int) {
	d.SpliceThreadWithHighlight(fileIdx, side, line, false, 0)
}

// SpliceThreadWithHighlight re-renders a single comment thread with optional
// highlighting and splices it into the cached render. O(thread) instead of O(n).
func (d *DiffViewer) SpliceThreadWithHighlight(fileIdx int, side string, line int, highlighted bool, hlIdx int) {
	if fileIdx < 0 || fileIdx >= len(d.FileRenderCache) || d.FileRenderCache[fileIdx] == nil {
		d.FormatFile(fileIdx)
		return
	}
	rc := d.FileRenderCache[fileIdx]
	if len(rc.ThreadRanges) == 0 {
		d.FormatFile(fileIdx)
		return
	}

	threadIdx := -1
	for i, tr := range rc.ThreadRanges {
		if tr.Side == side && tr.Line == line {
			threadIdx = i
			break
		}
	}
	if threadIdx < 0 {
		d.FormatFile(fileIdx)
		return
	}

	diffLineIdx := rc.ThreadRanges[threadIdx].DiffLineIdx
	lt := components.LineAdd
	if diffLineIdx < len(d.FileDiffs[fileIdx]) {
		lt = d.FileDiffs[fileIdx][diffLineIdx].Type
	}

	fileComments := d.CommentsForFile(d.Files[fileIdx].Filename)
	threadComments := components.CommentsForThread(fileComments, side, line)
	gutterW := components.TotalGutterWidth(components.GutterColWidth(d.FileDiffs[fileIdx]))

	newContent := components.RenderSingleThread(threadComments, d.ContentWidth(), lt, d.Ctx.DiffColors, highlighted, hlIdx, d.RenderBody, gutterW)
	components.SpliceThread(rc, threadIdx, newContent)

	d.RenderedFiles[fileIdx] = rc.Content
	if fileIdx < len(d.FileDiffOffsets) {
		d.FileDiffOffsets[fileIdx] = rc.DiffLineOffsets
	}
}

// RemoveThread removes a comment thread from the cached render.
// Used when resolving a thread. Returns true if the thread was found and removed.
func (d *DiffViewer) RemoveThread(fileIdx int, side string, line int) bool {
	if fileIdx < 0 || fileIdx >= len(d.FileRenderCache) || d.FileRenderCache[fileIdx] == nil {
		return false
	}
	rc := d.FileRenderCache[fileIdx]
	if len(rc.ThreadRanges) == 0 {
		return false
	}

	threadIdx := -1
	for i, tr := range rc.ThreadRanges {
		if tr.Side == side && tr.Line == line {
			threadIdx = i
			break
		}
	}
	if threadIdx < 0 {
		return false
	}

	components.RemoveThread(rc, threadIdx)

	d.RenderedFiles[fileIdx] = rc.Content
	if fileIdx < len(d.FileDiffOffsets) {
		d.FileDiffOffsets[fileIdx] = rc.DiffLineOffsets
	}
	return true
}

// InsertThread inserts a new comment thread into the cached render.
// Returns true if the thread was successfully inserted.
func (d *DiffViewer) InsertThread(fileIdx int, diffLineIdx int, side string, line int, comments []github.ReviewComment) bool {
	if fileIdx < 0 || fileIdx >= len(d.FileRenderCache) || d.FileRenderCache[fileIdx] == nil {
		return false
	}
	rc := d.FileRenderCache[fileIdx]

	lt := components.LineAdd
	if diffLineIdx < len(d.FileDiffs[fileIdx]) {
		lt = d.FileDiffs[fileIdx][diffLineIdx].Type
	}

	gutterW := components.TotalGutterWidth(components.GutterColWidth(d.FileDiffs[fileIdx]))
	content := components.RenderSingleThread(comments, d.ContentWidth(), lt, d.Ctx.DiffColors, false, 0, d.RenderBody, gutterW)

	idx := components.InsertThread(rc, diffLineIdx, side, line, content)
	if idx < 0 {
		return false
	}

	d.RenderedFiles[fileIdx] = rc.Content
	if fileIdx < len(d.FileDiffOffsets) {
		d.FileDiffOffsets[fileIdx] = rc.DiffLineOffsets
	}
	return true
}

// AppendCopilotPending appends all pending Copilot "Thinking..." comments
// for the given file to the comment slice.
func (d DiffViewer) AppendCopilotPending(filename string, fileComments []github.ReviewComment) []github.ReviewComment {
	dots := strings.Repeat(".", d.CopilotDots+1)
	for commentID, info := range d.CopilotPending {
		if info.Path != filename {
			continue
		}
		body := d.CopilotReplyBuf[commentID]
		if body == "" {
			body = "Thinking" + dots
		} else {
			body = body + dots
		}
		line := info.Line
		replyToInt := comments.IDToInt(commentID)
		pending := github.ReviewComment{
			ID:           0,
			Body:         body,
			Path:         filename,
			Line:         &line,
			OriginalLine: &line,
			Side:         info.Side,
			InReplyToID:  &replyToInt,
			User:         github.User{Login: "copilot"},
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		fileComments = append(fileComments, pending)
	}
	return fileComments
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
	d.FileCommentPositions = make([][]components.CommentPosition, n)
	d.FileRenderCache = make([]*components.DiffRenderResult, n)
}

// --- Copilot helpers ---

// SetCopilotPending marks a comment as awaiting Copilot reply.
func (d *DiffViewer) SetCopilotPending(commentID, path string, line int, side string) {
	if d.CopilotPending == nil {
		d.CopilotPending = make(map[string]CopilotPendingInfo)
	}
	d.CopilotPending[commentID] = CopilotPendingInfo{Path: path, Line: line, Side: side}
}

// ClearCopilotPending removes a single pending Copilot session.
func (d *DiffViewer) ClearCopilotPending(commentID string) {
	delete(d.CopilotPending, commentID)
}

// HasCopilotPending returns true if there are any pending Copilot sessions.
func (d DiffViewer) HasCopilotPending() bool {
	return len(d.CopilotPending) > 0
}

// IsCopilotPending returns true if the given comment is pending.
func (d DiffViewer) IsCopilotPending(commentID string) bool {
	_, ok := d.CopilotPending[commentID]
	return ok
}

// CopilotPendingPath returns the path of a pending Copilot session, or "".
func (d DiffViewer) CopilotPendingPath(commentID string) string {
	if info, ok := d.CopilotPending[commentID]; ok {
		return info.Path
	}
	return ""
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
				d.Tree.MoveCursorBy(-2 - len(d.Tree.Entries))
			} else {
				d.VP.GotoTop()
				if d.CurrentFileIdx >= 0 && d.HasDiffLines() {
					d.SyncDiffCursorToViewport()
				}
			}
			return KeyHandled
		}
		d.WaitingG = true
		return KeyHandled
	}
	return KeyNotHandled
}
