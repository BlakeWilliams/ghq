package diffviewer

import (
	"fmt"
	"image/color"
	"path"
	"regexp"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blakewilliams/gg/internal/github"
	"github.com/blakewilliams/gg/internal/review/agents"
	"github.com/blakewilliams/gg/internal/review/comments"
	"github.com/blakewilliams/gg/internal/ui/components"
	"github.com/blakewilliams/gg/internal/ui/uictx"
)

const scrollMargin = 5

// LayoutInfo carries optional metadata for the header/footer chrome.
// Callers that don't set fields get sensible defaults (no mode, no branch, etc).
type LayoutInfo struct {
	ModeName    string      // e.g. "Unstaged", "Staged", "Branch"
	ModeColor   color.Color // derived from mode; nil = no mode shown
	BranchName  string      // shown in footer under file tree
	PR          *github.PullRequest
	// ModeShortcut, when non-empty and HelpMode is on, is rendered in gray
	// next to the mode label (e.g. "BRANCH m") to hint at the cycle key.
	ModeShortcut string
	// HelpLine is the contextual help text shown at the bottom of the right
	// panel when HelpMode is on. Empty disables the row.
	HelpLine string
	HelpMode bool

	// ScrollOverride, when set, replaces the diff viewport scrollbar with
	// custom values (e.g. panel scroll in fullscreen mode).
	ScrollOverride *ScrollOverride
}

// ScrollOverride provides custom total/offset for the scrollbar indicator.
type ScrollOverride struct {
	Total  int // total number of content lines
	Offset int // current scroll offset
}

// helpFooterRows returns the number of rows the help footer steals from
// the right-pane viewport.
func (i LayoutInfo) helpFooterRows() int {
	if i.HelpMode && i.HelpLine != "" {
		return 2 // separator + help line
	}
	return 0
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
	Composing           bool
	CommentInput        textarea.Model
	CommentFile         string
	CommentLine         int
	CommentSide         string
	CommentStartLine    int
	CommentStartSide    string
	ReplyMode           components.ReplyMode

	// Copilot state
	Agent        *agents.Client
	CopilotState CopilotState

	// Comment source — set by outer model. Returns base comments for a file
	// (without copilot pending, which DiffViewer appends itself).
	Comments CommentSource

	// Render body callback for markdown in comment threads.
	RenderBody func(body string, width int, bg string) string

	// Render lists per file (structural render items).
	FileRenderers []*components.FileRenderList

	// FileBadgeData holds per-position badge data for each file.
	// Shared between the file tree (aggregated) and diff view (per-position).
	FileBadgeData []map[components.CommentKey]components.BadgeInfo

	// Loading spinner (shown while highlighting is in progress for current file).
	Spinner       spinner.Model
	SpinnerActive bool

	// Help mode chrome (right-pane footer with contextual key hints).
	// When HelpMode is true and HelpLine is non-empty, the bottom row of the
	// right pane is reserved for the help line and the viewport shrinks by 1.
	HelpMode bool
	HelpLine string

	// Internal
	WaitingG    bool // true after first "g" keypress, waiting for second "g" to trigger gg (go to top)
	LastContent string

	// SnapThreadComment is set by snap functions (SyncDiffCursorToViewport,
	// ScrollAndSyncCursor, DiffCursorFromScreenY) when the target position
	// falls inside a comment thread. 1-based comment index, or 0 if not in a thread.
	// Callers read this to decide whether to enter/update thread highlighting.
	SnapThreadComment int

	// Search
	Searching      bool           // true while the search popup is open
	SearchQuery    string         // current typed query (while popup is open)
	SearchPattern  *regexp.Regexp // compiled regex from last confirmed search
	SearchMatches  []int          // diffLine indices in current file that match
	SearchMatchIdx int            // current position in SearchMatches (-1 = none)

	// Hunk expansion state — persists across reloads.
	// Key: filename → original HunkInfo → total lines revealed.
	ExpandedHunks map[string]map[components.HunkInfo]int

	// BadgesOnly suppresses inline CommentThreadItem rendering in favor of
	// badge pills on diff lines. Set by callers that use the new comment UI.
	BadgesOnly bool

	// Comment panel state. Panel identity is stored so the panel can be
	// rebuilt when comment data changes (e.g. FormatFile).
	PanelOpen    bool
	PanelFocused bool   // true when the comment panel column has keyboard focus
	PanelFile    string // filename of the thread shown in the panel
	PanelSide      string // "LEFT" or "RIGHT"
	PanelLine      int    // source line number (end line)
	PanelStartLine int    // start line for multi-line selections (0 = single line)
	PanelScroll    int    // scroll offset into panel content lines
	Panel        *components.CommentPanel
}

// CommentSource provides comments for a file. Each view implements this
// to return comments from its backing store (local CommentStore, GitHub API, etc).
type CommentSource interface {
	CommentsForFile(filename string) []github.ReviewComment
}

// ReconcileAndSync runs state reconciliation on the current file's render list,
// dirtying only the items whose highlight state changed (cursor, selection,
// search). If any items were dirtied, it re-syncs the rendered content to the
// viewport. This is the SOLE sync point — callers never need to manually bump
// or sync.
func (d *DiffViewer) ReconcileAndSync() {
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileRenderers) || d.FileRenderers[idx] == nil {
		return
	}
	if idx >= len(d.FileDiffs) {
		return
	}
	list := d.FileRenderers[idx]
	rc := d.renderContext(d.FileDiffs[idx])
	dirtied := list.ReconcileHighlights(rc)
	if dirtied || list.IsDirty() {
		d.syncFromRenderList(idx, rc)
	}
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

// maxCommentPanelWidth caps the panel so it doesn't dominate the screen.
const maxCommentPanelWidth = 100

// commentPanelMinWidth returns the configured minimum or the default.
func (d DiffViewer) commentPanelMinWidth() int {
	if d.Ctx.Config.CommentPanelMinWidth > 0 {
		return d.Ctx.Config.CommentPanelMinWidth
	}
	return 55
}

// diffMinWidth returns the configured minimum or the default.
func (d DiffViewer) diffMinWidth() int {
	if d.Ctx.Config.DiffMinWidth > 0 {
		return d.Ctx.Config.DiffMinWidth
	}
	return 90
}

func (d DiffViewer) RightPanelWidth() int {
	return d.Width - d.Tree.Width
}

