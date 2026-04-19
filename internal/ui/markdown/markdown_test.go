package markdown

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/components"
	"github.com/blakewilliams/ghq/internal/ui/styles"
)

func testColors() styles.DiffColors {
	return styles.DiffColors{
		AddBg:             "\033[48;5;22m",
		BorderFg:          "\033[90m",
		HighlightBorderFg: "\033[33m",
	}
}

// TestRenderBody_CodeBlock verifies that code block lines fit within width.
func TestRenderBody_CodeBlock(t *testing.T) {
	r := NewRenderer(nil)
	bg := "\033[48;5;22m"
	width := 60

	body := "Here is the fix:\n```go\nfunc main() {\n    fmt.Println(\"hello\")\n}\n```\nDone."

	result := r.RenderBody(body, width, bg)

	for i, line := range strings.Split(result, "\n") {
		visW := lipgloss.Width(line)
		if visW > width {
			t.Errorf("line %d exceeds width %d (got %d): %q", i, width, visW, stripANSI(line))
		}
	}
}

// TestRenderBody_NoPadding verifies that glamour's trailing whitespace
// padding is stripped — the box renderer handles padding, not RenderBody.
func TestRenderBody_NoPadding(t *testing.T) {
	r := NewRenderer(nil)
	result := r.RenderBody("Short.", 80, "")

	for i, line := range strings.Split(result, "\n") {
		if strings.HasSuffix(line, "  ") {
			t.Errorf("line %d has trailing padding: %q", i, stripANSI(line))
		}
	}
}

// TestRenderBody_NoTrailingBlanks verifies that trailing empty lines from
// glamour's code block margins are stripped so the closing border isn't
// pushed to a new line.
func TestRenderBody_NoTrailingBlanks(t *testing.T) {
	r := NewRenderer(nil)
	body := "Code:\n```go\nx := 1\n```"

	result := r.RenderBody(body, 60, "")
	lines := strings.Split(result, "\n")

	// Last line should have visible content, not be blank.
	last := lines[len(lines)-1]
	if strings.TrimSpace(stripANSI(last)) == "" {
		t.Errorf("last line is blank (would push border): %q", last)
	}
}

// TestRenderBody_ResetNormalization verifies that both \033[m and
// \033[0m resets get the bg color re-injected.
func TestRenderBody_ResetNormalization(t *testing.T) {
	r := NewRenderer(nil)
	bg := "\033[48;5;22m"
	body := "Hello **bold** and `code`."

	result := r.RenderBody(body, 60, bg)

	// After normalization, there should be no bare \033[m (only \033[0m+bg).
	if strings.Contains(result, "\033[m") && !strings.Contains(result, "\033[0m") {
		t.Error("found bare \\033[m without normalization to \\033[0m")
	}

	// Every \033[0m should be followed by bg.
	resetCount := strings.Count(result, "\033[0m")
	resetBgCount := strings.Count(result, "\033[0m"+bg)
	if resetCount > 0 && resetBgCount != resetCount {
		t.Errorf("not all resets have bg: %d resets, %d with bg", resetCount, resetBgCount)
	}
}

// TestRenderBody_InlineCode verifies inline code renders correctly.
func TestRenderBody_InlineCode(t *testing.T) {
	r := NewRenderer(nil)
	body := "Use `fmt.Println` to print and `os.Exit(1)` to quit."

	result := r.RenderBody(body, 60, "\033[48;5;22m")
	plain := stripANSI(result)

	if !strings.Contains(plain, "fmt.Println") {
		t.Errorf("expected inline code in output: %q", plain)
	}
}

// TestRenderBody_NoBg verifies that passing empty bg still works.
func TestRenderBody_NoBg(t *testing.T) {
	r := NewRenderer(nil)
	result := r.RenderBody("Some **bold** and `code`.", 60, "")

	if result == "" {
		t.Error("expected non-empty output")
	}
	plain := stripANSI(result)
	if !strings.Contains(plain, "bold") || !strings.Contains(plain, "code") {
		t.Errorf("missing content: %q", plain)
	}
}

// TestRenderBody_StreamingPartialFence verifies that a body with an
// unclosed code fence doesn't break rendering.
func TestRenderBody_StreamingPartialFence(t *testing.T) {
	r := NewRenderer(nil)
	body := "Here:\n```go\nfunc hello() {"

	result := r.RenderBody(body, 60, "\033[48;5;22m")
	if result == "" {
		t.Error("expected non-empty output for partial fence")
	}
}

// TestCloseOpenFences verifies fence balancing for streaming content.
func TestCloseOpenFences(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect bool // true if fence should be appended
	}{
		{"no fences", "hello world", false},
		{"balanced", "```go\ncode\n```", false},
		{"unclosed backtick", "```\ncode", true},
		{"unclosed tilde", "~~~\ncode", true},
		{"triple balanced", "```\na\n```\n```\nb\n```", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CloseOpenFences(tt.input)
			hasSuffix := len(result) > len(tt.input)
			if hasSuffix != tt.expect {
				t.Errorf("got modified=%v, want=%v\ninput:  %q\nresult: %q",
					hasSuffix, tt.expect, tt.input, result)
			}
		})
	}
}

// TestRenderBody_ChromaHighlighting verifies that code blocks get syntax
// highlighting (contain ANSI color codes beyond just the dim \033[90m).
func TestRenderBody_ChromaHighlighting(t *testing.T) {
	r := NewRenderer(nil)
	body := "```go\nfunc main() {\n    fmt.Println(\"hello\")\n}\n```"

	result := r.RenderBody(body, 60, "")

	// Chroma should produce 38;2;... (truecolor) or 38;5;... (256-color) codes.
	if !strings.Contains(result, "\033[38;2;") && !strings.Contains(result, "\033[38;5;") {
		t.Errorf("expected chroma syntax highlighting (truecolor/256) in output, got: %q", result)
	}
}

