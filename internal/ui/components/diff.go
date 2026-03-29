package components

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/styles"
)

// RenderDiffFile renders a single file's diff content.
// Lines are truncated to width to avoid expensive ANSI width scanning
// in the viewport.
func RenderDiffFile(f github.PullRequestFile, width int) string {
	var b strings.Builder

	header := fmt.Sprintf("%s  +%d -%d", f.Filename, f.Additions, f.Deletions)
	if f.Status == "renamed" && f.PreviousFilename != "" {
		header = fmt.Sprintf("%s → %s  +%d -%d", f.PreviousFilename, f.Filename, f.Additions, f.Deletions)
	}
	b.WriteString(truncateLine(styles.DiffFileHeader.Render(header), width))
	b.WriteString("\n")

	if f.Patch == "" {
		b.WriteString(styles.SubtitleStyle.Render("  (binary or empty)"))
		b.WriteString("\n")
		return b.String()
	}

	b.WriteString(renderPatch(f.Patch, f.Filename, width))
	return b.String()
}

// renderPatch highlights an entire patch at once instead of line-by-line,
// then applies diff styling to the highlighted output.
func truncateLine(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Truncate(s, width, "")
}

func renderPatch(patch, filename string, width int) string {
	lines := strings.Split(patch, "\n")

	// Separate code content from diff metadata so we can highlight
	// all code in one tokenize pass.
	type lineInfo struct {
		prefix byte // '+', '-', '@', or 0 for context
		code   string
	}

	infos := make([]lineInfo, len(lines))
	var codeBuilder strings.Builder

	for i, line := range lines {
		if line == "" {
			infos[i] = lineInfo{}
			codeBuilder.WriteString("\n")
			continue
		}
		switch {
		case strings.HasPrefix(line, "@@"):
			infos[i] = lineInfo{prefix: '@', code: line}
			codeBuilder.WriteString("\n") // placeholder to keep line count aligned
		case strings.HasPrefix(line, "+"):
			infos[i] = lineInfo{prefix: '+', code: line[1:]}
			codeBuilder.WriteString(line[1:])
			codeBuilder.WriteString("\n")
		case strings.HasPrefix(line, "-"):
			infos[i] = lineInfo{prefix: '-', code: line[1:]}
			codeBuilder.WriteString(line[1:])
			codeBuilder.WriteString("\n")
		default:
			if len(line) > 0 && line[0] == ' ' {
				infos[i] = lineInfo{code: line[1:]}
				codeBuilder.WriteString(line[1:])
			} else {
				infos[i] = lineInfo{code: line}
				codeBuilder.WriteString(line)
			}
			codeBuilder.WriteString("\n")
		}
	}

	// Highlight all code in one pass.
	highlighted := highlightBlock(codeBuilder.String(), filename)
	highlightedLines := strings.Split(highlighted, "\n")

	// Reassemble with diff styling, truncating each line to viewport width.
	var b strings.Builder
	hlIdx := 0
	for _, info := range infos {
		var line string
		switch info.prefix {
		case '@':
			line = styles.DiffHunk.Render(info.code)
			hlIdx++
		case '+':
			hl := ""
			if hlIdx < len(highlightedLines) {
				hl = highlightedLines[hlIdx]
			}
			hlIdx++
			line = styles.DiffAdd.Render("+") + hl
		case '-':
			hl := ""
			if hlIdx < len(highlightedLines) {
				hl = highlightedLines[hlIdx]
			}
			hlIdx++
			line = styles.DiffDel.Render("-") + hl
		default:
			hl := ""
			if hlIdx < len(highlightedLines) {
				hl = highlightedLines[hlIdx]
			}
			hlIdx++
			line = " " + hl
		}
		b.WriteString(truncateLine(line, width))
		b.WriteString("\n")
	}

	return b.String()
}

// highlightBlock tokenizes and highlights an entire block of code at once.
func highlightBlock(code, filename string) string {
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	formatter := formatters.Get("terminal16m")
	style := chromastyles.Get("base16-snazzy")

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