func (d DiffViewer) RightPanelInnerWidth() int {
	return d.RightPanelWidth() - 1 // separator column
}

func (d DiffViewer) ContentWidth() int {
	return d.RightPanelInnerWidth() - 1 // -1 for scrollbar column
}

// CommentPanelWidth returns the width available for the comment panel.
// Returns 0 if the terminal is too narrow for a side panel (fallback needed).
func (d DiffViewer) CommentPanelWidth() int {
	if !d.PanelOpen {
		return 0
	}
	minPanel := d.commentPanelMinWidth()
	minDiff := d.diffMinWidth()
	// Layout: [diff (diffW)] [scroll (1)] [sep (1)] [panel (panelW)]
	// Total = innerRightW = diffW + 1 + 1 + panelW
	available := d.RightPanelInnerWidth() - minDiff - 2
	if available < minPanel {
		return 0 // too narrow, use fallback
	}
	maxPanel := maxCommentPanelWidth
	if maxPanel < minPanel {
		maxPanel = minPanel
	}
	if available > maxPanel {
		return maxPanel
	}
	return available
}

// DiffContentWidth returns the width of the diff content area when the panel
// is open. When the panel is closed, returns ContentWidth().
func (d DiffViewer) DiffContentWidth() int {
	panelW := d.CommentPanelWidth()
	if panelW == 0 {
		return d.ContentWidth()
	}
	// diff = innerRightW - panelW - 2 (scroll + sep)
	return d.RightPanelInnerWidth() - panelW - 2
}

// NeedsFallbackView returns true when the panel is open but the terminal
// is too narrow for a side panel.
func (d DiffViewer) NeedsFallbackView() bool {
	return d.PanelOpen && d.CommentPanelWidth() == 0
}

// PanelRenderWidth returns the width available for the panel content.
// In side-panel mode this is CommentPanelWidth(); in fullscreen it's ContentWidth().
func (d DiffViewer) PanelRenderWidth() int {
	if pw := d.CommentPanelWidth(); pw > 0 {
		return pw
	}
	return d.ContentWidth()
}

// IsDiffFocused returns true when the diff content pane has keyboard focus
// (i.e. neither tree nor comment panel is focused).
func (d DiffViewer) IsDiffFocused() bool {
	return !d.Tree.Focused && !d.PanelFocused
}

// NormalizeFocus ensures PanelFocused is only set when the panel is actually
// visible. In side-panel mode, clears focus if the terminal shrinks below the
// minimum. In fullscreen mode, the panel is always visible so focus stays.
func (d *DiffViewer) NormalizeFocus() {
	if !d.PanelOpen {
		d.PanelFocused = false
		return
	}
	// Fullscreen mode: panel replaces the diff view, always visible.
	if d.NeedsFallbackView() {
		return
	}
	// Side panel mode: clear focus if panel column doesn't fit.
	if d.PanelFocused && d.CommentPanelWidth() == 0 {
		d.PanelFocused = false
	}
}

func (d DiffViewer) ViewportHeight() int {
	return d.Height - 2 - d.helpRowCount() // header + separator (- help row)
}

// helpRowCount returns the number of bottom rows reserved for the help line
// in the right pane.
func (d DiffViewer) helpRowCount() int {
	if d.HelpMode && d.HelpLine != "" {
		return 2 // separator + help line
	}
	return 0
}

func (d DiffViewer) BorderStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(d.Ctx.DiffColors.BorderColor)
}

// OpenPanelForThread opens the comment panel for a specific thread.
func (d *DiffViewer) OpenPanelForThread(file, side string, line int) {
	d.PanelOpen = true
	d.PanelFile = file
	d.PanelSide = side
	d.PanelLine = line
	d.PanelScroll = 0
	d.RefreshPanel()
	d.ScrollPanel(999999)
	d.PanelFocused = true
}

// OpenPanelForNewComment opens the comment panel in compose mode for a line
// that doesn't have an existing thread yet.
func (d *DiffViewer) OpenPanelForNewComment(file, side string, line, startLine int) {
	d.PanelOpen = true
	d.PanelFile = file
	d.PanelSide = side
	d.PanelLine = line
	d.PanelStartLine = startLine
	d.PanelScroll = 0
	d.PanelFocused = true
	d.Panel = &components.CommentPanel{
		FilePath:   file,
		Side:       side,
		Line:       line,
		DiffContext: d.panelDiffContext(),
		Width:      d.PanelRenderWidth(),
		RenderBody: d.RenderBody,
		Colors:     d.Ctx.DiffColors,
	}
}



// ClosePanel closes the comment panel.
func (d *DiffViewer) ClosePanel() {
	d.PanelOpen = false
	d.PanelFocused = false
	d.PanelFile = ""
	d.PanelSide = ""
	d.PanelLine = 0
	d.PanelStartLine = 0
	d.PanelScroll = 0
	d.Panel = nil
}

// PanelViewHeight returns the number of visible rows available for panel
// content. This is the content area height (total height minus chrome rows
// and help rows).
func (d DiffViewer) PanelViewHeight() int {
	h := d.Height - 2 - d.helpRowCount() // header + separator + help
	if h < 1 {
		h = 1
	}
	return h
}

// ScrollPanel adjusts the panel scroll offset by delta lines, clamping to
// valid bounds.
func (d *DiffViewer) ScrollPanel(delta int) {
	if d.Panel == nil {
		return
	}
	totalLines := d.Panel.ContentLines()
	viewH := d.PanelViewHeight()
	maxScroll := totalLines - viewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	d.PanelScroll += delta
	if d.PanelScroll < 0 {
		d.PanelScroll = 0
	}
	if d.PanelScroll > maxScroll {
		d.PanelScroll = maxScroll
	}
}

// SyncPanelReplyView updates the panel's ReplyView from the textarea
// when composing, and adjusts textarea width to match the current panel width.
func (d *DiffViewer) SyncPanelReplyView() {
	if d.Panel == nil {
		return
	}
	if d.Composing && d.PanelOpen {
		w := d.PanelRenderWidth() - 4 // padding
		if w < 10 {
			w = 10
		}
		d.CommentInput.SetWidth(w)
		d.Panel.ReplyView = d.CommentInput.View()
		d.Panel.ReplyMode = d.ReplyMode
		d.Panel.HelpMode = d.Ctx.Config.HelpMode
	} else {
		d.Panel.ReplyView = ""
	}
}

