package components

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/review/comments"
	"github.com/blakewilliams/ghq/internal/ui/styles"
)

// LineType identifies the kind of diff line.
type LineType int

const (
	LineContext LineType = iota
	LineAdd
	LineDel
	LineHunk
)

// DiffLine represents a single parsed + rendered line in a diff.
type DiffLine struct {
	Type      LineType
	OldLineNo int
	NewLineNo int
	Content   string // raw code text (no ANSI)
	Rendered  string // fully rendered with gutter + syntax highlighting + bg
}

const DefaultGutterColWidth = 4

// GutterColWidth returns the width needed for each line-number column,
// using the default unless the diff has line numbers that need more space.
func GutterColWidth(diffLines []DiffLine) int {
	maxLine := 0
	for _, dl := range diffLines {
		if dl.OldLineNo > maxLine {
			maxLine = dl.OldLineNo
		}
		if dl.NewLineNo > maxLine {
			maxLine = dl.NewLineNo
		}
	}
	w := len(strconv.Itoa(maxLine))
	if w < DefaultGutterColWidth {
		return DefaultGutterColWidth
	}
	return w
}

// TotalGutterWidth returns the full gutter width: two columns + separator + space + marker.
func TotalGutterWidth(colW int) int {
	return colW*2 + 3
}

var (
	fileNameStyle = lipgloss.NewStyle().Bold(true)
	borderStyle   = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
)

// CommentPosition records the location of a comment in a thread.
type CommentPosition struct {
	Line      int    // diff line number (source)
	Side      string // "LEFT" or "RIGHT"
	Idx       int    // 0-based index within the thread
	CommentID int    // comment ID for read/unread tracking
}

// HighlightedDiff holds pre-highlighted diff data that is width-independent.
// This is the expensive part (Chroma syntax highlighting) that can be cached
// across resizes.
type HighlightedDiff struct {
	File       github.PullRequestFile
	DiffLines  []DiffLine
	HlLines    []string // syntax-highlighted lines for new/right side (full file), or nil
	HlLinesOld []string // syntax-highlighted lines for old/left side (full file), or nil
	Filename   string   // for fallback highlighting
}

// HighlightDiffFile runs the expensive Chroma syntax highlighting and returns
// a HighlightedDiff that can be cached and re-formatted at different widths.
// fileContent is the new/right side (working tree), oldFileContent is the old/left side (e.g., HEAD).
func HighlightDiffFile(f github.PullRequestFile, fileContent, oldFileContent string, chromaStyle *chroma.Style) HighlightedDiff {
	hd := HighlightedDiff{
		File:     f,
		Filename: f.Filename,
	}
	if f.Patch == "" {
		return hd
	}
	hd.DiffLines = ParsePatchLines(f.Patch)

	// Simply use the provided content if non-empty - trust the caller.
	newContentValid := fileContent != ""
	oldContentValid := oldFileContent != ""

	if newContentValid {
		hd.HlLines = highlightFileLines(fileContent, f.Filename, chromaStyle)
	}
	if oldContentValid {
		hd.HlLinesOld = highlightFileLines(oldFileContent, f.Filename, chromaStyle)
	}

	// Fallback: if we don't have valid new content, batch-highlight the diff
	// content for sequential access (needed for add/context lines).
	if !newContentValid {
		var codeBuilder strings.Builder
		for i, dl := range hd.DiffLines {
			if dl.Type == LineHunk {
				// Insert newline separator between hunks, but not before the first one.
				if i > 0 {
					codeBuilder.WriteString("\n")
				}
			} else {
				codeBuilder.WriteString(dl.Content)
				codeBuilder.WriteString("\n")
			}
		}
		highlighted := highlightBlock(codeBuilder.String(), f.Filename, chromaStyle)
		hd.HlLines = strings.Split(highlighted, "\n")
	}
	return hd
}

// DiffFormatOptions configures optional behavior for diff formatting.
type DiffFormatOptions struct {
	// HighlightThreadLine/Side: when set, the comment thread on this line
	// gets a highlighted border color instead of the default dim border.
	HighlightThreadLine int
	HighlightThreadSide string // "LEFT" or "RIGHT"
	// HighlightCommentIndex: 1-indexed comment within the highlighted thread.
	// 0 = highlight the whole thread, >0 = highlight only that comment.
	HighlightCommentIndex int
	// RenderBody, if set, is called to render comment bodies (e.g. markdown).
	RenderBody func(body string, width int, bg string) string
	// PendingComments are extra RenderComments (e.g. streaming copilot replies)
	// keyed by side+line. They are appended after base comments.
	PendingComments map[CommentKey][]RenderComment
	// ThreadedComments, if set, provides pre-built RenderComments keyed by
	// side+line. When set, these replace the ReviewComment→RenderComment
	// conversion for base comments (preserving blocks from local comments).
	ThreadedComments map[CommentKey][]RenderComment
}

