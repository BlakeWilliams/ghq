package components

import (
	"regexp"
	"strings"
	"time"

	"github.com/blakewilliams/gg/internal/github"
	"github.com/blakewilliams/gg/internal/review/comments"
	"github.com/blakewilliams/gg/internal/ui/styles"
)

// RenderContext holds width + styling needed to render any item.
type RenderContext struct {
	Width      int
	Colors     styles.DiffColors
	ColW       int // gutter column width
	RenderBody func(body string, width int, bg string) string
	AnimFrame  int // animation frame (0-3) for running tool spinners

	// Highlight state — passed through so DiffLineItem can apply highlights
	// natively during rendering, eliminating post-hoc overlay string surgery.
	CursorLine     int            // diff line index of cursor (-1 = none)
	SelectionStart int            // diff line index of selection start (-1 = none)
	SelectionEnd   int            // diff line index of selection end (-1 = none)
	SearchPattern  *regexp.Regexp // compiled search pattern (nil = no search)
	SearchMatches  map[int]int    // diffLineIdx -> 1-based match number
}

// RenderComment is the view-model for rendering a single comment in a thread.
// Both github.ReviewComment (API) and comments.LocalComment (local) convert
// to this type at the rendering boundary.
type RenderComment struct {
	ID        int
	Author    string
	CreatedAt time.Time
	Blocks    []comments.ContentBlock
}

// ReviewCommentToRender converts a GitHub API ReviewComment into a
// RenderComment by wrapping the body as a single TextBlock.
func ReviewCommentToRender(c github.ReviewComment) RenderComment {
	return RenderComment{
		ID:        c.ID,
		Author:    c.User.Login,
		CreatedAt: c.CreatedAt,
		Blocks:    []comments.ContentBlock{comments.TextBlock{Text: c.Body}},
	}
}

// ReviewCommentsToRender converts a slice of GitHub API ReviewComments.
func ReviewCommentsToRender(cs []github.ReviewComment) []RenderComment {
	out := make([]RenderComment, len(cs))
	for i, c := range cs {
		out[i] = ReviewCommentToRender(c)
	}
	return out
}

// Renderable is a single element in the rendered diff output.
// Implementations: DiffLineItem, CommentThreadItem.
type Renderable interface {
	// Render returns the rendered ANSI string for this item.
	Render(rc RenderContext) string
	// RenderedLineCount returns how many visual lines this item occupies.
	RenderedLineCount(rc RenderContext) int
	// DiffIdx returns the diff line index this item corresponds to, or -1.
	DiffIdx() int
	// IsDiffLine returns true for diff code lines and hunk headers.
	IsDiffLine() bool
	// ThreadKey returns (side, line) for comment threads, or ("", 0) for non-threads.
	ThreadKey() (string, int)
	// Invalidate clears cached render output.
	Invalidate()
}

// ---------------------------------------------------------------------------
// DiffLineItem — a single diff line (add/del/context/hunk header)
// ---------------------------------------------------------------------------

type DiffLineItem struct {
	diffLineIdx int
	DiffLine    *DiffLine

	// Cached render state
	content   string
	lineCount int
	width     int
	dirty     bool // set by ReconcileHighlights when this item needs re-render
}

func NewDiffLineItem(idx int, dl *DiffLine) *DiffLineItem {
	return &DiffLineItem{diffLineIdx: idx, DiffLine: dl}
}

func (d *DiffLineItem) Render(rc RenderContext) string {
	if d.width == rc.Width && d.content != "" && !d.dirty {
		return d.content
	}
	d.render(rc)
	return d.content
}

func (d *DiffLineItem) RenderedLineCount(rc RenderContext) int {
	if d.width == rc.Width && d.content != "" && !d.dirty {
		return d.lineCount
	}
	d.render(rc)
	return d.lineCount
}

func (d *DiffLineItem) DiffIdx() int        { return d.diffLineIdx }
func (d *DiffLineItem) IsDiffLine() bool     { return true }
func (d *DiffLineItem) ThreadKey() (string, int) { return "", 0 }

func (d *DiffLineItem) Invalidate() {
	d.width = 0
	d.content = ""
	d.lineCount = 0
}

