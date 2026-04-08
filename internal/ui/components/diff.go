package components

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
	"charm.land/lipgloss/v2"

	"github.com/blakewilliams/ghq/internal/github"
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

const gutterWidth = 4

var (
	fileNameStyle = lipgloss.NewStyle().Bold(true)
	borderStyle   = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
)

// DiffRenderResult holds the rendered diff string and metadata about line positions.
type DiffRenderResult struct {
	Content         string // the full rendered string
	DiffLineOffsets []int  // rendered line index for each diff line (0-based from start of Content)
}

// HighlightedDiff holds pre-highlighted diff data that is width-independent.
// This is the expensive part (Chroma syntax highlighting) that can be cached
// across resizes.
type HighlightedDiff struct {
	File      github.PullRequestFile
	DiffLines []DiffLine
	HlLines   []string // syntax-highlighted lines (full file), or nil
	Filename  string   // for fallback highlighting
}

// HighlightDiffFile runs the expensive Chroma syntax highlighting and returns
// a HighlightedDiff that can be cached and re-formatted at different widths.
func HighlightDiffFile(f github.PullRequestFile, fileContent string, chromaStyle *chroma.Style) HighlightedDiff {
	hd := HighlightedDiff{
		File:     f,
		Filename: f.Filename,
	}
	if f.Patch == "" {
		return hd
	}
	hd.DiffLines = ParsePatchLines(f.Patch)
	if fileContent != "" {
		hd.HlLines = highlightFileLines(fileContent, f.Filename, chromaStyle)
	} else {
		// Fallback: batch-highlight the diff content.
		var codeBuilder strings.Builder
		for _, dl := range hd.DiffLines {
			if dl.Type == LineHunk {
				codeBuilder.WriteString("\n")
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

// DiffFormatOptions configures optional behavior for FormatDiffFile.
type DiffFormatOptions struct {
	// HighlightThreadLine/Side: when set, the comment thread on this line
	// gets a highlighted border color instead of the default dim border.
	HighlightThreadLine int
	HighlightThreadSide string // "LEFT" or "RIGHT"
	// HighlightCommentIndex: 1-indexed comment within the highlighted thread.
	// 0 = highlight the whole thread, >0 = highlight only that comment.
	HighlightCommentIndex int
	// RenderBody, if set, is called to render comment bodies (e.g. markdown).
	// It receives the body text, the wrap width, and the raw ANSI bg code
	// for the diff line the comment sits on. The returned string should use
	// reset+bg instead of bare \033[0m so the diff background survives.
	RenderBody func(body string, width int, bg string) string
}

// FormatDiffFile takes a pre-highlighted diff and formats it at the given width.
// This is cheap (no Chroma) and can be re-run on resize.
func FormatDiffFile(hd HighlightedDiff, width int, colors styles.DiffColors, comments []github.ReviewComment, opts ...DiffFormatOptions) DiffRenderResult {
	f := hd.File

	if f.Patch == "" {
		return DiffRenderResult{Content: styles.SubtitleStyle.Render("(binary or empty)")}
	}

	// Make a working copy of diff lines so we don't mutate the cached highlight data.
	diffLines := make([]DiffLine, len(hd.DiffLines))
	copy(diffLines, hd.DiffLines)

	// Format each line at the target width using cached highlighted content.
	formatDiffLinesFromHL(diffLines, hd.HlLines, hd.Filename, width, colors)

	commentsByLine := buildCommentThreads(comments)

	var opt DiffFormatOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	var b strings.Builder
	renderedLineIdx := 0

	offsets := make([]int, len(diffLines))
	for i, dl := range diffLines {
		offsets[i] = renderedLineIdx

		// Wrap the rendered line if it exceeds width.
		segments := wrapRenderedLine(dl.Rendered, width, dl.Type, colors)
		for _, seg := range segments {
			b.WriteString(seg)
			b.WriteString("\n")
			renderedLineIdx++
		}

		var ck commentKey
		if dl.Type == LineDel {
			ck = commentKey{Side: "LEFT", Line: dl.OldLineNo}
		} else {
			ck = commentKey{Side: "RIGHT", Line: dl.NewLineNo}
		}
		if ck.Line > 0 {
			if threads, ok := commentsByLine[ck]; ok {
				highlighted := ck.Line == opt.HighlightThreadLine && ck.Side == opt.HighlightThreadSide
				hlIdx := 0
				if highlighted {
					hlIdx = opt.HighlightCommentIndex // 0=whole thread, >0=single comment
				}
				threadStr := renderCommentThread(threads, width, dl.Type, colors, highlighted, hlIdx, colors.HighlightBorderFg, opt.RenderBody)
				for _, tl := range strings.Split(strings.TrimRight(threadStr, "\n"), "\n") {
					b.WriteString(tl + "\n")
					renderedLineIdx++
				}
			}
		}
	}
	return DiffRenderResult{Content: strings.TrimRight(b.String(), "\n"), DiffLineOffsets: offsets}
}

// RenderDiffFile is a convenience that highlights and formats in one call.
func RenderDiffFile(f github.PullRequestFile, fileContent string, width int, colors styles.DiffColors, comments []github.ReviewComment) DiffRenderResult {
	hd := HighlightDiffFile(f, fileContent, colors.ChromaStyle)
	return FormatDiffFile(hd, width, colors, comments)
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

func formatDiffLinesFromHL(diffLines []DiffLine, hlLines []string, filename string, width int, colors styles.DiffColors) {
	// Detect if hlLines are indexed by line number (full file) or sequential (fallback).
	// Full file mode: hlLines[lineNo-1] gives the highlighted line.
	// Fallback mode: hlLines are in diff order (including blank for hunks).
	useLineIndex := false
	if len(diffLines) > 0 && len(hlLines) > 0 {
		for _, dl := range diffLines {
			if dl.Type == LineAdd && dl.NewLineNo > 0 && dl.NewLineNo <= len(hlLines) {
				useLineIndex = true
				break
			}
			if dl.Type == LineContext && dl.NewLineNo > 0 && dl.NewLineNo <= len(hlLines) {
				useLineIndex = true
				break
			}
		}
	}

	hlIdx := 0
	for i := range diffLines {
		dl := &diffLines[i]
		switch dl.Type {
		case LineHunk:
			dl.Rendered = renderHunkLine(dl.Content, width, colors)
			if !useLineIndex {
				hlIdx++
			}

		case LineAdd:
			var hl string
			if useLineIndex {
				hl = getHighlightedLine(hlLines, dl.NewLineNo)
			} else if hlIdx < len(hlLines) {
				hl = hlLines[hlIdx]
				hlIdx++
			}
			hl = expandTabs(hl)
			hl = injectBackground(hl, colors.AddBg)
			gutter := colors.AddBg + colors.AddFg +
				padNum(gutterWidth) + padNum(gutterWidth, dl.NewLineNo) +
				" " + "\033[1m" + "+" + "\033[0m" + colors.AddBg
			dl.Rendered = padWithBg(truncateLine(gutter+hl, width), width, colors.AddBg)

		case LineDel:
			var hl string
			if useLineIndex {
				hl = highlightSnippet(dl.Content, filename, colors.ChromaStyle)
			} else if hlIdx < len(hlLines) {
				hl = hlLines[hlIdx]
				hlIdx++
			}
			hl = expandTabs(hl)
			hl = injectBackground(hl, colors.DelBg)
			gutter := colors.DelBg + colors.DelFg +
				padNum(gutterWidth, dl.OldLineNo) + padNum(gutterWidth) +
				" " + "\033[1m" + "-" + "\033[0m" + colors.DelBg
			dl.Rendered = padWithBg(truncateLine(gutter+hl, width), width, colors.DelBg)

		case LineContext:
			var hl string
			if useLineIndex {
				hl = getHighlightedLine(hlLines, dl.NewLineNo)
			} else if hlIdx < len(hlLines) {
				hl = hlLines[hlIdx]
				hlIdx++
			}
			hl = expandTabs(hl)
			gutter := styles.DiffLineNum.Render(
				padNum(gutterWidth, dl.OldLineNo) + padNum(gutterWidth, dl.NewLineNo) + " ",
			)
			dl.Rendered = padToWidth(truncateLine(gutter+" "+hl, width), width)
		}
	}
}

// renderHunkLine renders a @@ hunk header with full-width background.
func renderHunkLine(content string, width int, colors styles.DiffColors) string {
	line := colors.HunkBg + colors.HunkFg + expandTabs(content)
	return padWithBg(truncateLine(line, width), width, colors.HunkBg)
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
func wrapRenderedLine(rendered string, targetW int, lt LineType, colors styles.DiffColors) []string {
	visW := lipgloss.Width(rendered)
	if visW <= targetW {
		return []string{rendered}
	}

	// Determine the background code for padding continuation lines.
	var bgCode string
	switch lt {
	case LineAdd:
		bgCode = colors.AddBg
	case LineDel:
		bgCode = colors.DelBg
	case LineHunk:
		bgCode = colors.HunkBg
	}

	gutterW := gutterWidth*2 + 2 // 10 visible chars
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
		contGutter := bgCode + strings.Repeat(" ", gutterW)
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

func truncateLine(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Truncate(s, width, "")
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

// bgForLineType returns the raw ANSI bg code for the given diff line type.
func bgForLineType(lt LineType, colors styles.DiffColors) string {
	switch lt {
	case LineAdd:
		return colors.AddBg
	case LineDel:
		return colors.DelBg
	default:
		return ""
	}
}


// commentGutterWidth is the visible width of the gutter area in diff lines.
// padNum(4) + padNum(4) + " " + marker(1) = 10 visible chars.
const commentGutterWidth = gutterWidth*2 + 2

// commentGutter renders an empty gutter matching the diff line style.
func commentGutter(bg string) string {
	return bg + strings.Repeat(" ", commentGutterWidth)
}

// emptyLine renders a full-width blank line with the given bg.
func emptyLine(bg string, width int) string {
	return padWithBg(bg, width, bg) + "\n"
}

// renderCommentThread renders a thread of review comments as a block below a diff line.
// It inherits the background color from the line type (add/del/context) and uses
// the line's fg color for borders to make the comment box stand out.
// When highlighted is true, the border uses yellow instead of the dim default.
// renderCommentThread renders a thread of review comments.
// hlIdx: 0 = highlight whole thread (or none if !highlighted), >0 = highlight only that 1-indexed comment.
func renderCommentThread(comments []github.ReviewComment, width int, lt LineType, colors styles.DiffColors, highlighted bool, hlIdx int, hlBorderFg string, renderBody func(string, int, string) string) string {
	bg := bgForLineType(lt, colors)
	defaultBorderFg := colors.BorderFg
	// If highlighting the whole thread (hlIdx==0), use highlight color for all borders.
	threadBorderFg := defaultBorderFg
	if highlighted && hlIdx == 0 {
		threadBorderFg = hlBorderFg
	}
	gutterStr := commentGutter(bg)
	// Content area is everything after the gutter.
	contentW := width - commentGutterWidth
	if contentW < 20 {
		contentW = 20
	}

	var b strings.Builder

	// Blank line above.
	b.WriteString(emptyLine(bg, width))

	for i, c := range comments {
		// Per-comment border color: highlight only the selected comment.
		borderFg := threadBorderFg
		if highlighted && hlIdx > 0 {
			if i+1 == hlIdx {
				borderFg = hlBorderFg
			} else {
				borderFg = defaultBorderFg
			}
		}

		authorStyled := ColoredAuthor(c.User.Login)
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

		// Body lines: │ text │
		innerW := contentW - 4 // "│ " + " │"
		if innerW < 10 {
			innerW = 10
		}
		body := c.Body
		if renderBody != nil {
			body = renderBody(body, innerW, bg)
		}
		for _, line := range strings.Split(body, "\n") {
			visW := lipgloss.Width(line)
			if visW > innerW {
				line = ansi.Truncate(line, innerW, "…")
				visW = lipgloss.Width(line)
			}
			pad := innerW - visW
			if pad < 0 {
				pad = 0
			}
			content := gutterStr + borderFg + "│" + "\033[0m" + bg +
				" " + line + "\033[0m" + bg + strings.Repeat(" ", pad) + " " +
				borderFg + "│" + "\033[0m"
			b.WriteString(padWithBg(content, width, bg))
			b.WriteString("\n")
		}
	}

	// Bottom border uses the last comment's border color.
	lastBorderFg := threadBorderFg
	if highlighted && hlIdx > 0 {
		if len(comments) == hlIdx {
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

	return b.String()
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