// CommentKey identifies a comment thread position.
type CommentKey struct {
	Side string
	Line int
}

// BuildRenderList creates a FileRenderList from a highlighted diff.
// The diffLines slice is owned by the returned list — callers must not
// mutate it afterwards. Each DiffLineItem holds a pointer into this slice.
func BuildRenderList(diffLines []DiffLine, comments []github.ReviewComment, opts ...DiffFormatOptions) *FileRenderList {
	commentsByLine := buildCommentThreads(comments)

	var opt DiffFormatOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	placed := make(map[commentKey]bool)

	// Helper: try to place a thread for a given key after diff line i.
	tryPlace := func(items []Renderable, i int, ck commentKey, parentLT LineType) []Renderable {
		if ck.Line <= 0 || placed[ck] {
			return items
		}
		rendered := renderThread(ck, commentsByLine, opt)
		if len(rendered) == 0 {
			return items
		}
		placed[ck] = true
		ct := NewCommentThreadItem(i, ck.Side, ck.Line, rendered, parentLT)
		ct.Highlighted = ck.Line == opt.HighlightThreadLine && ck.Side == opt.HighlightThreadSide
		if ct.Highlighted {
			ct.HlIdx = opt.HighlightCommentIndex
		}
		return append(items, ct)
	}

	items := make([]Renderable, 0, len(diffLines))
	for i := range diffLines {
		dl := &diffLines[i]
		items = append(items, NewDiffLineItem(i, dl))

		switch dl.Type {
		case LineDel:
			items = tryPlace(items, i, commentKey{Side: "LEFT", Line: dl.OldLineNo}, dl.Type)
		case LineContext:
			// Context lines carry both old and new numbers — check both sides.
			items = tryPlace(items, i, commentKey{Side: "RIGHT", Line: dl.NewLineNo}, dl.Type)
			items = tryPlace(items, i, commentKey{Side: "LEFT", Line: dl.OldLineNo}, dl.Type)
		default:
			items = tryPlace(items, i, commentKey{Side: "RIGHT", Line: dl.NewLineNo}, dl.Type)
		}
	}

	return &FileRenderList{Items: items, dirty: true}
}

// renderThread builds the RenderComment slice for a given key,
// merging base + pending comments.
func renderThread(ck commentKey, commentsByLine map[commentKey][]github.ReviewComment, opt DiffFormatOptions) []RenderComment {
	pk := CommentKey{Side: ck.Side, Line: ck.Line}
	var rendered []RenderComment
	if opt.ThreadedComments != nil {
		rendered = opt.ThreadedComments[pk]
	} else if threadComments, ok := commentsByLine[ck]; ok {
		rendered = ReviewCommentsToRender(threadComments)
	}
	if pending := opt.PendingComments[pk]; len(pending) > 0 {
		rendered = append(rendered, pending...)
	}
	return rendered
}

// CommentsForThread returns the comments that form the thread at the given side+line.
func CommentsForThread(allComments []github.ReviewComment, side string, line int) []github.ReviewComment {
	threads := buildCommentThreads(allComments)
	key := commentKey{Side: side, Line: line}
	return threads[key]
}

// BuildThreadedRenderComments threads ReviewComments by position and converts
// to RenderComments. If blockLookup is non-nil, comments whose ID appears in
// the map get their blocks from the lookup (preserving tool calls, etc.)
// instead of wrapping Body as a single TextBlock.
func BuildThreadedRenderComments(comments []github.ReviewComment, blockLookup map[int][]comments.ContentBlock) map[CommentKey][]RenderComment {
	threads := buildCommentThreads(comments)
	result := make(map[CommentKey][]RenderComment, len(threads))
	for ck, threadComments := range threads {
		pk := CommentKey{Side: ck.Side, Line: ck.Line}
		rendered := make([]RenderComment, len(threadComments))
		for i, c := range threadComments {
			if blocks, ok := blockLookup[c.ID]; ok && len(blocks) > 0 {
				rendered[i] = RenderComment{
					ID:        c.ID,
					Author:    c.User.Login,
					CreatedAt: c.CreatedAt,
					Blocks:    blocks,
				}
			} else {
				rendered[i] = ReviewCommentToRender(c)
			}
		}
		result[pk] = rendered
	}
	return result
}

