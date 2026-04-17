package components

import (
	"strings"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/styles"
)

// RenderContext holds width + styling needed to render any item.
type RenderContext struct {
	Width      int
	Colors     styles.DiffColors
	ColW       int // gutter column width
	RenderBody func(body string, width int, bg string) string
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
}

func NewDiffLineItem(idx int, dl *DiffLine) *DiffLineItem {
	return &DiffLineItem{diffLineIdx: idx, DiffLine: dl}
}

func (d *DiffLineItem) Render(rc RenderContext) string {
	if d.width == rc.Width && d.content != "" {
		return d.content
	}
	d.render(rc)
	return d.content
}

func (d *DiffLineItem) RenderedLineCount(rc RenderContext) int {
	if d.width == rc.Width && d.content != "" {
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
	gutterW := TotalGutterWidth(rc.ColW)
	segments := wrapRenderedLine(d.DiffLine.Rendered, rc.Width, d.DiffLine.Type, rc.Colors, gutterW)
	d.content = strings.Join(segments, "\n") + "\n"
	d.lineCount = len(segments)
	d.width = rc.Width
}

// ---------------------------------------------------------------------------
// CommentThreadItem — a comment thread below a diff line
// ---------------------------------------------------------------------------

type CommentThreadItem struct {
	diffLineIdx    int
	Side           string
	Line           int
	Comments       []github.ReviewComment
	Highlighted    bool
	HlIdx          int // 0 = whole thread, >0 = specific comment (1-based)
	ParentLineType LineType

	// Cached render state
	content      string
	lineCount    int
	commentLines []int // per-comment rendered line count (from renderCommentThread)
	width        int
}

func NewCommentThreadItem(diffLineIdx int, side string, line int, comments []github.ReviewComment, parentLT LineType) *CommentThreadItem {
	return &CommentThreadItem{
		diffLineIdx:    diffLineIdx,
		Side:           side,
		Line:           line,
		Comments:       comments,
		ParentLineType: parentLT,
	}
}

func (c *CommentThreadItem) Render(rc RenderContext) string {
	if c.width == rc.Width && c.content != "" {
		return c.content
	}
	c.render(rc)
	return c.content
}

func (c *CommentThreadItem) RenderedLineCount(rc RenderContext) int {
	if c.width == rc.Width && c.content != "" {
		return c.lineCount
	}
	c.render(rc)
	return c.lineCount
}

func (c *CommentThreadItem) DiffIdx() int        { return c.diffLineIdx }
func (c *CommentThreadItem) IsDiffLine() bool     { return false }
func (c *CommentThreadItem) ThreadKey() (string, int) { return c.Side, c.Line }

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
		return
	}
	gutterW := TotalGutterWidth(rc.ColW)
	result := renderCommentThread(
		c.Comments, rc.Width, c.ParentLineType, rc.Colors,
		c.Highlighted, c.HlIdx, rc.Colors.HighlightBorderFg,
		rc.RenderBody, gutterW,
	)
	c.content = result.content
	c.lineCount = strings.Count(strings.TrimRight(result.content, "\n"), "\n") + 1
	c.commentLines = result.commentLines
	c.width = rc.Width
}

// ---------------------------------------------------------------------------
// FileRenderList — ordered list of Renderable items for a file
// ---------------------------------------------------------------------------

type FileRenderList struct {
	Items []Renderable

	// Cached full output — invalidated on any mutation.
	cachedStr string
	dirty     bool
}

// String returns the full rendered content for this file.
func (f *FileRenderList) String(rc RenderContext) string {
	if !f.dirty && f.cachedStr != "" {
		return f.cachedStr
	}
	var b strings.Builder
	for _, item := range f.Items {
		b.WriteString(item.Render(rc))
	}
	f.cachedStr = strings.TrimRight(b.String(), "\n")
	f.dirty = false
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
	f.cachedStr = ""
}

// ReplaceThread finds an existing comment thread for the given side+line and
// replaces it. Returns true if found.
func (f *FileRenderList) ReplaceThread(side string, line int, replacement Renderable) bool {
	for i, item := range f.Items {
		s, l := item.ThreadKey()
		if s == side && l == line {
			f.Items[i] = replacement
			f.dirty = true
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
	f.cachedStr = ""
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

// CommentPositions returns a CommentPosition for each individual comment
// across all threads, with correct visual-line offsets.
func (f *FileRenderList) CommentPositions(rc RenderContext) []CommentPosition {
	var positions []CommentPosition
	offset := 0
	for _, item := range f.Items {
		if ct, ok := item.(*CommentThreadItem); ok && len(ct.Comments) > 0 {
			// Ensure the thread is rendered so commentLines is populated.
			ct.Render(rc)
			// Thread layout: blank line, then per-comment blocks.
			lineInThread := 1 // skip blank line above
			for ci, cl := range ct.commentLines {
				positions = append(positions, CommentPosition{
					Line:   ct.Line,
					Side:   ct.Side,
					Idx:    ci,
					Offset: offset + lineInThread,
				})
				lineInThread += cl
			}
		}
		offset += item.RenderedLineCount(rc)
	}
	return positions
}
