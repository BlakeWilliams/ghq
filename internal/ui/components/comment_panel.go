package components

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/blakewilliams/gg/internal/review/comments"
	"github.com/blakewilliams/gg/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// ReplyMode controls the visual style and label of the reply textarea border.
type ReplyMode int

const (
	ReplyModeGitHub ReplyMode = iota
	ReplyModeCopilot
)

// CommentPanel renders a single comment thread for display in a side panel
// or fallback full view. The caller is responsible for placing the output
// in a viewport for scrolling.
type CommentPanel struct {
	// Thread data
	Comments []RenderComment
	FilePath string
	Side     string
	Line     int
	Resolved bool

	// DiffContext holds pre-rendered diff lines around the commented line.
	// Displayed at the top of the panel to give context.
	DiffContext []string

	// Dimensions
	Width int

	// Rendering
	RenderBody  func(body string, width int, bg string) string
	Colors      styles.DiffColors
	ChromeColor color.Color

	// ReplyView, when non-empty, is rendered at the bottom of the panel
	// as a textarea for composing a reply.
	ReplyView string
	ReplyMode ReplyMode
	HelpMode  bool

	// CommentOffsets is populated by View() with the starting line number
	// (0-indexed) of each comment in the rendered output.
	CommentOffsets []int
}

// View renders the panel content as a string.
// Each line is padded to p.Width. The output is suitable for embedding
// in a viewport or joining with a diff view.
func (p *CommentPanel) View() string {
	if p.Width <= 0 {
		return ""
	}
	w := p.Width
	contentW := w - 2 // 1 char padding on each side

	if contentW < 10 {
		contentW = 10
	}

	var b strings.Builder
	lineCount := 0 // tracks current line number
	writeLine := func(s string) {
		b.WriteString(s)
		b.WriteString("\n")
		lineCount++
	}

	// Chrome styles
	var chromeClr color.Color = lipgloss.BrightBlack
	if p.ChromeColor != nil {
		chromeClr = p.ChromeColor
	}
	chrome := lipgloss.NewStyle().Foreground(chromeClr)
	dim := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)

	// Filename header with subtle background
	if p.FilePath != "" {
		nameStyle := lipgloss.NewStyle().Bold(true)
		content := " " + nameStyle.Render(p.FilePath)
		if p.Colors.SelectColor != nil {
			line := lipgloss.NewStyle().Background(p.Colors.SelectColor).Width(w).Render(content)
			writeLine(line)
		} else {
			writeLine(p.padLine(content, w))
		}
	}

	// Diff context lines at the top (if provided)
	if len(p.DiffContext) > 0 {
		for _, line := range p.DiffContext {
			writeLine(p.padLine(line, w))
		}
		sep := chrome.Render(strings.Repeat("─", w))
		writeLine(ansi.Truncate(sep, w, ""))
	}

	// Blank line before first comment
	writeLine(p.emptyLine(w))

	// Resolved indicator
	if p.Resolved {
		resolvedLabel := " " + lipgloss.NewStyle().Foreground(lipgloss.Cyan).Render("✓ Resolved")
		writeLine(p.padLine(resolvedLabel, w))
		writeLine(p.emptyLine(w))
	}

	// Render each comment, recording line offsets.
	p.CommentOffsets = make([]int, len(p.Comments))
	for i, c := range p.Comments {
		p.CommentOffsets[i] = lineCount

		// Comment header: " @author · 2h ago"
		author := ColoredAuthor(c.Author)
		time := dim.Render(" · " + relativeTime(c.CreatedAt))
		commentHeader := " " + author + time
		writeLine(p.padLine(commentHeader, w))

		// Comment body — render each content block
		for _, block := range c.Blocks {
			switch blk := block.(type) {
			case comments.TextBlock:
				body := blk.Text
				if p.RenderBody != nil {
					body = p.RenderBody(body, contentW, "")
				}
				bodyLines := strings.Split(body, "\n")
				for _, line := range bodyLines {
					padded := " " + line
					writeLine(p.padLine(padded, w))
				}
			case comments.ToolGroupBlock:
				// Render tool calls as a compact summary.
				// Account for lines: top border + tool rows + bottom border.
				p.renderToolGroupSummary(&b, blk, contentW, w)
				lineCount += 2 + len(blk.Tools)
			}
		}

		// Separator between comments (not after the last)
		if i < len(p.Comments)-1 {
			writeLine(p.emptyLine(w))
			sepLine := " " + chrome.Render(strings.Repeat("─", contentW))
			writeLine(p.padLine(sepLine, w))
			writeLine(p.emptyLine(w))
		}
	}

	// Trailing blank line
	writeLine(p.emptyLine(w))

	// Reply textarea (when composing)
	if p.ReplyView != "" {
		var borderClr color.Color
		var modeText string
		switch p.ReplyMode {
		case ReplyModeCopilot:
			borderClr = p.Colors.PaletteCyan
			modeText = "Asking Copilot"
		default:
			borderClr = lipgloss.BrightBlack
			modeText = "Replying on GitHub"
		}
		if borderClr == nil {
			borderClr = lipgloss.BrightBlack
		}
		borderStyle := lipgloss.NewStyle().Foreground(borderClr)

		// Top border: ── Asking Copilot shift+tab ─────
		labelPart := borderStyle.Render(modeText)
		if p.HelpMode {
			labelPart += " " + dim.Render("shift+tab")
		}
		topContent := borderStyle.Render("──") + " " + labelPart + " "
		topContentW := lipgloss.Width(topContent)
		fillW := w - topContentW
		if fillW < 0 {
			fillW = 0
		}
		topBorder := topContent + borderStyle.Render(strings.Repeat("─", fillW))
		b.WriteString(ansi.Truncate(topBorder, w, ""))
		b.WriteString("\n")

		replyLines := strings.Split(p.ReplyView, "\n")
		for _, line := range replyLines {
			b.WriteString(p.padLine(" "+line, w))
			b.WriteString("\n")
		}

		// Bottom border
		botBorder := borderStyle.Render(strings.Repeat("─", w))
		b.WriteString(ansi.Truncate(botBorder, w, ""))
		b.WriteString("\n")
	}

	return b.String()
}