// ParsePatchLines parses a unified diff patch into structured DiffLines.
func ParsePatchLines(patch string) []DiffLine {
	lines := strings.Split(patch, "\n")
	result := make([]DiffLine, 0, len(lines))
	oldNum, newNum := 0, 0

	for _, line := range lines {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "@@"):
			dl := DiffLine{Type: LineHunk, Content: line}
			if h, ok := parseHunkHeader(line); ok {
				oldNum = h.oldStart
				newNum = h.newStart
			}
			result = append(result, dl)
		case strings.HasPrefix(line, "+"):
			result = append(result, DiffLine{
				Type: LineAdd, NewLineNo: newNum, Content: line[1:],
			})
			newNum++
		case strings.HasPrefix(line, "-"):
			result = append(result, DiffLine{
				Type: LineDel, OldLineNo: oldNum, Content: line[1:],
			})
			oldNum++
		default:
			content := line
			if len(line) > 0 && line[0] == ' ' {
				content = line[1:]
			}
			result = append(result, DiffLine{
				Type: LineContext, OldLineNo: oldNum, NewLineNo: newNum,
				Content: content,
			})
			oldNum++
			newNum++
		}
	}
	return result
}

// formatDiffLinesFromHL formats diff lines using pre-highlighted content at the given width.
// hlLines may be indexed by line number (full file highlight) or sequentially (fallback).
// The function detects which mode based on whether line numbers in the diff lines
// fall within the hlLines range.
const tabWidth = 4

// expandTabs replaces tab characters with spaces (tabWidth-aligned).
// Works on strings that may contain ANSI escape codes.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	return strings.ReplaceAll(s, "\t", strings.Repeat(" ", tabWidth))
}

// detectUseLineIndex checks if hlLines are indexed by line number (full file)
// or sequential (fallback diff-based).
func detectUseLineIndex(diffLines []DiffLine, hlLines []string) bool {
	if len(diffLines) == 0 || len(hlLines) == 0 {
		return false
	}
	allFit := true
	hasLines := false
	for _, dl := range diffLines {
		if dl.Type == LineAdd && dl.NewLineNo > 0 {
			hasLines = true
			if dl.NewLineNo > len(hlLines) {
				allFit = false
				break
			}
		}
		if dl.Type == LineContext && dl.NewLineNo > 0 {
			hasLines = true
			if dl.NewLineNo > len(hlLines) {
				allFit = false
				break
			}
		}
	}
	return hasLines && allFit
}

// detectUseOldLineIndex checks if hlLinesOld are indexed by old line number.
func detectUseOldLineIndex(diffLines []DiffLine, hlLinesOld []string) bool {
	if len(diffLines) == 0 || len(hlLinesOld) == 0 {
		return false
	}
	allFit := true
	hasLines := false
	for _, dl := range diffLines {
		if dl.Type == LineDel && dl.OldLineNo > 0 {
			hasLines = true
			if dl.OldLineNo > len(hlLinesOld) {
				allFit = false
				break
			}
		}
		if dl.Type == LineContext && dl.OldLineNo > 0 {
			hasLines = true
			if dl.OldLineNo > len(hlLinesOld) {
				allFit = false
				break
			}
		}
	}
	return hasLines && allFit
}

func FormatDiffLinesFromHL(diffLines []DiffLine, hlLines, hlLinesOld []string, filename string, width int, colors styles.DiffColors, colW int) {
	formatDiffLinesFromHL(diffLines, hlLines, hlLinesOld, filename, width, colors, colW)
}