func (d *DiffLineItem) render(rc RenderContext) {
	if d.DiffLine == nil || d.DiffLine.Rendered == "" {
		d.content = ""
		d.lineCount = 0
		d.width = rc.Width
		return
	}

	rendered := d.DiffLine.Rendered
	gutterW := TotalGutterWidth(rc.ColW)
	colors := rc.Colors
	idx := d.diffLineIdx

	// Determine the effective bg for this line (used as restoreBg for search).
	isCursor := rc.CursorLine >= 0 && idx == rc.CursorLine && rc.SelectionStart < 0
	isSelected := rc.SelectionStart >= 0 && idx >= rc.SelectionStart && idx <= rc.SelectionEnd

	// Skip hunk headers from cursor/selection highlighting.
	if d.DiffLine.Type == LineHunk {
		isCursor = false
		isSelected = false
	}

	var effectiveBg string // for search restoreBg
	var wrapBg string      // override for wrapRenderedLine (empty = derive from LineType)
	if isCursor || isSelected {
		// Apply cursor/selection bg: swap sign char, replace all bg codes.
		rendered = ReplaceCursorSign(rendered)
		var selBg string
		switch d.DiffLine.Type {
		case LineAdd:
			selBg = colors.SelectedAddBg
		case LineDel:
			selBg = colors.SelectedDelBg
		default:
			selBg = colors.SelectedCtxBg
		}
		if selBg != "" {
			rendered = ReplaceBackground(rendered, colors.AddBg, colors.DelBg, selBg)
			effectiveBg = selBg
			wrapBg = selBg
		}
	} else {
		switch d.DiffLine.Type {
		case LineAdd:
			effectiveBg = colors.AddBg
		case LineDel:
			effectiveBg = colors.DelBg
		default:
			effectiveBg = "\033[49m"
		}
	}

	// Apply search highlights.
	if rc.SearchPattern != nil {
		if _, ok := rc.SearchMatches[idx]; ok {
			rendered = HighlightSearchSpans(rendered, d.DiffLine.Content, rc.SearchPattern, gutterW, colors.SearchMatchBg, colors.SearchMatchFg, effectiveBg)
		}
	}

	segments := wrapRenderedLine(rendered, rc.Width, d.DiffLine.Type, rc.Colors, gutterW, wrapBg)

	d.content = strings.Join(segments, "\n") + "\n"
	d.lineCount = len(segments)
	d.width = rc.Width
	d.dirty = false
}

// ---------------------------------------------------------------------------
// CommentThreadItem — a comment thread below a diff line
// ---------------------------------------------------------------------------

type CommentThreadItem struct {
	diffLineIdx    int
	Side           string
	Line           int
	Comments       []RenderComment
	Highlighted    bool
	HlIdx          int // 0 = whole thread, >0 = specific comment (1-based)
	ParentLineType LineType
	OpenBottom     bool // when true, omit bottom border + trailing blank (reply box connects below)

	// Cached render state
	content        string
	lineCount      int
	commentLines   []int // per-comment rendered line count (from renderCommentThread)
	width          int
	cachedOpenBtm  bool
}

func NewCommentThreadItem(diffLineIdx int, side string, line int, comments []RenderComment, parentLT LineType) *CommentThreadItem {
	return &CommentThreadItem{
		diffLineIdx:    diffLineIdx,
		Side:           side,
		Line:           line,
		Comments:       comments,
		ParentLineType: parentLT,
	}
}

func (c *CommentThreadItem) Render(rc RenderContext) string {
	if c.width == rc.Width && c.content != "" && c.cachedOpenBtm == c.OpenBottom {
		return c.content
	}
	c.render(rc)
	return c.content
}

func (c *CommentThreadItem) RenderedLineCount(rc RenderContext) int {
	if c.width == rc.Width && c.content != "" && c.cachedOpenBtm == c.OpenBottom {
		return c.lineCount
	}
	c.render(rc)
	return c.lineCount
}

func (c *CommentThreadItem) DiffIdx() int              { return c.diffLineIdx }
func (c *CommentThreadItem) IsDiffLine() bool           { return false }
func (c *CommentThreadItem) ThreadKey() (string, int)   { return c.Side, c.Line }

// CommentIndexAtOffset maps a visual line offset within this thread's rendered
// body to a 1-based comment index. Returns 0 if the offset falls in the
// thread's blank leader line or is out of range.
func (c *CommentThreadItem) CommentIndexAtOffset(offset int, rc RenderContext) int {
	c.Render(rc) // ensure commentLines populated
	running := 1 // skip blank line above comments
	for i, cl := range c.commentLines {
		if offset < running+cl {
			return i + 1 // 1-based
		}
		running += cl
	}
	return 0
}

func (c *CommentThreadItem) Invalidate() {
	c.width = 0
	c.content = ""
	c.lineCount = 0
	c.commentLines = nil
}