// ContentLines returns the number of visual lines the panel content occupies.
func (p *CommentPanel) ContentLines() int {
	content := p.View()
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n")
}

// padLine pads or truncates a line to exactly width characters.
func (p *CommentPanel) padLine(s string, width int) string {
	visW := lipgloss.Width(s)
	if visW >= width {
		return ansi.Truncate(s, width, "")
	}
	return s + strings.Repeat(" ", width-visW)
}

// emptyLine returns a line of spaces with width characters.
func (p *CommentPanel) emptyLine(width int) string {
	return strings.Repeat(" ", width)
}

// renderToolGroupSummary renders tool calls as a rounded-border sub-box
// within a comment body, matching the style used in the diff view.
func (p *CommentPanel) renderToolGroupSummary(b *strings.Builder, group comments.ToolGroupBlock, contentW, totalW int) {
	if len(group.Tools) == 0 {
		return
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)

	var borderStyle lipgloss.Style
	switch group.ToolGroupStatus() {
	case "running":
		borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	case "failed":
		borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Red)
	default:
		borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Green)
	}

	subBoxW := contentW
	subInnerW := subBoxW - 4 // "│ " + " │"
	if subInnerW < 6 {
		subInnerW = 6
	}

	// Top border: ╭ Label ───╮  or  ╭──────────╮
	var topLine string
	if group.Label != "" {
		labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Yellow)
		label := " " + group.Label + " "
		labelW := lipgloss.Width(label)
		topFill := subBoxW - 2 - labelW
		if topFill < 0 {
			topFill = 0
		}
		topLine = borderStyle.Render("╭") + labelStyle.Render(label) + borderStyle.Render(strings.Repeat("─", topFill)+"╮")
	} else {
		topFill := subBoxW - 2
		if topFill < 0 {
			topFill = 0
		}
		topLine = borderStyle.Render("╭" + strings.Repeat("─", topFill) + "╮")
	}
	b.WriteString(p.padLine(" "+topLine, totalW))
	b.WriteString("\n")

	// Tool rows: │ ● name │
	for _, tc := range group.Tools {
		name := tc.Name
		if name == "" {
			name = "unknown"
		}
		var statusIcon string
		switch tc.Status {
		case "running":
			statusIcon = lipgloss.NewStyle().Foreground(lipgloss.Yellow).Render("●")
		case "failed":
			statusIcon = lipgloss.NewStyle().Foreground(lipgloss.Red).Render("✕")
		default:
			statusIcon = lipgloss.NewStyle().Foreground(lipgloss.Green).Render("●")
		}

		nameW := lipgloss.Width(name)
		maxNameW := subInnerW - 2
		if nameW > maxNameW {
			name = ansi.Truncate(name, maxNameW-1, "…")
			nameW = maxNameW
		}

		var rowContent string
		if tc.Arguments != "" {
			argsSpace := subInnerW - 2 - nameW - 1 // "● " prefix, then 1 space before args
			if argsSpace > 3 {
				args := tc.Arguments
				if lipgloss.Width(args) > argsSpace {
					args = ansi.Truncate(args, argsSpace-1, "…")
				}
				rowContent = statusIcon + " " + dim.Render(name) + " " + dim.Render(args)
			} else {
				rowContent = statusIcon + " " + dim.Render(name)
			}
		} else {
			rowContent = statusIcon + " " + dim.Render(name)
		}
		rowVisW := lipgloss.Width(rowContent)
		rowPad := subInnerW - rowVisW
		if rowPad < 0 {
			rowPad = 0
		}
		row := borderStyle.Render("│") + " " + rowContent + strings.Repeat(" ", rowPad) + " " + borderStyle.Render("│")
		b.WriteString(p.padLine(" "+row, totalW))
		b.WriteString("\n")
	}

	// Bottom border: ╰───────╯
	botFill := subBoxW - 2
	if botFill < 0 {
		botFill = 0
	}
	botLine := borderStyle.Render("╰" + strings.Repeat("─", botFill) + "╯")
	b.WriteString(p.padLine(" "+botLine, totalW))
	b.WriteString("\n")
}

// RenderFallbackView renders the panel content with diff context lines at the
// top, suitable for replacing the right pane when the terminal is too narrow
// for a side panel.
func (p *CommentPanel) RenderFallbackView(contextLines []string, height int) string {
	w := p.Width
	if w <= 0 {
		return ""
	}

	var chromeClr color.Color = lipgloss.BrightBlack
	if p.ChromeColor != nil {
		chromeClr = p.ChromeColor
	}
	chrome := lipgloss.NewStyle().Foreground(chromeClr)

	var b strings.Builder

	// Diff context (3-5 lines)
	for _, line := range contextLines {
		b.WriteString(p.padLine(line, w))
		b.WriteString("\n")
	}

	// Separator between diff context and comments
	sep := chrome.Render(strings.Repeat("─", w))
	b.WriteString(ansi.Truncate(sep, w, ""))
	b.WriteString("\n")

	// Comment panel content
	b.WriteString(p.View())

	return b.String()
}