func formatDiffLinesFromHL(diffLines []DiffLine, hlLines, hlLinesOld []string, filename string, width int, colors styles.DiffColors, colW int) {
	// Detect if hlLines are indexed by line number (full file) or sequential (fallback).
	// Full file mode: hlLines[lineNo-1] gives the highlighted line.
	// Fallback mode: hlLines are in diff order (including blank for hunks).
	useNewLineIndex := detectUseLineIndex(diffLines, hlLines)
	useOldLineIndex := len(hlLinesOld) > 0 && detectUseOldLineIndex(diffLines, hlLinesOld)

	hlIdx := 0
	seenHunk := false
	for i := range diffLines {
		dl := &diffLines[i]
		switch dl.Type {
		case LineHunk:
			dl.Rendered = renderHunkLine(dl.Content, width, colors)
			// In sequential fallback mode, hunks after the first have a separator
			// line in hlLines that we need to skip.
			if !useNewLineIndex && !useOldLineIndex && seenHunk {
				hlIdx++
			}
			seenHunk = true

		case LineAdd:
			var hl string
			if useNewLineIndex {
				hl = getHighlightedLine(hlLines, dl.NewLineNo)
			} else if hlIdx < len(hlLines) {
				hl = hlLines[hlIdx]
				hlIdx++
			}
			hl = expandTabs(hl)
			hl = injectBackground(hl, colors.AddBg)
			gutter := colors.AddBg + colors.AddFg +
				padNum(colW) + " " + padNum(colW, dl.NewLineNo) +
				" " + "\033[1m" + "+" + "\033[0m" + colors.AddBg
			dl.Rendered = gutter + hl

		case LineDel:
			var hl string
			if useOldLineIndex {
				hl = getHighlightedLine(hlLinesOld, dl.OldLineNo)
			} else {
				// Fallback: highlight single line (slower but handles edge cases).
				// Never use hlLines (new file content) for deleted lines.
				hl = highlightSnippet(dl.Content, filename, colors.ChromaStyle)
			}
			hl = expandTabs(hl)
			hl = injectBackground(hl, colors.DelBg)
			gutter := colors.DelBg + colors.DelFg +
				padNum(colW, dl.OldLineNo) + " " + padNum(colW) +
				" " + "\033[1m" + "-" + "\033[0m" + colors.DelBg
			dl.Rendered = gutter + hl

		case LineContext:
			var hl string
			if useNewLineIndex {
				hl = getHighlightedLine(hlLines, dl.NewLineNo)
			} else if hlIdx < len(hlLines) {
				hl = hlLines[hlIdx]
				hlIdx++
			}
			hl = expandTabs(hl)
			gutter := styles.DiffLineNum.Render(
				padNum(colW, dl.OldLineNo) + " " + padNum(colW, dl.NewLineNo) + " ",
			)
			dl.Rendered = gutter + " " + hl
		}
	}
}

// renderHunkLine renders a @@ hunk header with full-width background.
func renderHunkLine(content string, width int, colors styles.DiffColors) string {
	line := colors.HunkBg + colors.HunkFg + expandTabs(content)
	return line
}

// injectBackground replaces every SGR reset in chroma output with a reset
// followed by the given background code, so the bg survives per-token resets.
func injectBackground(highlighted string, bgCode string) string {
	if bgCode == "" {
		return highlighted
	}
	// Chroma uses \033[0m, lipgloss uses \033[m — catch both.
	s := strings.ReplaceAll(highlighted, "\033[0m", "\033[0m"+bgCode)
	s = strings.ReplaceAll(s, "\033[m", "\033[m"+bgCode)
	return s
}

// wrapRenderedLine splits a rendered diff line into multiple lines if it
// exceeds the target width. The first segment keeps the original gutter;
// continuation segments get a blank gutter with matching background.
func wrapRenderedLine(rendered string, targetW int, lt LineType, colors styles.DiffColors, gutterW int) []string {
	// Determine the background code for padding.
	var bgCode string
	switch lt {
	case LineAdd:
		bgCode = colors.AddBg
	case LineDel:
		bgCode = colors.DelBg
	case LineHunk:
		bgCode = colors.HunkBg
	}

	visW := lipgloss.Width(rendered)
	if visW <= targetW {
		// Short line — pad to full width with background.
		if bgCode != "" {
			return []string{padWithBg(rendered, targetW, bgCode)}
		}
		return []string{padToWidth(rendered, targetW)}
	}

	// gutterW is passed in as total visible gutter width
	codeW := targetW - gutterW
	if codeW < 10 {
		codeW = 10
	}

	// First segment: full line truncated to targetW.
	var segments []string
	first := ansi.Truncate(rendered, targetW, "")
	if bgCode != "" {
		first = padWithBg(first, targetW, bgCode)
	} else {
		first = padToWidth(first, targetW)
	}
	segments = append(segments, first)

	// Remaining code: cut everything after the first segment's visible chars.
	// The gutter is part of the first targetW chars, so the overflow starts
	// at visible position targetW.
	remaining := ansi.Cut(rendered, targetW, visW)
	for {
		remainW := lipgloss.Width(remaining)
		if remainW <= 0 {
			break
		}
		// Continuation gutter: spaces + wrap arrow (↩) in dim.
		wrapArrow := "\033[2m↪\033[0m" + bgCode + " "
		contGutter := bgCode + strings.Repeat(" ", gutterW-2) + wrapArrow
		chunk := ansi.Truncate(remaining, codeW, "")
		chunkW := lipgloss.Width(chunk)
		if chunkW <= 0 {
			break
		}
		line := contGutter + chunk
		if bgCode != "" {
			line = padWithBg(line, targetW, bgCode)
		} else {
			line = padToWidth(line, targetW)
		}
		segments = append(segments, line)

		if chunkW >= remainW {
			break
		}
		remaining = ansi.Cut(remaining, chunkW, remainW)
	}

	return segments
}