// PanelAtBottom returns true when the panel scroll is at or near the bottom.
func (d *DiffViewer) PanelAtBottom() bool {
	if d.Panel == nil {
		return true
	}
	totalLines := d.Panel.ContentLines()
	viewH := d.PanelViewHeight()
	maxScroll := totalLines - viewH
	if maxScroll <= 0 {
		return true
	}
	return d.PanelScroll >= maxScroll-1
}

// RefreshPanel rebuilds the panel content from current comment data.
// Called after comment data changes (FormatFile, new comments, etc).
func (d *DiffViewer) RefreshPanel() {
	if !d.PanelOpen || d.PanelFile == "" {
		return
	}
	// Remember if we were pinned to the bottom before rebuilding.
	wasAtBottom := d.PanelAtBottom()
	fileComments := d.CommentsForFile(d.PanelFile)
	threadComments := components.CommentsForThread(fileComments, d.PanelSide, d.PanelLine)
	if len(threadComments) == 0 {
		if !d.Composing {
			d.ClosePanel()
			return
		}
		// Composing a new comment — rebuild panel with just diff context.
		d.Panel = &components.CommentPanel{
			FilePath:   d.PanelFile,
			Side:       d.PanelSide,
			Line:       d.PanelLine,
			DiffContext: d.panelDiffContext(),
			Width:      d.PanelRenderWidth(),
			RenderBody: d.RenderBody,
			Colors:     d.Ctx.DiffColors,
		}
		if wasAtBottom {
			d.ScrollPanel(999999)
		}
		return
	}

	// Extract multi-line start from the root comment.
	root := threadComments[0]
	d.PanelStartLine = 0
	if d.PanelSide == "LEFT" {
		if root.OriginalStartLine != nil && *root.OriginalStartLine > 0 {
			d.PanelStartLine = *root.OriginalStartLine
		}
	} else {
		if root.StartLine != nil && *root.StartLine > 0 {
			d.PanelStartLine = *root.StartLine
		}
	}

	var renderComments []components.RenderComment
	blockLookup := d.blocksForFile(d.PanelFile)
	if blockLookup != nil {
		threaded := components.BuildThreadedRenderComments(fileComments, blockLookup)
		pk := components.CommentKey{Side: d.PanelSide, Line: d.PanelLine}
		renderComments = threaded[pk]
	}
	if len(renderComments) == 0 {
		renderComments = components.ReviewCommentsToRender(threadComments)
	}
	// Merge pending copilot comments.
	if pending := d.CopilotState.PendingRenderComments(d.PanelFile); len(pending) > 0 {
		pk := components.CommentKey{Side: d.PanelSide, Line: d.PanelLine}
		if p := pending[pk]; len(p) > 0 {
			renderComments = append(renderComments, p...)
		}
	}

	d.Panel = &components.CommentPanel{
		Comments:    renderComments,
		FilePath:    d.PanelFile,
		Side:        d.PanelSide,
		Line:        d.PanelLine,
		Resolved:    components.ThreadIsResolved(threadComments),
		DiffContext:  d.panelDiffContext(),
		Width:       d.PanelRenderWidth(),
		RenderBody:  d.RenderBody,
		Colors:      d.Ctx.DiffColors,
	}

	// If we were at the bottom, stay at the bottom after new content arrives.
	if wasAtBottom {
		d.ScrollPanel(999999)
	}
}

// panelDiffContext extracts rendered diff lines for the panel's comment range.
// For multi-line selections (PanelStartLine > 0), it renders from start to end line.
// For single-line comments, it shows up to 2 context lines before the target.
func (d *DiffViewer) panelDiffContext() []string {
	fileIdx := d.FileIndexForPath(d.PanelFile)
	if fileIdx < 0 || fileIdx >= len(d.FileRenderers) || d.FileRenderers[fileIdx] == nil {
		return nil
	}
	if fileIdx >= len(d.FileDiffs) {
		return nil
	}

	list := d.FileRenderers[fileIdx]
	rc := d.renderContext(d.FileDiffs[fileIdx])

	// Find the diff line index that matches PanelSide + PanelLine (end line).
	targetIdx := -1
	startIdx := -1
	for _, item := range list.Items {
		dli, ok := item.(*components.DiffLineItem)
		if !ok || dli.DiffLine == nil {
			continue
		}
		dl := dli.DiffLine
		lineNo := dl.NewLineNo
		if d.PanelSide == "LEFT" {
			lineNo = dl.OldLineNo
		}
		if d.PanelStartLine > 0 && lineNo == d.PanelStartLine && startIdx < 0 {
			startIdx = dli.DiffIdx()
		}
		if lineNo == d.PanelLine {
			targetIdx = dli.DiffIdx()
			break
		}
	}
	if targetIdx < 0 {
		return nil
	}

	// For multi-line selections, render exactly the selected range.
	// For single-line comments, show up to 2 context lines before.
	if startIdx >= 0 && startIdx < targetIdx {
		// Use the multi-line start directly.
	} else {
		const contextBefore = 2
		startIdx = targetIdx - contextBefore
	}
	if startIdx < 0 {
		startIdx = 0
	}

	var lines []string
	for i := startIdx; i <= targetIdx; i++ {
		dli := list.DiffLineItemAt(i)
		if dli == nil {
			continue
		}
		rendered := dli.Render(rc)
		rendered = strings.TrimRight(rendered, "\n")
		lines = append(lines, rendered)
	}
	return lines
}

// NextBadgeDiffIdx returns the diff line index of the next line with a badge
// after from. Wraps around. Returns -1 if no badges exist.
func (d DiffViewer) NextBadgeDiffIdx(from int) int {
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileRenderers) || d.FileRenderers[idx] == nil {
		return -1
	}
	list := d.FileRenderers[idx]
	first := -1
	for _, item := range list.Items {
		dli, ok := item.(*components.DiffLineItem)
		if !ok || dli.Badge == nil {
			continue
		}
		di := dli.DiffIdx()
		if first < 0 {
			first = di
		}
		if di > from {
			return di
		}
	}
	return first // wrap around
}

// PrevBadgeDiffIdx returns the diff line index of the previous line with a
// badge before from. Wraps around. Returns -1 if no badges exist.
func (d DiffViewer) PrevBadgeDiffIdx(from int) int {
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileRenderers) || d.FileRenderers[idx] == nil {
		return -1
	}
	list := d.FileRenderers[idx]
	last := -1
	candidate := -1
	for _, item := range list.Items {
		dli, ok := item.(*components.DiffLineItem)
		if !ok || dli.Badge == nil {
			continue
		}
		di := dli.DiffIdx()
		last = di
		if di < from {
			candidate = di
		}
	}
	if candidate >= 0 {
		return candidate
	}
	return last // wrap around
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

