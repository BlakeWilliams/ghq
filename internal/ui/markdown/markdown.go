// Package markdown provides comment body markdown rendering using glamour
// with chroma syntax highlighting for fenced code blocks. Output is suitable
// for embedding inside box-drawn comment threads in the diff viewer.
package markdown

import (
	"regexp"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
)

// commentStyle is a compact glamour style for rendering markdown inside
// comment thread boxes. Uses ANSI palette colors to derive from the
// terminal colorscheme.
var commentStyle = ansi.StyleConfig{
	Document: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{},
	},
	Heading: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color: strPtr("5"), // magenta
			Bold:  boolPtr(true),
		},
	},
	Emph: ansi.StylePrimitive{
		Italic: boolPtr(true),
	},
	Strong: ansi.StylePrimitive{
		Bold: boolPtr(true),
	},
	Strikethrough: ansi.StylePrimitive{
		CrossedOut: boolPtr(true),
	},
	Code: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color:  strPtr("6"), // cyan
			Prefix: "`",
			Suffix: "`",
		},
	},
	// CodeBlock is intentionally left default — we replace glamour's code
	// block output with chroma-highlighted code in post-processing.
	CodeBlock: ansi.StyleCodeBlock{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: strPtr("8"),
			},
			Margin: uintPtr(1),
		},
	},
	BlockQuote: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color:  strPtr("8"),
			Italic: boolPtr(true),
		},
		Indent:      uintPtr(1),
		IndentToken: strPtr("│ "),
	},
	List: ansi.StyleList{
		StyleBlock: ansi.StyleBlock{
			Indent: uintPtr(2),
		},
		LevelIndent: 2,
	},
	Item: ansi.StylePrimitive{
		BlockPrefix: "• ",
	},
	Enumeration: ansi.StylePrimitive{
		BlockPrefix: ". ",
	},
	Link: ansi.StylePrimitive{
		Format: "{{/*hidden*/}}",
	},
	LinkText: ansi.StylePrimitive{
		Color:     strPtr("4"), // blue
		Underline: boolPtr(true),
	},
}

func boolPtr(b bool) *bool  { return &b }
func strPtr(s string) *string { return &s }
func uintPtr(u uint) *uint  { return &u }

// reFence matches a fenced code block opening: optional indent + ``` or ~~~
// followed by an optional language identifier.
var reFence = regexp.MustCompile("^[ \t]*(```|~~~)(\\w*)")

// Renderer renders markdown comment bodies for embedding in diff comment boxes.
type Renderer struct {
	chromaStyle *chroma.Style
}

// NewRenderer creates a Renderer. If chromaStyle is nil, chroma uses its
// default style for code block highlighting.
func NewRenderer(chromaStyle *chroma.Style) *Renderer {
	return &Renderer{chromaStyle: chromaStyle}
}

// SetChromaStyle updates the chroma style used for code block highlighting.
func (r *Renderer) SetChromaStyle(s *chroma.Style) {
	r.chromaStyle = s
}

// RenderBody is the function suitable for use as components.RenderBody.
// It renders the markdown body, highlights fenced code blocks with chroma,
// strips glamour's trailing padding, and re-injects bg after every ANSI reset.
func (r *Renderer) RenderBody(body string, width int, bg string) string {
	if body == "" || width <= 0 {
		return body
	}

	// Highlight fenced code blocks with chroma before glamour processes them.
	// We replace the code inside fences with chroma output so glamour just
	// passes it through.
	body, codeBlocks := extractCodeBlocks(body)

	// Close unclosed code fences so glamour doesn't break during streaming.
	body = CloseOpenFences(body)

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(commentStyle),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return body
	}
	rendered, err := renderer.Render(body)
	if err != nil {
		return body
	}

	// Re-inject chroma-highlighted code blocks.
	rendered = r.injectCodeBlocks(rendered, codeBlocks, bg)

	// Glamour pads every line to the wrap width with trailing spaces.
	// Strip that — the comment box renderer handles its own padding.
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}

	// Strip trailing empty lines — glamour adds margin after code blocks
	// which creates a blank row before the closing border.
	for len(lines) > 0 && strings.TrimSpace(stripANSI(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}

	rendered = strings.Join(lines, "\n")

	// Glamour emits both \033[0m and the short form \033[m as resets.
	// Normalize to \033[0m so our bg injection catches all of them.
	rendered = strings.ReplaceAll(rendered, "\033[m", "\033[0m")

	// Re-inject bg after every ANSI reset so the diff background is preserved.
	if bg != "" {
		rendered = strings.ReplaceAll(rendered, "\033[0m", "\033[0m"+bg)
	}

	return strings.TrimRight(rendered, "\n")
}