// padToWidth pads a line to targetWidth with spaces (no background color).
func padToWidth(s string, targetWidth int) string {
	currentWidth := lipgloss.Width(s)
	if currentWidth < targetWidth {
		return s + strings.Repeat(" ", targetWidth-currentWidth)
	}
	return s
}

// padWithBg pads a line to targetWidth with the background color, then resets.
func padWithBg(s string, targetWidth int, bgCode string) string {
	currentWidth := lipgloss.Width(s)
	pad := ""
	if currentWidth < targetWidth {
		pad = strings.Repeat(" ", targetWidth-currentWidth)
	}
	return s + pad + "\033[0m"
}

// --- Helpers ---

type hunk struct {
	oldStart int
	newStart int
}

func parseHunkHeader(line string) (hunk, bool) {
	parts := strings.SplitN(line, "@@", 3)
	if len(parts) < 3 {
		return hunk{}, false
	}
	ranges := strings.TrimSpace(parts[1])
	fields := strings.Fields(ranges)
	if len(fields) < 2 {
		return hunk{}, false
	}

	h := hunk{}
	old := strings.TrimPrefix(fields[0], "-")
	if comma := strings.IndexByte(old, ','); comma >= 0 {
		old = old[:comma]
	}
	if n, err := strconv.Atoi(old); err == nil {
		h.oldStart = n
	}

	new_ := strings.TrimPrefix(fields[1], "+")
	if comma := strings.IndexByte(new_, ','); comma >= 0 {
		new_ = new_[:comma]
	}
	if n, err := strconv.Atoi(new_); err == nil {
		h.newStart = n
	}

	return h, true
}

func padNum(w int, nums ...int) string {
	if len(nums) == 0 || nums[0] == 0 {
		return strings.Repeat(" ", w)
	}
	s := strconv.Itoa(nums[0])
	if len(s) >= w {
		return s
	}
	return strings.Repeat(" ", w-len(s)) + s
}

func getHighlightedLine(lines []string, lineNum int) string {
	idx := lineNum - 1
	if idx >= 0 && idx < len(lines) {
		return lines[idx]
	}
	return ""
}

func highlightFileLines(content, filename string, chromaStyle *chroma.Style) []string {
	highlighted := highlightBlock(content, filename, chromaStyle)
	return strings.Split(highlighted, "\n")
}

func highlightSnippet(code, filename string, chromaStyle *chroma.Style) string {
	result := highlightBlock(code, filename, chromaStyle)
	return strings.TrimRight(result, "\n")
}

// wrapCommentLine wraps a single line of comment body text to fit within maxW
// visible columns, preserving ANSI sequences across chunks.
func wrapCommentLine(line string, maxW int) []string {
	visW := lipgloss.Width(line)
	if visW <= maxW || maxW <= 0 {
		return []string{line}
	}
	var segments []string
	remaining := line
	for {
		remainW := lipgloss.Width(remaining)
		if remainW <= 0 {
			break
		}
		chunk := ansi.Truncate(remaining, maxW, "")
		chunkW := lipgloss.Width(chunk)
		if chunkW <= 0 {
			break
		}
		segments = append(segments, chunk)
		if chunkW >= remainW {
			break
		}
		remaining = ansi.Cut(remaining, chunkW, remainW)
	}
	if len(segments) == 0 {
		return []string{line}
	}
	return segments
}

// commentKey encodes side + line for comment thread lookup.
type commentKey struct {
	Side string // "LEFT" or "RIGHT"
	Line int
}