// CursorOnHunk returns true if the diff cursor is on a hunk header line.
func (d DiffViewer) CursorOnHunk() bool {
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileDiffs) || d.DiffCursor < 0 || d.DiffCursor >= len(d.FileDiffs[idx]) {
		return false
	}
	return d.FileDiffs[idx][d.DiffCursor].Type == components.LineHunk
}

// ClearThreadHighlight un-highlights the thread at the current cursor and
// resets ThreadCursor to 0. Safe to call when ThreadCursor is already 0.
func (d *DiffViewer) ClearThreadHighlight() {
	if d.ThreadCursor == 0 {
		return
	}
	idx := d.CurrentFileIdx
	if idx >= 0 && idx < len(d.FileDiffs) && d.DiffCursor < len(d.FileDiffs[idx]) {
		dl := d.FileDiffs[idx][d.DiffCursor]
		var side string
		var line int
		if dl.Type == components.LineDel {
			side, line = "LEFT", dl.OldLineNo
		} else {
			side, line = "RIGHT", dl.NewLineNo
		}
		d.SpliceThreadWithHighlight(idx, side, line, false, 0)
	}
	d.ThreadCursor = 0
}

// MoveDiffCursor moves the cursor by delta.
func (d *DiffViewer) MoveDiffCursor(delta int) {
	if d.CurrentFileIdx < 0 || d.CurrentFileIdx >= len(d.FileDiffs) {
		return
	}
	d.ClearThreadHighlight()
	lines := d.FileDiffs[d.CurrentFileIdx]
	newPos := d.DiffCursor + delta

	if newPos < 0 || newPos >= len(lines) {
		return
	}
	d.DiffCursor = newPos
	d.ScrollToDiffCursor()
}

// MoveDiffCursorBy jumps the cursor by delta lines, clamped.
func (d *DiffViewer) MoveDiffCursorBy(delta int) {
	if d.CurrentFileIdx < 0 || d.CurrentFileIdx >= len(d.FileDiffs) {
		return
	}
	d.ClearThreadHighlight()
	lines := d.FileDiffs[d.CurrentFileIdx]
	newPos := d.DiffCursor + delta

	if newPos < 0 {
		newPos = 0
	}
	if newPos >= len(lines) {
		newPos = len(lines) - 1
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
// Sets SnapThreadComment if the target position falls inside a comment thread.
func (d *DiffViewer) ScrollAndSyncCursor(delta int) {
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileDiffOffsets) {
		return
	}

	d.VP.SetYOffset(d.VP.YOffset() + delta)

	center := d.VP.YOffset() + d.ViewportHeight()/2
	d.DiffCursor, d.SnapThreadComment = d.snapTarget(idx, center)
}

// SyncDiffCursorToViewport moves the diff cursor to the line closest to
// the center of the viewport. Used after viewport-only scrolling.
// Sets SnapThreadComment if the center falls inside a comment thread.
func (d *DiffViewer) SyncDiffCursorToViewport() {
	idx := d.CurrentFileIdx
	if idx < 0 || idx >= len(d.FileDiffOffsets) || len(d.FileDiffOffsets[idx]) == 0 {
		return
	}
	center := d.VP.YOffset() + d.ViewportHeight()/2
	d.DiffCursor, d.SnapThreadComment = d.snapTarget(idx, center)
}

// DiffCursorFromScreenY maps a screen Y coordinate to the nearest diff line
// index, accounting for chrome rows and viewport scroll. Returns -1 if no
// valid diff line is found. Sets SnapThreadComment if the position falls
// inside a comment thread.
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

	diffIdx, tc := d.snapTarget(idx, absLine)
	d.SnapThreadComment = tc
	return diffIdx
}

// --- Cursor overlay rendering ---