func (c *CommentThreadItem) render(rc RenderContext) {
	if len(c.Comments) == 0 {
		c.content = ""
		c.lineCount = 0
		c.commentLines = nil
		c.width = rc.Width
		c.cachedOpenBtm = c.OpenBottom
		return
	}
	gutterW := TotalGutterWidth(rc.ColW)
	result := renderCommentThread(
		c.Comments, rc.Width, c.ParentLineType, rc.Colors,
		c.Highlighted, c.HlIdx, rc.Colors.SelectedBorderFg,
		rc.RenderBody, gutterW, rc.AnimFrame, c.OpenBottom,
	)
	c.content = result.content
	c.lineCount = strings.Count(strings.TrimRight(result.content, "\n"), "\n") + 1
	c.commentLines = result.commentLines
	c.width = rc.Width
	c.cachedOpenBtm = c.OpenBottom
}

// ---------------------------------------------------------------------------
// FileRenderList — ordered list of Renderable items for a file
// ---------------------------------------------------------------------------

type FileRenderList struct {
	Items []Renderable

	// Fast lookup: diffLineIdx → *DiffLineItem.
	diffLineMap map[int]*DiffLineItem

	// Cached full output — invalidated when any item is dirty.
	cachedStr     string
	dirty         bool // structural change (items added/removed)
	hasDirtyItems bool // at least one item needs re-render

	// Last-rendered highlight state — used by ReconcileHighlights to diff
	// current state against what was last rendered and dirty only the minimum
	// set of items. Initialised to sentinel values so first reconcile dirties
	// the correct initial set.
	lastCursorLine  int
	lastSelStart    int
	lastSelEnd        int
	lastSearchMatch   map[int]int // diffLineIdx -> match number
	lastSearchPattern string // .String() of last search pattern (for regex change detection)
	reconciled        bool   // false until first ReconcileHighlights call
}

// BuildDiffLineMap populates the fast diffLineIdx → *DiffLineItem index.
// Must be called after Items is populated or modified.
func (f *FileRenderList) BuildDiffLineMap() {
	f.diffLineMap = make(map[int]*DiffLineItem, len(f.Items))
	for _, item := range f.Items {
		if dli, ok := item.(*DiffLineItem); ok {
			f.diffLineMap[dli.diffLineIdx] = dli
		}
	}
}

// InvalidateLines marks specific diff lines as dirty so they re-render
// on the next String() call. O(len(indices)) with map lookup.
func (f *FileRenderList) InvalidateLines(indices ...int) {
	for _, idx := range indices {
		if dli, ok := f.diffLineMap[idx]; ok {
			dli.dirty = true
			f.hasDirtyItems = true
		}
	}
}

// ReconcileHighlights diffs the current RenderContext against the last-rendered
// state and marks only the changed items dirty. Returns true if any items were
// dirtied.
func (f *FileRenderList) ReconcileHighlights(rc RenderContext) bool {
	dirtied := false

	// Derive the pattern key for comparison.
	var patternKey string
	if rc.SearchPattern != nil {
		patternKey = rc.SearchPattern.String()
	}

	if !f.reconciled {
		// First reconciliation — dirty everything that has highlights.
		f.reconciled = true
		if rc.CursorLine >= 0 {
			f.InvalidateLines(rc.CursorLine)
			dirtied = true
		}
		if rc.SelectionStart >= 0 {
			for i := rc.SelectionStart; i <= rc.SelectionEnd; i++ {
				f.InvalidateLines(i)
			}
			dirtied = true
		}
		for idx := range rc.SearchMatches {
			f.InvalidateLines(idx)
			dirtied = true
		}
		f.lastCursorLine = rc.CursorLine
		f.lastSelStart = rc.SelectionStart
		f.lastSelEnd = rc.SelectionEnd
		f.lastSearchMatch = copyMatchMap(rc.SearchMatches)
		f.lastSearchPattern = patternKey
		return dirtied
	}

	// Cursor moved?
	if rc.CursorLine != f.lastCursorLine {
		if f.lastCursorLine >= 0 {
			f.InvalidateLines(f.lastCursorLine)
			dirtied = true
		}
		if rc.CursorLine >= 0 {
			f.InvalidateLines(rc.CursorLine)
			dirtied = true
		}
		f.lastCursorLine = rc.CursorLine
	}

	// Selection changed?
	if rc.SelectionStart != f.lastSelStart || rc.SelectionEnd != f.lastSelEnd {
		// Invalidate old range.
		if f.lastSelStart >= 0 {
			for i := f.lastSelStart; i <= f.lastSelEnd; i++ {
				f.InvalidateLines(i)
			}
			dirtied = true
		}
		// Invalidate new range.
		if rc.SelectionStart >= 0 {
			for i := rc.SelectionStart; i <= rc.SelectionEnd; i++ {
				f.InvalidateLines(i)
			}
			dirtied = true
		}
		f.lastSelStart = rc.SelectionStart
		f.lastSelEnd = rc.SelectionEnd
	}

	// Search pattern changed? Even if match lines are the same, the inline
	// highlights depend on the regex itself, so re-render all matched lines.
	if patternKey != f.lastSearchPattern {
		for idx := range f.lastSearchMatch {
			f.InvalidateLines(idx)
			dirtied = true
		}
		for idx := range rc.SearchMatches {
			f.InvalidateLines(idx)
			dirtied = true
		}
		f.lastSearchMatch = copyMatchMap(rc.SearchMatches)
		f.lastSearchPattern = patternKey
	} else if !matchMapsEqual(rc.SearchMatches, f.lastSearchMatch) {
		// Same pattern but match set changed (e.g. file content updated).
		for idx := range f.lastSearchMatch {
			f.InvalidateLines(idx)
			dirtied = true
		}
		for idx := range rc.SearchMatches {
			f.InvalidateLines(idx)
			dirtied = true
		}
		f.lastSearchMatch = copyMatchMap(rc.SearchMatches)
	}

	return dirtied
}