// buildCommentThreads groups comments by (side, line),
// threading replies under their parent.
func buildCommentThreads(comments []github.ReviewComment) map[commentKey][]github.ReviewComment {
	if len(comments) == 0 {
		return nil
	}

	byID := make(map[int]*github.ReviewComment, len(comments))
	for i := range comments {
		byID[comments[i].ID] = &comments[i]
	}

	result := make(map[commentKey][]github.ReviewComment)
	// Add root comments first.
	for _, c := range comments {
		if c.InReplyToID != nil {
			continue
		}
		line := 0
		if c.Line != nil {
			line = *c.Line
		} else if c.OriginalLine != nil {
			line = *c.OriginalLine
		}
		side := c.Side
		if side == "" {
			side = "RIGHT"
		}
		if line > 0 {
			key := commentKey{Side: side, Line: line}
			result[key] = append(result[key], c)
		}
	}
	// Add replies after their root.
	for _, c := range comments {
		if c.InReplyToID == nil {
			continue
		}
		root := byID[*c.InReplyToID]
		if root == nil {
			continue
		}
		for root.InReplyToID != nil {
			if parent, ok := byID[*root.InReplyToID]; ok {
				root = parent
			} else {
				break
			}
		}
		line := 0
		if root.Line != nil {
			line = *root.Line
		} else if root.OriginalLine != nil {
			line = *root.OriginalLine
		}
		side := root.Side
		if side == "" {
			side = "RIGHT"
		}
		if line > 0 {
			key := commentKey{Side: side, Line: line}
			result[key] = append(result[key], c)
		}
	}
	return result
}

// dimCode is the raw ANSI escape for BrightBlack foreground (used for comment borders).
const dimCode = "\033[90m"

// BgForLineType returns the ANSI background escape code for a diff line type.
func BgForLineType(lt LineType, colors styles.DiffColors) string {
	switch lt {
	case LineAdd:
		return colors.AddBg
	case LineDel:
		return colors.DelBg
	default:
		return ""
	}
}

func bgForLineType(lt LineType, colors styles.DiffColors) string {
	return BgForLineType(lt, colors)
}

// CommentGutter renders an empty gutter matching the diff line style.
func CommentGutter(bg string, gutterW int) string {
	return commentGutter(bg, gutterW)
}

// commentGutter renders an empty gutter matching the diff line style.
func commentGutter(bg string, gutterW int) string {
	return bg + strings.Repeat(" ", gutterW)
}

// PadWithBg pads a string to targetWidth and appends a reset escape.
func PadWithBg(s string, targetWidth int, bgCode string) string {
	return padWithBg(s, targetWidth, bgCode)
}

// emptyLine renders a full-width blank line with the given bg.
func emptyLine(bg string, width int) string {
	return padWithBg(bg, width, bg) + "\n"
}

// renderCommentThread renders a thread of review comments as a block below a diff line.
// It inherits the background color from the line type (add/del/context) and uses
// the line's fg color for borders to make the comment box stand out.
// When highlighted is true, the border uses yellow instead of the dim default.
// commentThreadResult holds the rendered thread string and per-comment line counts.
type commentThreadResult struct {
	content      string
	commentLines []int // rendered line count for each comment (header + body)
}

// renderBodyLines renders text body lines inside comment borders.
// Returns the number of lines written.
func renderBodyLines(b *strings.Builder, body, gutterStr, borderFg, bg string, innerW, width int) int {
	count := 0
	for _, line := range strings.Split(body, "\n") {
		wrappedLines := wrapCommentLine(line, innerW)
		for _, wl := range wrappedLines {
			visW := lipgloss.Width(wl)
			pad := innerW - visW
			if pad < 0 {
				pad = 0
			}
			content := gutterStr + borderFg + "│" + "\033[0m" + bg +
				" " + wl + "\033[0m" + bg + strings.Repeat(" ", pad) + " " +
				borderFg + "│" + "\033[0m"
			b.WriteString(padWithBg(content, width, bg))
			b.WriteString("\n")
			count++
		}
	}
	return count
}