// snapTarget resolves a target absolute line to the best diff cursor position.
// If the target falls inside a comment thread, returns the thread's parent
// diff line index and the 1-based comment index. Otherwise returns the
// closest non-hunk diff line and 0 for threadComment.
func (d *DiffViewer) snapTarget(fileIdx int, targetAbs int) (diffIdx int, threadComment int) {
	if fileIdx < 0 || fileIdx >= len(d.FileDiffOffsets) || len(d.FileDiffOffsets[fileIdx]) == 0 {
		return -1, 0
	}

	// Check if target falls inside a comment thread.
	if fileIdx < len(d.FileRenderers) && d.FileRenderers[fileIdx] != nil {
		rc := d.RenderContextForFile(fileIdx)
		if item, offset := d.FileRenderers[fileIdx].ItemAtLine(targetAbs, rc); item != nil {
			if ct, ok := item.(*components.CommentThreadItem); ok {
				ci := ct.CommentIndexAtOffset(offset, rc)
				if ci > 0 {
					return ct.DiffIdx(), ci
				}
			}
		}
	}

	// Fallback: closest diff line by distance.
	offsets := d.FileDiffOffsets[fileIdx]
	diffs := d.FileDiffs[fileIdx]
	best := -1
	bestDist := 0
	for i := 0; i < len(offsets); i++ {
		if i >= len(diffs) {
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
	return best, 0
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

	// Determine which pane is focused for header styling.
	bright := bold.Foreground(lipgloss.BrightWhite)
	treeTitleStyle := dim
	diffTitleStyle := dim
	panelTitleStyle := dim
	if d.Tree.Focused {
		treeTitleStyle = bright
	} else if d.PanelFocused {
		panelTitleStyle = bright
	} else {
		diffTitleStyle = bright
	}

	sep := chrome.Render("│")
	rightW := d.RightPanelWidth()

	// Pre-compute side panel geometry so header can use it.
	innerRightW := rightW - 1
	panelW := d.CommentPanelWidth()
	hasSidePanel := panelW > 0
	diffHeaderW := innerRightW // width available for the diff header
	if hasSidePanel {
		// diffContent + scroll(1) + sep(1) + panel
		diffHeaderW = d.DiffContentWidth() + 1 // +1 for scroll column
	}

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
		if info.HelpMode && info.ModeShortcut != "" {
			modeLabel += " " + dim.Render(info.ModeShortcut)
		}
	}

	treeLabelW := lipgloss.Width(treeLabel)
	modeLabelW := lipgloss.Width(modeLabel)
	treeHeaderPad := treeW - 1 - treeLabelW - modeLabelW - 1 // -1 leading space, -1 for sep
	if treeHeaderPad < 0 {
		treeHeaderPad = 0
	}
	treeHeader := " " + treeLabel + strings.Repeat(" ", treeHeaderPad) + modeLabel

	// === Header: right (diff) panel ===
	// " dir/filename ... +N -M "
	var rightLabel string
	var rightTrailer string // right-aligned: stats + PR + scroll
	if rightTitle == "Overview" {
		rightLabel = diffTitleStyle.Render("Overview")
	} else {
		dir, file := path.Split(rightTitle)
		if dir != "" {
			rightLabel = dim.Render(dir) + diffTitleStyle.Render(file)
		} else {
			rightLabel = diffTitleStyle.Render(rightTitle)
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
	diffHdrGap := diffHeaderW - 2 - rightLabelW - rightTrailerW // -2 for leading/trailing space
	if diffHdrGap < 0 {
		diffHdrGap = 0
	}
	diffHeader := " " + rightLabel + strings.Repeat(" ", diffHdrGap) + rightTrailer + " "

	// === Header: comment panel (when visible) ===
	var headerLine string
	if hasSidePanel {
		commentCount := 0
		if d.Panel != nil {
			commentCount = len(d.Panel.Comments)
		}
		var countText string
		if commentCount == 1 {
			countText = "1 Comment"
		} else {
			countText = fmt.Sprintf("%d Comments", commentCount)
		}
		panelLabel := panelTitleStyle.Render(countText)
		panelLabelW := lipgloss.Width(panelLabel)
		panelHdrPad := panelW - 1 - panelLabelW // -1 for leading space
		if panelHdrPad < 0 {
			panelHdrPad = 0
		}
		panelHeader := " " + panelLabel + strings.Repeat(" ", panelHdrPad)
		headerLine = treeHeader + sep + diffHeader + sep + panelHeader
	} else {
		headerLine = treeHeader + sep + diffHeader
	}

	// Separator row: thin horizontal rule.
	treeFill := treeW - 1
	if treeFill < 0 {
		treeFill = 0
	}
	var separatorLine string
	if hasSidePanel {
		diffFill := diffHeaderW
		if diffFill < 0 {
			diffFill = 0
		}
		panelFill := panelW
		if panelFill < 0 {
			panelFill = 0
		}
		separatorLine = chrome.Render(strings.Repeat("─", treeFill) + "┼" + strings.Repeat("─", diffFill) + "┼" + strings.Repeat("─", panelFill))
	} else {
		rightFill := rightW - 1
		if rightFill < 0 {
			rightFill = 0
		}
		separatorLine = chrome.Render(strings.Repeat("─", treeFill) + "┼" + strings.Repeat("─", rightFill))
	}

	// Content area.
	contentH := d.Height - chromeRows

	// Copilot session status: 2 extra footer rows when sessions are running.
	copilotCount := len(d.CopilotState.Pending)
	copilotFooterRows := 0
	if copilotCount > 0 {
		copilotFooterRows = 2 // separator + status line
	}

	// Help footer: 1 row reserved at the bottom of the right panel for
	// contextual key hints when help mode is enabled.
	helpRows := info.helpFooterRows()

	tree := d.Tree
	tree.Width = treeW - 1
	tree.Height = contentH - 2 - copilotFooterRows // tree loses rows for footer(s)
	tree.CurrentFileIdx = d.CurrentFileIdx
	tree.AnimFrame = d.CopilotState.Dots
	treeContentLines := tree.View()
	rightLines := strings.Split(rightView, "\n")

	// Scrollbar: compute thumb position and size. The viewport content
	// lives in (contentH - helpRows) rows on the right side.
	totalLines := d.VP.TotalLineCount()
	scrollOffset := d.VP.YOffset()
	if info.ScrollOverride != nil {
		totalLines = info.ScrollOverride.Total
		scrollOffset = info.ScrollOverride.Offset
	}
	vpH := contentH - helpRows
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
		if scrollOffset > scrollable {
			scrollOffset = scrollable
		}
		thumbStart = scrollOffset * (vpH - thumbLen) / scrollable
	}
	// Scrollbar styles: track matches border color, thumb slightly lighter.
	trackChar := chrome.Render("│")
	thumbColor := uictx.BrightnessModify(chromeClr, 40)
	thumbChar := lipgloss.NewStyle().Foreground(thumbColor).Render("┃")

	// Content width for the diff area (used for help line, etc.)
	contentW := innerRightW - 1 // -1 for scrollbar column

	// Pre-render panel lines if the side panel fits.
	diffW := contentW
	var panelLines []string
	if hasSidePanel {
		diffW = d.DiffContentWidth()
		d.Panel.Width = panelW
		d.Panel.ChromeColor = chromeClr
		d.SyncPanelReplyView()
		panelContent := d.Panel.View()
		panelLines = strings.Split(strings.TrimRight(panelContent, "\n"), "\n")
		// Apply panel scroll offset.
		if d.PanelScroll > 0 && d.PanelScroll < len(panelLines) {
			panelLines = panelLines[d.PanelScroll:]
		} else if d.PanelScroll >= len(panelLines) {
			panelLines = nil
		}
	}

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

		// Use narrower diff width when side panel is active.
		rowW := contentW
		if hasSidePanel {
			rowW = diffW
		}

		var scrollCol string
		if helpRows > 0 && i == contentH-helpRows {
			// Separator row above the help line.
			rl = chrome.Render(strings.Repeat("─", rowW))
			scrollCol = " "
		} else if helpRows > 0 && i > contentH-helpRows {
			// Right-pane help footer row.
			rl = renderHelpLine(info.HelpLine, rowW, dim)
			scrollCol = " "
		} else {
			if i < len(rightLines) {
				rl = rightLines[i]
			}
			rlW := lipgloss.Width(rl)
			if rlW > rowW {
				rl = ansi.Truncate(rl, rowW, "")
				rlW = lipgloss.Width(rl)
			}
			if rlW < rowW {
				rl += strings.Repeat(" ", rowW-rlW)
			}
			if thumbStart < 0 {
				scrollCol = " "
			} else if i >= thumbStart && i < thumbStart+thumbLen {
				scrollCol = thumbChar
			} else {
				scrollCol = trackChar
			}
		}

		// Compose: tree + sep + diff + scroll [+ panelSep + panel]
		if hasSidePanel {
			panelLine := ""
			if i < len(panelLines) {
				panelLine = panelLines[i]
			}
			plW := lipgloss.Width(panelLine)
			if plW > panelW {
				panelLine = ansi.Truncate(panelLine, panelW, "")
				plW = lipgloss.Width(panelLine)
			}
			if plW < panelW {
				panelLine += strings.Repeat(" ", panelW-plW)
			}
			b.WriteString(leftPart + sep + rl + scrollCol + sep + panelLine)
		} else {
			b.WriteString(leftPart + sep + rl + scrollCol)
		}
		if i < contentH-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}

// renderHelpLine pads the help line out to width w. It is rendered with the
// dim style; embedded ANSI is preserved as-is.
func renderHelpLine(s string, w int, dim lipgloss.Style) string {
	// Reserve a leading space so the text doesn't sit flush against the
	// vertical separator.
	prefix := " "
	body := s
	usable := w - lipgloss.Width(prefix)
	if usable < 0 {
		usable = 0
	}
	if lipgloss.Width(body) > usable {
		// Truncate with no ellipsis to keep things minimal.
		body = lipgloss.NewStyle().MaxWidth(usable).Render(body)
	}
	pad := usable - lipgloss.Width(body)
	if pad < 0 {
		pad = 0
	}
	return prefix + body + strings.Repeat(" ", pad)
}

// --- File formatting ---

// renderContext builds a RenderContext for the current viewer state using
// the given diff lines (needed for gutter column width).
func (d *DiffViewer) renderContext(diffLines []components.DiffLine) components.RenderContext {
	colW := components.GutterColWidth(diffLines)

	// Build search matches map for this file's diff lines.
	var searchMatches map[int]int
	if d.SearchPattern != nil && len(d.SearchMatches) > 0 {
		searchMatches = make(map[int]int, len(d.SearchMatches))
		for i, idx := range d.SearchMatches {
			searchMatches[idx] = i + 1 // 1-based match number
		}
	}

	cursorLine := d.DiffCursor
	selStart := -1
	selEnd := -1
	if d.SelectionAnchor >= 0 {
		selStart = d.SelectionAnchor
		selEnd = d.DiffCursor
		if selStart > selEnd {
			selStart, selEnd = selEnd, selStart
		}
	}

	return components.RenderContext{
		Width:          d.DiffContentWidth(),
		Colors:         d.Ctx.DiffColors,
		ColW:           colW,
		RenderBody:     d.RenderBody,
		AnimFrame:      d.CopilotState.Dots,
		CursorLine:     cursorLine,
		SelectionStart: selStart,
		SelectionEnd:   selEnd,
		SearchPattern:  d.SearchPattern,
		SearchMatches:  searchMatches,
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
	list := d.FileRenderers[index]
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
	opts.BadgesOnly = d.BadgesOnly
	if d.RenderBody != nil {
		opts.RenderBody = d.RenderBody
	}
	if d.FileBadgeData != nil && index < len(d.FileBadgeData) {
		opts.BadgeData = d.FileBadgeData[index]
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
	if index < len(d.FileRenderers) {
		d.FileRenderers[index] = components.BuildRenderList(diffLines, fileComments, opts)
		rc := d.renderContext(diffLines)
		d.syncFromRenderList(index, rc)
	}

	// Refresh the comment panel if it's showing a thread from this file.
	if d.PanelOpen && d.PanelFile == filename {
		d.RefreshPanel()
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

	if index < len(d.FileRenderers) {
		d.FileRenderers[index] = components.BuildRenderList(diffLines, fileComments, opts)
		rc := d.renderContext(diffLines)
		d.syncFromRenderList(index, rc)
	}
}

// ReformatAllFiles invalidates all caches and re-renders the current file.
func (d *DiffViewer) ReformatAllFiles() {
	for i := range d.RenderedFiles {
		d.RenderedFiles[i] = ""
	}
	for _, list := range d.FileRenderers {
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
	if fileIdx < 0 || fileIdx >= len(d.FileRenderers) || d.FileRenderers[fileIdx] == nil {
		d.FormatFile(fileIdx)
		return
	}
	list := d.FileRenderers[fileIdx]
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
	if fileIdx < 0 || fileIdx >= len(d.FileRenderers) || d.FileRenderers[fileIdx] == nil {
		return false
	}
	list := d.FileRenderers[fileIdx]
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
	if fileIdx < 0 || fileIdx >= len(d.FileRenderers) || d.FileRenderers[fileIdx] == nil {
		return false
	}

	lt := components.LineAdd
	if diffLineIdx < len(d.FileDiffs[fileIdx]) {
		lt = d.FileDiffs[fileIdx][diffLineIdx].Type
	}

	item := components.NewCommentThreadItem(diffLineIdx, side, line, components.ReviewCommentsToRender(comments), lt)
	d.FileRenderers[fileIdx].InsertAfterDiffLine(diffLineIdx, item)

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
	if fileIdx < 0 || fileIdx >= len(d.FileRenderers) || d.FileRenderers[fileIdx] == nil {
		return -1
	}
	if fileIdx >= len(d.FileDiffs) {
		return -1
	}
	rc := d.renderContext(d.FileDiffs[fileIdx])
	return d.FileRenderers[fileIdx].ThreadEndOffset(side, line, rc)
}

// SetThreadOpenBottom sets or clears the OpenBottom flag on a thread, invalidates
// it, and re-syncs the render list so that RenderedFiles and offsets are updated.
func (d *DiffViewer) SetThreadOpenBottom(fileIdx int, side string, line int, open bool) {
	if fileIdx < 0 || fileIdx >= len(d.FileRenderers) || d.FileRenderers[fileIdx] == nil {
		return
	}
	list := d.FileRenderers[fileIdx]
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
	if fileIdx < 0 || fileIdx >= len(d.FileRenderers) || d.FileRenderers[fileIdx] == nil {
		return components.LineContext
	}
	list := d.FileRenderers[fileIdx]
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
	innerH := d.Height - 2 - d.helpRowCount()

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
	innerH := d.Height - 2 - d.helpRowCount()

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

// ExpandHunk expands context around the hunk at diffLineIdx in the given file.
// It reveals up to n hidden lines above the hunk from the full file content.
// Returns true if expansion happened (caller should re-format).
func (d *DiffViewer) ExpandHunk(fileIdx, diffLineIdx, n int) bool {
	if !d.expandHunkInner(fileIdx, diffLineIdx, n) {
		return false
	}
	d.FormatFile(fileIdx)
	return true
}

// expandHunkInner performs the expansion splice and tracks it, but does NOT
// call FormatFile. Used by ExpandHunk (single) and ReapplyExpansions (batch).
func (d *DiffViewer) expandHunkInner(fileIdx, diffLineIdx, n int) bool {
	if fileIdx < 0 || fileIdx >= len(d.HighlightedFiles) {
		return false
	}
	hl := &d.HighlightedFiles[fileIdx]
	if diffLineIdx < 0 || diffLineIdx >= len(hl.DiffLines) {
		return false
	}
	if hl.DiffLines[diffLineIdx].Type != components.LineHunk {
		return false
	}
	if len(hl.HlLines) == 0 && len(hl.HlLinesOld) == 0 {
		return false
	}

	// Parse the hunk header to get where this hunk starts in the file.
	hunkInfo, ok := components.ParseHunkHeader(hl.DiffLines[diffLineIdx].Content)
	if !ok {
		return false
	}

	// Resolve the original hunk info for tracking. If this hunk was already
	// partially expanded, the current header has shifted start lines. We find
	// the original by checking: origStart - totalRevealed == currentStart.
	filename := d.Files[fileIdx].Filename
	origInfo := hunkInfo
	isNewExpansion := true
	if recs, ok := d.ExpandedHunks[filename]; ok {
		for key, revealed := range recs {
			if key == hunkInfo {
				origInfo = key
				isNewExpansion = true
				break
			}
			if key.NewStart-revealed == hunkInfo.NewStart && key.OldStart-revealed == hunkInfo.OldStart {
				origInfo = key
				isNewExpansion = false
				break
			}
		}
	}

	// Find where the previous content ends by walking backward.
	// Only keep the first (highest) value found for each side since we're
	// iterating from highest to lowest line numbers.
	prevOldEnd := 0 // 0 means start of file
	prevNewEnd := 0
	for i := diffLineIdx - 1; i >= 0; i-- {
		dl := hl.DiffLines[i]
		switch dl.Type {
		case components.LineContext:
			if prevOldEnd == 0 {
				prevOldEnd = dl.OldLineNo
			}
			if prevNewEnd == 0 {
				prevNewEnd = dl.NewLineNo
			}
		case components.LineAdd:
			if prevNewEnd == 0 {
				prevNewEnd = dl.NewLineNo
			}
			if prevOldEnd == 0 {
				continue
			}
		case components.LineDel:
			if prevOldEnd == 0 {
				prevOldEnd = dl.OldLineNo
			}
			if prevNewEnd == 0 {
				continue
			}
		default:
			continue
		}
		break
	}

	// Compute the gap: hidden lines between previous content and this hunk.
	gapNewStart := prevNewEnd + 1
	gapNewEnd := hunkInfo.NewStart - 1
	gapOldStart := prevOldEnd + 1
	gapOldEnd := hunkInfo.OldStart - 1

	totalGap := gapNewEnd - gapNewStart + 1
	if totalGap <= 0 {
		return false
	}

	// Reveal up to n lines from the bottom of the gap (closest to the hunk).
	revealStart := gapNewEnd - n + 1
	if revealStart < gapNewStart {
		revealStart = gapNewStart
	}
	revealOldStart := gapOldEnd - (gapNewEnd - revealStart)
	if revealOldStart < gapOldStart {
		revealOldStart = gapOldStart
	}

	revealCount := gapNewEnd - revealStart + 1
	if revealCount <= 0 {
		return false
	}

	// Build new context lines.
	newLines := make([]components.DiffLine, revealCount)
	for i := 0; i < revealCount; i++ {
		newLineNo := revealStart + i
		oldLineNo := revealOldStart + i
		newLines[i] = components.DiffLine{
			Type:      components.LineContext,
			OldLineNo: oldLineNo,
			NewLineNo: newLineNo,
			Content:   getLineContent(hl.HlLines, newLineNo),
		}
	}

	// If we revealed all hidden lines, remove the hunk header entirely.
	removeHunk := revealStart == gapNewStart

	// Splice into DiffLines.
	var result []components.DiffLine
	if removeHunk {
		// Replace the hunk header with the new context lines.
		result = make([]components.DiffLine, 0, len(hl.DiffLines)-1+revealCount)
		result = append(result, hl.DiffLines[:diffLineIdx]...)
		result = append(result, newLines...)
		result = append(result, hl.DiffLines[diffLineIdx+1:]...)
	} else {
		// Update the hunk header to reflect the new start, insert lines after hunk.
		newOldStart := revealOldStart
		newNewStart := revealStart
		hl.DiffLines[diffLineIdx].Content = updateHunkHeader(
			hl.DiffLines[diffLineIdx].Content, newOldStart, newNewStart, revealCount,
		)
		result = make([]components.DiffLine, 0, len(hl.DiffLines)+revealCount)
		result = append(result, hl.DiffLines[:diffLineIdx+1]...)
		result = append(result, newLines...)
		result = append(result, hl.DiffLines[diffLineIdx+1:]...)
	}

	hl.DiffLines = result

	// Keep FileDiffs in sync so ReconcileAndSync uses the correct data.
	if fileIdx < len(d.FileDiffs) {
		cp := make([]components.DiffLine, len(result))
		copy(cp, result)
		d.FileDiffs[fileIdx] = cp
	}

	// Track the expansion so it can be reapplied after reloads.
	if d.ExpandedHunks == nil {
		d.ExpandedHunks = make(map[string]map[components.HunkInfo]int)
	}
	if d.ExpandedHunks[filename] == nil {
		d.ExpandedHunks[filename] = make(map[components.HunkInfo]int)
	}
	if isNewExpansion {
		d.ExpandedHunks[filename][origInfo] = revealCount
	} else {
		d.ExpandedHunks[filename][origInfo] += revealCount
	}

	// Keep cursor on the hunk header (which stays at diffLineIdx).
	// If the hunk was fully removed, cursor lands on the first revealed
	// context line at the same index — that's fine.

	return true
}

// ReapplyExpansions re-expands any previously-expanded hunks for the given file.
// Called after a reload or re-highlight installs fresh DiffLines.
func (d *DiffViewer) ReapplyExpansions(fileIdx int) {
	if fileIdx < 0 || fileIdx >= len(d.HighlightedFiles) {
		return
	}
	filename := d.Files[fileIdx].Filename
	records, ok := d.ExpandedHunks[filename]
	if !ok || len(records) == 0 {
		return
	}

	hl := &d.HighlightedFiles[fileIdx]
	// Iterate bottom-to-top so splice index shifts don't affect unprocessed hunks.
	for i := len(hl.DiffLines) - 1; i >= 0; i-- {
		if hl.DiffLines[i].Type != components.LineHunk {
			continue
		}
		info, ok := components.ParseHunkHeader(hl.DiffLines[i].Content)
		if !ok {
			continue
		}
		if revealed, ok := records[info]; ok && revealed > 0 {
			d.expandHunkInner(fileIdx, i, revealed)
		}
	}
}

// ResetExpansions clears all remembered hunk expansions.
func (d *DiffViewer) ResetExpansions() {
	d.ExpandedHunks = nil
}

// getLineContent extracts raw text content for a line number from highlighted lines.
// Returns empty string if out of range.
func getLineContent(hlLines []string, lineNo int) string {
	if lineNo <= 0 || lineNo > len(hlLines) {
		return ""
	}
	// hlLines contains ANSI-highlighted text. We need raw content for DiffLine.Content.
	// Strip ANSI codes to get the raw text.
	return stripANSI(hlLines[lineNo-1])
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// updateHunkHeader rewrites the @@ line to reflect new starting line numbers
// and adds extraLines to both old and new counts, preserving the original
// trailing context (e.g. function name).
func updateHunkHeader(header string, newOldStart, newNewStart, extraLines int) string {
	parts := strings.SplitN(header, "@@", 3)
	if len(parts) < 3 {
		return header
	}
	ranges := strings.TrimSpace(parts[1])
	fields := strings.Fields(ranges)
	if len(fields) < 2 {
		return header
	}

	oldRange := rewriteRange(fields[0], "-", newOldStart, extraLines)
	newRange := rewriteRange(fields[1], "+", newNewStart, extraLines)

	// Preserve the original trailing context (e.g. function name) from the header.
	trailing := parts[2]

	return fmt.Sprintf("@@ %s %s @@%s", oldRange, newRange, trailing)
}

// rewriteRange rewrites a hunk range like "-10,5" with a new start and added count.
func rewriteRange(r, prefix string, newStart, extraCount int) string {
	r = strings.TrimPrefix(r, prefix)
	if comma := strings.IndexByte(r, ','); comma >= 0 {
		countStr := r[comma+1:]
		count := 0
		if n, err := strconv.Atoi(countStr); err == nil {
			count = n
		}
		return fmt.Sprintf("%s%d,%d", prefix, newStart, count+extraCount)
	}
	return fmt.Sprintf("%s%d,%d", prefix, newStart, 1+extraCount)
}

// InitFileSlices allocates the per-file slices for a new set of files.
func (d *DiffViewer) InitFileSlices(n int) {
	d.HighlightedFiles = make([]components.HighlightedDiff, n)
	d.RenderedFiles = make([]string, n)
	d.FileDiffs = make([][]components.DiffLine, n)
	d.FileDiffOffsets = make([][]int, n)
	d.FileRenderers = make([]*components.FileRenderList, n)
	d.FileBadgeData = make([]map[components.CommentKey]components.BadgeInfo, n)
}

// HunkDiffText returns the full diff text for the hunk at diffLineIdx
// (from the @@ line through all its add/del/context lines until the next hunk or EOF).
func (d *DiffViewer) HunkDiffText(fileIdx, diffLineIdx int) string {
	if fileIdx < 0 || fileIdx >= len(d.HighlightedFiles) {
		return ""
	}
	hl := d.HighlightedFiles[fileIdx]
	if diffLineIdx < 0 || diffLineIdx >= len(hl.DiffLines) {
		return ""
	}
	if hl.DiffLines[diffLineIdx].Type != components.LineHunk {
		return ""
	}

	var b strings.Builder
	b.WriteString(hl.DiffLines[diffLineIdx].Content)
	b.WriteByte('\n')
	for i := diffLineIdx + 1; i < len(hl.DiffLines); i++ {
		dl := hl.DiffLines[i]
		if dl.Type == components.LineHunk {
			break
		}
		switch dl.Type {
		case components.LineAdd:
			b.WriteByte('+')
		case components.LineDel:
			b.WriteByte('-')
		default:
			b.WriteByte(' ')
		}
		b.WriteString(dl.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// HunkSummaryCommentID returns a unique comment ID for a hunk summary at the given position.
func HunkSummaryCommentID(filename string, lineNo int) string {
	return fmt.Sprintf("hunk-summary:%s:%d", filename, lineNo)
}

// HunkLineNo returns the new-side start line number for the hunk at diffLineIdx.
func (d *DiffViewer) HunkLineNo(fileIdx, diffLineIdx int) int {
	if fileIdx < 0 || fileIdx >= len(d.HighlightedFiles) {
		return 0
	}
	hl := d.HighlightedFiles[fileIdx]
	if diffLineIdx < 0 || diffLineIdx >= len(hl.DiffLines) {
		return 0
	}
	h, ok := components.ParseHunkHeader(hl.DiffLines[diffLineIdx].Content)
	if !ok {
		return 0
	}
	return h.NewStart
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
		if d.PanelFocused {
			// Panel → diff.
			d.PanelFocused = false
			d.Tree.Focused = false
		} else {
			d.Tree.Focused = !d.Tree.Focused
		}
		return KeyHandled
	case "h", "left":
		if d.PanelFocused {
			d.PanelFocused = false
			d.Tree.Focused = false
			return KeyHandled
		}
		d.Tree.Focused = true
		return KeyHandled
	case "l", "right":
		if d.Tree.Focused {
			d.Tree.Focused = false
			return KeyHandled
		}
		// From diff → panel (only when side panel is visible).
		if d.PanelOpen && d.CommentPanelWidth() > 0 && !d.PanelFocused {
			d.PanelFocused = true
			return KeyHandled
		}
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
				}
			}
			return KeyHandled
		}
		d.WaitingG = true
		return KeyHandled
	}
	return KeyNotHandled
}
