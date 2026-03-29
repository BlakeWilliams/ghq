package components

import (
	"fmt"
	"strconv"
	"strings"

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

// RenderDiffFile renders a single file's diff with full-width colored backgrounds.
func RenderDiffFile(f github.PullRequestFile, fileContent string, width int, colors styles.DiffColors) string {
	name := f.Filename
	if f.Status == "renamed" && f.PreviousFilename != "" {
		name = f.PreviousFilename + " → " + f.Filename
	}

	adds := lipgloss.NewStyle().Foreground(lipgloss.Green).Render(fmt.Sprintf("+%d", f.Additions))
	dels := lipgloss.NewStyle().Foreground(lipgloss.Red).Render(fmt.Sprintf("-%d", f.Deletions))
	statsPlain := fmt.Sprintf("+%d -%d", f.Additions, f.Deletions)

	nameMax := width - len(statsPlain) - 2
	if nameMax < 0 {
		nameMax = 0
	}
	if lipgloss.Width(name) > nameMax {
		name = ansi.Truncate(name, nameMax-1, "…")
	}
	gap := width - lipgloss.Width(name) - len(statsPlain)
	if gap < 1 {
		gap = 1
	}

	header := fileNameStyle.Render(name) + strings.Repeat(" ", gap) + adds + " " + dels
	rule := borderStyle.Render(strings.Repeat("─", width))

	if f.Patch == "" {
		return header + "\n" + rule + "\n" + styles.SubtitleStyle.Render("(binary or empty)") + "\n" + rule
	}

	diffLines := parsePatchLines(f.Patch)

	if fileContent != "" {
		hlLines := highlightFileLines(fileContent, f.Filename, colors.ChromaStyle)
		renderDiffLines(diffLines, hlLines, f.Filename, width, colors)
	} else {
		renderDiffLinesFallback(diffLines, f.Filename, width, colors)
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(rule)
	b.WriteString("\n")
	for _, dl := range diffLines {
		b.WriteString(dl.Rendered)
		b.WriteString("\n")
	}
	b.WriteString(rule)
	return b.String()
}

// parsePatchLines parses a unified diff patch into structured DiffLines.
func parsePatchLines(patch string) []DiffLine {
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

// renderDiffLines renders each DiffLine using pre-highlighted full file lines.
func renderDiffLines(diffLines []DiffLine, hlLines []string, filename string, width int, colors styles.DiffColors) {
	for i := range diffLines {
		dl := &diffLines[i]
		switch dl.Type {
		case LineHunk:
			dl.Rendered = renderHunkLine(dl.Content, width, colors)

		case LineAdd:
			hl := getHighlightedLine(hlLines, dl.NewLineNo)
			hl = injectBackground(hl, colors.AddBg)
			gutter := colors.AddBg + colors.AddFg +
				padNum(gutterWidth) + padNum(gutterWidth, dl.NewLineNo) +
				" " + "\033[1m" + "+" + "\033[0m" + colors.AddBg
			dl.Rendered = padWithBg(truncateLine(gutter+hl, width), width, colors.AddBg)

		case LineDel:
			hl := highlightSnippet(dl.Content, filename, colors.ChromaStyle)
			hl = injectBackground(hl, colors.DelBg)
			gutter := colors.DelBg + colors.DelFg +
				padNum(gutterWidth, dl.OldLineNo) + padNum(gutterWidth) +
				" " + "\033[1m" + "-" + "\033[0m" + colors.DelBg
			dl.Rendered = padWithBg(truncateLine(gutter+hl, width), width, colors.DelBg)

		case LineContext:
			hl := getHighlightedLine(hlLines, dl.NewLineNo)
			gutter := styles.DiffLineNum.Render(
				padNum(gutterWidth, dl.OldLineNo) + padNum(gutterWidth, dl.NewLineNo) + " ",
			)
			dl.Rendered = truncateLine(gutter+" "+hl, width)
		}
	}
}

// renderDiffLinesFallback renders when no file content is available.
func renderDiffLinesFallback(diffLines []DiffLine, filename string, width int, colors styles.DiffColors) {
	// Build a single code block from all non-hunk lines for batch highlighting.
	var codeBuilder strings.Builder
	for _, dl := range diffLines {
		if dl.Type == LineHunk {
			codeBuilder.WriteString("\n")
		} else {
			codeBuilder.WriteString(dl.Content)
			codeBuilder.WriteString("\n")
		}
	}
	highlighted := highlightBlock(codeBuilder.String(), filename, colors.ChromaStyle)
	hlLines := strings.Split(highlighted, "\n")

	gutterTotal := gutterWidth*2 + 1
	codeWidth := width - gutterTotal - 1
	if codeWidth < 1 {
		codeWidth = 1
	}

	hlIdx := 0
	for i := range diffLines {
		dl := &diffLines[i]
		switch dl.Type {
		case LineHunk:
			dl.Rendered = renderHunkLine(dl.Content, width, colors)
			hlIdx++

		case LineAdd:
			hl := ""
			if hlIdx < len(hlLines) {
				hl = hlLines[hlIdx]
			}
			hlIdx++
			hl = injectBackground(hl, colors.AddBg)
			gutter := colors.AddBg + colors.AddFg +
				padNum(gutterWidth) + padNum(gutterWidth, dl.NewLineNo) +
				" " + "\033[1m" + "+" + "\033[0m" + colors.AddBg
			dl.Rendered = padWithBg(truncateLine(gutter+hl, width), width, colors.AddBg)

		case LineDel:
			hl := ""
			if hlIdx < len(hlLines) {
				hl = hlLines[hlIdx]
			}
			hlIdx++
			hl = injectBackground(hl, colors.DelBg)
			gutter := colors.DelBg + colors.DelFg +
				padNum(gutterWidth, dl.OldLineNo) + padNum(gutterWidth) +
				" " + "\033[1m" + "-" + "\033[0m" + colors.DelBg
			dl.Rendered = padWithBg(truncateLine(gutter+hl, width), width, colors.DelBg)

		case LineContext:
			hl := ""
			if hlIdx < len(hlLines) {
				hl = hlLines[hlIdx]
			}
			hlIdx++
			gutter := styles.DiffLineNum.Render(
				padNum(gutterWidth, dl.OldLineNo) + padNum(gutterWidth, dl.NewLineNo) + " ",
			)
			dl.Rendered = truncateLine(gutter+" "+hl, width)
		}
	}
}

// renderHunkLine renders a @@ hunk header with full-width background.
func renderHunkLine(content string, width int, colors styles.DiffColors) string {
	line := colors.HunkBg + colors.HunkFg + content
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