// renderToolGroup renders a tool call group as a rounded-border sub-box.
// Border color reflects aggregate status: green (all done), yellow (some running), red (any failed).
// If a label is set (from report_intent), it shows in yellow on the top border.
func renderToolGroup(b *strings.Builder, group comments.ToolGroupBlock, gutterStr, borderFg, bg string, innerW, width int, colors styles.DiffColors, animFrame int) int {
	if len(group.Tools) == 0 {
		return 0
	}

	// Status-based border color for the tool sub-box.
	var toolBorderFg string
	switch group.ToolGroupStatus() {
	case "running":
		toolBorderFg = "\033[33m" // yellow
	case "failed":
		toolBorderFg = "\033[31m" // red
	default:
		toolBorderFg = "\033[32m" // green
	}
	dimFg := "\033[90m"    // bright black for tool names/args
	yellowFg := "\033[33m" // yellow for label text

	// The tool sub-box sits inside the comment borders.
	subBoxW := innerW - 2 // 1 char padding on each side within the comment
	if subBoxW < 10 {
		subBoxW = 10
	}
	subInnerW := subBoxW - 4 // "│ " + " │" inside the sub-box
	if subInnerW < 6 {
		subInnerW = 6
	}

	count := 0

	// Top border: ╭ Label ───╮  or  ╭──────────╮  (no label)
	var subTop string
	if group.Label != "" {
		label := " " + group.Label + " "
		labelW := lipgloss.Width(label)
		topFill := subBoxW - 2 - labelW
		if topFill < 0 {
			topFill = 0
		}
		subTop = toolBorderFg + "╭" + yellowFg + label + toolBorderFg + strings.Repeat("─", topFill) + "╮" + "\033[0m"
	} else {
		topFill := subBoxW - 2
		if topFill < 0 {
			topFill = 0
		}
		subTop = toolBorderFg + "╭" + strings.Repeat("─", topFill) + "╮" + "\033[0m"
	}
	subTopW := lipgloss.Width(subTop)
	subTopPad := innerW - subTopW
	if subTopPad < 0 {
		subTopPad = 0
	}
	line := gutterStr + borderFg + "│" + "\033[0m" + bg +
		" " + subTop + bg + strings.Repeat(" ", subTopPad) + " " +
		borderFg + "│" + "\033[0m"
	b.WriteString(padWithBg(line, width, bg))
	b.WriteString("\n")
	count++

	// Tool rows: │ ● name  args │
	spinFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	for _, tc := range group.Tools {
		var marker string
		switch tc.Status {
		case "done":
			marker = "\033[32m●" + bg // green, then restore bg
		case "failed":
			marker = "\033[31m✕" + bg // red
		default:
			marker = "\033[33m" + spinFrames[animFrame%len(spinFrames)] + bg // yellow spinner
		}

		name := tc.Name
		nameW := lipgloss.Width(name)

		// Budget: "● " (2) + name + " args" — fits in subInnerW.
		maxNameW := subInnerW - 2
		if nameW > maxNameW {
			name = name[:maxNameW-1] + "…"
			nameW = maxNameW
		}

		// Build row content: marker + name, then optionally truncated args.
		var rowContent string
		if tc.Arguments != "" {
			argsSpace := subInnerW - 2 - nameW - 1 // -2 for "● ", -1 for space before args
			if argsSpace > 3 {
				args := tc.Arguments
				argsW := lipgloss.Width(args)
				if argsW > argsSpace {
					args = args[:argsSpace-1] + "…"
				}
				rowContent = marker + " " + dimFg + name + bg + " " + dimFg + args + bg
			} else {
				rowContent = marker + " " + dimFg + name + bg
			}
		} else {
			rowContent = marker + " " + dimFg + name + bg
		}

		rowVisW := lipgloss.Width(rowContent)
		rowPad := subInnerW - rowVisW
		if rowPad < 0 {
			rowPad = 0
		}
		subRow := toolBorderFg + "│" + bg +
			" " + rowContent + strings.Repeat(" ", rowPad) + " " +
			toolBorderFg + "│" + "\033[0m"
		subRowW := lipgloss.Width(subRow)
		outerPad := innerW - subRowW
		if outerPad < 0 {
			outerPad = 0
		}
		line := gutterStr + borderFg + "│" + "\033[0m" + bg +
			" " + subRow + bg + strings.Repeat(" ", outerPad) + " " +
			borderFg + "│" + "\033[0m"
		b.WriteString(padWithBg(line, width, bg))
		b.WriteString("\n")
		count++
	}

	// Bottom border: ╰───────╯
	botFill := subBoxW - 2
	if botFill < 0 {
		botFill = 0
	}
	subBot := toolBorderFg + "╰" + strings.Repeat("─", botFill) + "╯" + "\033[0m"
	subBotW := lipgloss.Width(subBot)
	subBotPad := innerW - subBotW
	if subBotPad < 0 {
		subBotPad = 0
	}
	line = gutterStr + borderFg + "│" + "\033[0m" + bg +
		" " + subBot + bg + strings.Repeat(" ", subBotPad) + " " +
		borderFg + "│" + "\033[0m"
	b.WriteString(padWithBg(line, width, bg))
	b.WriteString("\n")
	count++

	return count
}