func copyMatchMap(m map[int]int) map[int]int {
	if m == nil {
		return nil
	}
	out := make(map[int]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func matchMapsEqual(a, b map[int]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// MarkDirty forces the next String() call to re-render all items.
func (f *FileRenderList) MarkDirty() {
	f.dirty = true
	f.hasDirtyItems = true
	f.cachedStr = ""
}

// IsDirty returns true if any item needs re-rendering.
func (f *FileRenderList) IsDirty() bool {
	return f.dirty || f.hasDirtyItems
}

// String returns the full rendered content for this file.
func (f *FileRenderList) String(rc RenderContext) string {
	if !f.dirty && !f.hasDirtyItems && f.cachedStr != "" {
		return f.cachedStr
	}
	var b strings.Builder
	for _, item := range f.Items {
		b.WriteString(item.Render(rc))
	}
	f.cachedStr = strings.TrimRight(b.String(), "\n")
	f.dirty = false
	f.hasDirtyItems = false
	return f.cachedStr
}

// TotalLines returns the total number of visual lines across all items.
func (f *FileRenderList) TotalLines(rc RenderContext) int {
	total := 0
	for _, item := range f.Items {
		total += item.RenderedLineCount(rc)
	}
	return total
}

// DiffLineOffset returns the rendered line offset for a given diff line index.
func (f *FileRenderList) DiffLineOffset(diffLineIdx int, rc RenderContext) int {
	offset := 0
	for _, item := range f.Items {
		if item.IsDiffLine() && item.DiffIdx() == diffLineIdx {
			return offset
		}
		offset += item.RenderedLineCount(rc)
	}
	return -1
}

// DiffLineOffsets returns a slice mapping each diff line index to its rendered line offset.
func (f *FileRenderList) DiffLineOffsets(numDiffLines int, rc RenderContext) []int {
	offsets := make([]int, numDiffLines)
	offset := 0
	for _, item := range f.Items {
		idx := item.DiffIdx()
		if item.IsDiffLine() && idx >= 0 && idx < numDiffLines {
			offsets[idx] = offset
		}
		offset += item.RenderedLineCount(rc)
	}
	return offsets
}

// InsertAfterDiffLine inserts items after the given diff line and any existing
// threads on that line.
func (f *FileRenderList) InsertAfterDiffLine(diffLineIdx int, items ...Renderable) {
	insertAt := -1
	for i, item := range f.Items {
		if item.IsDiffLine() && item.DiffIdx() == diffLineIdx {
			insertAt = i + 1
		} else if insertAt > 0 {
			_, line := item.ThreadKey()
			if line > 0 && item.DiffIdx() == diffLineIdx {
				insertAt = i + 1
			} else {
				break
			}
		}
	}
	if insertAt < 0 {
		return
	}

	newItems := make([]Renderable, 0, len(f.Items)+len(items))
	newItems = append(newItems, f.Items[:insertAt]...)
	newItems = append(newItems, items...)
	newItems = append(newItems, f.Items[insertAt:]...)
	f.Items = newItems
	f.dirty = true
	f.hasDirtyItems = true
	f.cachedStr = ""
	f.BuildDiffLineMap()
}

// ReplaceThread finds an existing comment thread for the given side+line and
// replaces it. Returns true if found.
func (f *FileRenderList) ReplaceThread(side string, line int, replacement Renderable) bool {
	for i, item := range f.Items {
		s, l := item.ThreadKey()
		if s == side && l == line {
			f.Items[i] = replacement
			f.dirty = true
			f.hasDirtyItems = true
			f.cachedStr = ""
			return true
		}
	}
	return false
}

// RemoveThread removes the comment thread for the given side+line.
func (f *FileRenderList) RemoveThread(side string, line int) bool {
	for i, item := range f.Items {
		s, l := item.ThreadKey()
		if s == side && l == line {
			f.Items = append(f.Items[:i], f.Items[i+1:]...)
			f.dirty = true
			f.hasDirtyItems = true
			f.cachedStr = ""
			return true
		}
	}
	return false
}

// InvalidateAll marks all items as needing re-render.
func (f *FileRenderList) InvalidateAll() {
	for _, item := range f.Items {
		item.Invalidate()
	}
	f.dirty = true
	f.hasDirtyItems = true
	f.cachedStr = ""
	// Reset reconciliation state so next reconcile re-evaluates everything.
	f.reconciled = false
}

// ItemAtLine returns the item containing the given visual line and the
// offset within that item. Returns nil if out of range.
func (f *FileRenderList) ItemAtLine(visualLine int, rc RenderContext) (Renderable, int) {
	offset := 0
	for _, item := range f.Items {
		lc := item.RenderedLineCount(rc)
		if visualLine < offset+lc {
			return item, visualLine - offset
		}
		offset += lc
	}
	return nil, 0
}

// FindThread returns the index of the comment thread for side+line, or -1.
func (f *FileRenderList) FindThread(side string, line int) int {
	for i, item := range f.Items {
		s, l := item.ThreadKey()
		if s == side && l == line {
			return i
		}
	}
	return -1
}

// ThreadEndOffset returns the rendered line offset immediately after the
// comment thread at (side, line). Returns -1 if the thread is not found.
func (f *FileRenderList) ThreadEndOffset(side string, line int, rc RenderContext) int {
	offset := 0
	for _, item := range f.Items {
		offset += item.RenderedLineCount(rc)
		s, l := item.ThreadKey()
		if s == side && l == line {
			return offset
		}
	}
	return -1
}

func (f *FileRenderList) CommentPositions(rc RenderContext) []CommentPosition {
	var positions []CommentPosition
	for _, item := range f.Items {
		if ct, ok := item.(*CommentThreadItem); ok && len(ct.Comments) > 0 {
			for ci := range ct.Comments {
				commentID := 0
				if ci < len(ct.Comments) {
					commentID = ct.Comments[ci].ID
				}
				positions = append(positions, CommentPosition{
					Line:      ct.Line,
					Side:      ct.Side,
					Idx:       ci,
					CommentID: commentID,
				})
			}
		}
	}
	return positions
}

// ThreadCommentCount returns the number of comments in the thread at (side, line).
func (f *FileRenderList) ThreadCommentCount(side string, line int) int {
	for _, item := range f.Items {
		if ct, ok := item.(*CommentThreadItem); ok && ct.Side == side && ct.Line == line {
			return len(ct.Comments)
		}
	}
	return 0
}

// ThreadCommentOffset returns the rendered line offset and height of the Nth
// comment (0-based) in the thread at (side, line). Returns (-1, 0) if not found.
func (f *FileRenderList) ThreadCommentOffset(side string, line int, commentIdx int, rc RenderContext) (offset, height int) {
	runningOffset := 0
	for _, item := range f.Items {
		if ct, ok := item.(*CommentThreadItem); ok && ct.Side == side && ct.Line == line {
			ct.Render(rc)
			if commentIdx < 0 || commentIdx >= len(ct.commentLines) {
				return -1, 0
			}
			// Thread layout: blank line, then per-comment blocks.
			lineInThread := 1 // skip blank line above
			for ci, cl := range ct.commentLines {
				if ci == commentIdx {
					return runningOffset + lineInThread, cl
				}
				lineInThread += cl
			}
			return -1, 0
		}
		runningOffset += item.RenderedLineCount(rc)
	}
	return -1, 0
}