// TestCommentThread_WithMarkdownRenderer renders a comment thread through the
// full pipeline using the real markdown Renderer. Verifies every output line
// fits within the target width and has matching │ borders.
func TestCommentThread_WithMarkdownRenderer(t *testing.T) {
	r := NewRenderer(nil)
	width := 80
	colors := testColors()
	colW := 4

	body := "Here is the fix:\n```go\nfunc main() {\n    fmt.Println(\"hello world\")\n}\n```\nLooks good!"

	thread := components.NewCommentThreadItem(
		0, "RIGHT", 1,
		components.ReviewCommentsToRender([]github.ReviewComment{
			{
				ID:   1,
				Body: body,
				User: github.User{Login: "copilot"},
			},
		}),
		components.LineAdd,
	)

	rc := components.RenderContext{
		Width:      width,
		Colors:     colors,
		ColW:       colW,
		RenderBody: r.RenderBody,
	}

	rendered := thread.Render(rc)
	lines := strings.Split(rendered, "\n")

	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(lines))
	}

	for i, line := range lines {
		if line == "" {
			continue
		}
		visW := lipgloss.Width(line)
		if visW > width {
			t.Errorf("line %d exceeds width %d (got %d): %q", i, width, visW, stripANSI(line))
		}
	}

	// Body lines should have matching │ borders.
	for i, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "│") {
			count := strings.Count(plain, "│")
			if count != 2 {
				t.Errorf("line %d: expected 2 │ borders, got %d: %q", i, count, plain)
			}
		}
	}

	// Closing border should be on the last non-empty line, not preceded
	// by a blank body line.
	lastNonEmpty := ""
	secondLast := ""
	for _, line := range lines {
		if strings.TrimSpace(stripANSI(line)) != "" {
			secondLast = lastNonEmpty
			lastNonEmpty = line
		}
	}
	plainLast := stripANSI(lastNonEmpty)
	if !strings.Contains(plainLast, "╰") {
		t.Errorf("last non-empty line missing ╰ border: %q", plainLast)
	}
	// The line before the closing border should not be blank inside the box.
	if secondLast != "" {
		plainSecond := stripANSI(secondLast)
		inner := strings.TrimLeft(plainSecond, " ")
		if strings.HasPrefix(inner, "│") {
			// Extract content between │ borders.
			parts := strings.SplitN(inner, "│", 3)
			if len(parts) >= 2 && strings.TrimSpace(parts[1]) == "" {
				t.Errorf("blank body line before closing border: %q", plainSecond)
			}
		}
	}
}

// TestCommentThread_NarrowWidth tests the full pipeline at a narrow width.
func TestCommentThread_NarrowWidth(t *testing.T) {
	r := NewRenderer(nil)
	width := 50
	colors := testColors()
	colW := 3

	body := "Check this:\n```\nsome_long_variable_name := something\n```\nFixed."

	thread := components.NewCommentThreadItem(
		0, "RIGHT", 1,
		components.ReviewCommentsToRender([]github.ReviewComment{
			{
				ID:   2,
				Body: body,
				User: github.User{Login: "copilot"},
			},
		}),
		components.LineAdd,
	)

	rc := components.RenderContext{
		Width:      width,
		Colors:     colors,
		ColW:       colW,
		RenderBody: r.RenderBody,
	}

	rendered := thread.Render(rc)
	for i, line := range strings.Split(rendered, "\n") {
		if line == "" {
			continue
		}
		visW := lipgloss.Width(line)
		if visW > width {
			t.Errorf("line %d exceeds width %d (got %d): %q", i, width, visW, stripANSI(line))
		}
	}
}

// TestCommentThread_TabIndentedCode tests Go code with tab indentation —
// the exact scenario that caused displaced borders in the real app.
// Tabs measure as 0-width in lipgloss but render as 8 columns in terminals.
func TestCommentThread_TabIndentedCode(t *testing.T) {
	r := NewRenderer(nil)
	colors := testColors()
	colW := 4

	// Use actual tab characters like Go code would have.
	body := "Consider:\n```go\ntype ReplyMsg struct {\n\tCommentID string\n\tDelta     string\n\tDone      bool\n}\n```\nDone."

	for _, width := range []int{80, 120, 200, 260} {
		thread := components.NewCommentThreadItem(
			0, "RIGHT", 1,
			components.ReviewCommentsToRender([]github.ReviewComment{
				{
					ID:   3,
					Body: body,
					User: github.User{Login: "copilot"},
				},
			}),
			components.LineAdd,
		)

		rc := components.RenderContext{
			Width:      width,
			Colors:     colors,
			ColW:       colW,
			RenderBody: r.RenderBody,
		}

		rendered := thread.Render(rc)
		lines := strings.Split(rendered, "\n")

		for i, line := range lines {
			if line == "" {
				continue
			}
			// No tabs should survive.
			if strings.Contains(line, "\t") {
				t.Errorf("w=%d line %d: tab character in output: %q", width, i, stripANSI(line))
			}
			// Every line must fit within width.
			visW := lipgloss.Width(line)
			if visW > width {
				t.Errorf("w=%d line %d: exceeds width %d (got %d): %q", width, i, width, visW, stripANSI(line))
			}
		}

		// Every body line (with │) must have exactly 2 borders.
		for i, line := range lines {
			plain := stripANSI(line)
			if strings.Contains(plain, "│") {
				count := strings.Count(plain, "│")
				if count != 2 {
					t.Errorf("w=%d line %d: expected 2 │ borders, got %d: %q", width, i, count, plain)
				}
			}
		}
	}
}