// renderCommentThread renders a thread of review comments.
// hlIdx: 0 = highlight whole thread (or none if !highlighted), >0 = highlight only that 1-indexed comment.
func renderCommentThread(rcComments []RenderComment, width int, lt LineType, colors styles.DiffColors, highlighted bool, hlIdx int, hlBorderFg string, renderBody func(string, int, string) string, gutterW int, animFrame int, openBottom bool) commentThreadResult {
	bg := bgForLineType(lt, colors)
	defaultBorderFg := colors.BorderFg
	// If highlighting the whole thread (hlIdx==0), use highlight color for all borders.
	threadBorderFg := defaultBorderFg
	if highlighted && hlIdx == 0 {
		threadBorderFg = hlBorderFg
	}
	gutterStr := commentGutter(bg, gutterW)
	// Content area is everything after the gutter.
	contentW := width - gutterW
	if contentW < 20 {
		contentW = 20
	}

	var b strings.Builder
	var commentLineCounts []int
	lineCount := 0

	// Blank line above.
	b.WriteString(emptyLine(bg, width))
	lineCount++

	for i, c := range rcComments {
		commentStart := lineCount

		// Per-comment border color: highlight only the selected comment.
		borderFg := threadBorderFg
		if highlighted && hlIdx > 0 {
			if i+1 == hlIdx {
				borderFg = hlBorderFg
			} else {
				borderFg = defaultBorderFg
			}
		}

		authorStyled := ColoredAuthor(c.Author)
		author := " " + authorStyled + "\033[0m" + bg + " "
		ageStr := relativeTime(c.CreatedAt)
		age := dimCode + ageStr + "\033[0m" + bg + " "
		authorW := 1 + lipgloss.Width(authorStyled) + 1 // " @user "
		ageW := len(ageStr) + 1

		var left, right string
		if i == 0 {
			left = "╭"
			right = "╮"
		} else {
			left = "├"
			right = "┤"
		}

		// ╭ + author + age + ─fill─ + ╮ = contentW
		fillW := contentW - 1 - authorW - ageW - 1
		if fillW < 0 {
			fillW = 0
		}
		topLine := gutterStr + borderFg + left + "\033[0m" + bg +
			author + age +
			borderFg + strings.Repeat("─", fillW) + right + "\033[0m"
		b.WriteString(padWithBg(topLine, width, bg))
		b.WriteString("\n")
		lineCount++

		// Body lines: │ text │
		innerW := contentW - 4 // "│ " + " │"
		if innerW < 10 {
			innerW = 10
		}

		// Render each content block.
		blocks := c.Blocks
		if len(blocks) == 0 {
			blocks = []comments.ContentBlock{comments.TextBlock{}}
		}
		for _, block := range blocks {
			switch blk := block.(type) {
			case comments.TextBlock:
				body := blk.Text
				if renderBody != nil && body != "" {
					body = renderBody(body, innerW, bg)
				}
				lineCount += renderBodyLines(&b, body, gutterStr, borderFg, bg, innerW, width)

			case comments.ToolGroupBlock:
				lineCount += renderToolGroup(&b, blk, gutterStr, borderFg, bg, innerW, width, colors, animFrame)
			}
		}

		commentLineCounts = append(commentLineCounts, lineCount-commentStart)
	}

	// Bottom border + trailing blank — omitted when openBottom so a reply box can connect.
	if !openBottom {
		lastBorderFg := threadBorderFg
		if highlighted && hlIdx > 0 {
			if len(rcComments) == hlIdx {
				lastBorderFg = hlBorderFg
			} else {
				lastBorderFg = defaultBorderFg
			}
		}
		fillW := contentW - 2
		if fillW < 0 {
			fillW = 0
		}
		bottomLine := gutterStr + lastBorderFg + "╰" + strings.Repeat("─", fillW) + "╯" + "\033[0m"
		b.WriteString(padWithBg(bottomLine, width, bg))
		b.WriteString("\n")

		// Blank line below.
		b.WriteString(emptyLine(bg, width))
		lineCount++ // bottom border
		lineCount++ // blank line below
	}

	return commentThreadResult{
		content:      b.String(),
		commentLines: commentLineCounts,
	}
}

// wrapText wraps text to the given width, splitting on whitespace.
func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		current := words[0]
		for _, w := range words[1:] {
			if len(current)+1+len(w) > width {
				lines = append(lines, current)
				current = w
			} else {
				current += " " + w
			}
		}
		lines = append(lines, current)
	}
	return lines
}

// relativeTime formats a time as a human-readable relative string.
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", h)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	default:
		months := int(d.Hours() / 24 / 30)
		if months <= 1 {
			return "1mo ago"
		}
		return fmt.Sprintf("%dmo ago", months)
	}
}

func highlightBlock(code, filename string, chromaStyle *chroma.Style) string {
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	formatter := formatters.Get("terminal16m")
	style := chromaStyle
	if style == nil {
		style = chromastyles.Get("monokai")
	}

	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return code
	}

	var b strings.Builder
	err = formatter.Format(&b, style, iterator)
	if err != nil {
		return code
	}

	return strings.TrimRight(b.String(), "\n")
}