// codeBlock holds a single extracted fenced code block.
type codeBlock struct {
	lang string
	code string
}

// placeholder is used to mark where code blocks were extracted.
const placeholder = "\x00CODEBLOCK\x00"

// extractCodeBlocks pulls fenced code blocks out of the markdown body,
// replacing them with placeholders. This lets us highlight the code with
// chroma independently and re-inject it after glamour runs.
//
// Glamour has built-in chroma support via its style config, but we bypass it
// so we can re-inject the diff background color through ANSI resets and
// control the chroma formatter/style dynamically at runtime.
func extractCodeBlocks(body string) (string, []codeBlock) {
	var blocks []codeBlock
	var out strings.Builder
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) {
		m := reFence.FindStringSubmatch(lines[i])
		if m == nil {
			out.WriteString(lines[i])
			out.WriteString("\n")
			i++
			continue
		}
		fence := m[1]
		lang := m[2]
		i++ // skip opening fence
		var code strings.Builder
		for i < len(lines) {
			if strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), fence) {
				i++ // skip closing fence
				break
			}
			code.WriteString(lines[i])
			code.WriteString("\n")
			i++
		}
		blocks = append(blocks, codeBlock{lang: lang, code: expandTabs(code.String(), 4)})
		// Write a fenced block with the placeholder as content so glamour
		// still sees a code block structure and applies its margins.
		out.WriteString("```\n")
		out.WriteString(placeholder + "\n")
		out.WriteString("```\n")
	}
	return out.String(), blocks
}

// injectCodeBlocks replaces placeholder lines in the glamour output with
// chroma-highlighted code.
func (r *Renderer) injectCodeBlocks(rendered string, blocks []codeBlock, bg string) string {
	if len(blocks) == 0 {
		return rendered
	}
	blockIdx := 0
	lines := strings.Split(rendered, "\n")
	var out []string
	for _, line := range lines {
		plain := stripANSI(strings.TrimSpace(line))
		if plain == placeholder && blockIdx < len(blocks) {
			block := blocks[blockIdx]
			blockIdx++
			highlighted := r.highlightCode(block.code, block.lang, bg)
			// Indent highlighted lines to match glamour's code block margin.
			for _, hl := range strings.Split(strings.TrimRight(highlighted, "\n"), "\n") {
				out = append(out, " "+hl)
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// highlightCode runs chroma syntax highlighting on a code block.
func (r *Renderer) highlightCode(code, lang, bg string) string {
	var lexer chroma.Lexer
	if lang != "" {
		lexer = lexers.Get(lang)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	formatter := formatters.Get("terminal16m")
	style := r.chromaStyle
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

// stripANSI removes all ANSI escape sequences from a string.
func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\033' {
			i++
			if i < len(s) && (s[i] == '[' || s[i] == ']') {
				i++
				for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
					i++
				}
				if i < len(s) {
					i++
				}
			}
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// expandTabs replaces tab characters with spaces. Each tab advances to the
// next multiple of tabWidth columns, matching terminal tab-stop behavior.
// This is necessary because lipgloss.Width counts tabs as 0-width but
// terminals render them as 8-column tab stops, causing width mismatches.
func expandTabs(s string, tabWidth int) string {
	var out strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			spaces := tabWidth - (col % tabWidth)
			out.WriteString(strings.Repeat(" ", spaces))
			col += spaces
		} else if r == '\n' {
			out.WriteRune(r)
			col = 0
		} else {
			out.WriteRune(r)
			col++
		}
	}
	return out.String()
}

// CloseOpenFences appends a closing fence if the body has an unclosed
// fenced code block. Scans line-by-line for opening/closing markers
// (``` or ~~~) anchored at the start of a line.
func CloseOpenFences(body string) string {
	open := false
	var fence string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if !open {
				open = true
				fence = trimmed[:3]
			} else if strings.HasPrefix(trimmed, fence) {
				open = false
			}
		}
	}
	if open {
		return body + "\n" + fence
	}
	return body
}
