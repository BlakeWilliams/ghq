package components

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/blakewilliams/gg/internal/review/comments"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeRenderComment(author string, body string) RenderComment {
	return RenderComment{
		ID:        1,
		Author:    author,
		CreatedAt: time.Now().Add(-2 * time.Hour),
		Blocks:    []comments.ContentBlock{comments.TextBlock{Text: body}},
	}
}

func TestCommentPanel_View_SingleComment(t *testing.T) {
	p := &CommentPanel{
		Comments: []RenderComment{
			makeRenderComment("octocat", "This is a test comment"),
		},
		FilePath: "internal/auth/handler.go",
		Side:     "RIGHT",
		Line:     42,
		Width:    60,
		Colors:   testColors(),
	}

	view := p.View()
	require.NotEmpty(t, view)

	// Check structural content (strip ANSI for text assertions)
	stripped := stripANSI(view)
	assert.Contains(t, stripped, "@octocat")
	assert.Contains(t, stripped, "2h ago")
	assert.Contains(t, stripped, "This is a test comment")
}

func TestCommentPanel_View_MultipleComments(t *testing.T) {
	p := &CommentPanel{
		Comments: []RenderComment{
			makeRenderComment("octocat", "First comment"),
			makeRenderComment("blake", "Second comment"),
			makeRenderComment("copilot", "Third comment"),
		},
		FilePath: "main.go",
		Side:     "RIGHT",
		Line:     10,
		Width:    80,
		Colors:   testColors(),
	}

	view := p.View()
	stripped := stripANSI(view)

	assert.Contains(t, stripped, "@octocat")
	assert.Contains(t, stripped, "@blake")
	assert.Contains(t, stripped, "@copilot")
	assert.Contains(t, stripped, "First comment")
	assert.Contains(t, stripped, "Third comment")

	// Should have separators between comments
	lines := strings.Split(view, "\n")
	sepCount := 0
	for _, line := range lines {
		if strings.Contains(stripANSI(line), "───") {
			sepCount++
		}
	}
	// 2 between-comment separators (no header separator anymore)
	assert.GreaterOrEqual(t, sepCount, 2, "should have separators between comments")
}

func TestCommentPanel_View_Resolved(t *testing.T) {
	p := &CommentPanel{
		Comments: []RenderComment{
			makeRenderComment("octocat", "Fix this"),
		},
		FilePath: "main.go",
		Side:     "RIGHT",
		Line:     5,
		Width:    60,
		Resolved: true,
		Colors:   testColors(),
	}

	view := p.View()
	stripped := stripANSI(view)
	assert.Contains(t, stripped, "Resolved")
}

func TestCommentPanel_View_ZeroWidth(t *testing.T) {
	p := &CommentPanel{
		Comments: []RenderComment{makeRenderComment("a", "b")},
		Width:    0,
	}
	assert.Empty(t, p.View())
}

func TestCommentPanel_View_NarrowWidth(t *testing.T) {
	p := &CommentPanel{
		Comments: []RenderComment{makeRenderComment("octocat", "Hello world")},
		FilePath: "main.go",
		Side:     "RIGHT",
		Line:     1,
		Width:    20,
		Colors:   testColors(),
	}

	view := p.View()
	require.NotEmpty(t, view)

	// Every line should be <= width
	for _, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
		w := lipgloss.Width(line)
		assert.LessOrEqual(t, w, 20, "line should not exceed panel width: %q", stripANSI(line))
	}
}

func TestCommentPanel_View_WithMarkdown(t *testing.T) {
	called := false
	renderBody := func(body string, width int, bg string) string {
		called = true
		return "**rendered:** " + body
	}

	p := &CommentPanel{
		Comments: []RenderComment{
			makeRenderComment("octocat", "Use `fmt.Errorf` here"),
		},
		FilePath:   "main.go",
		Side:       "RIGHT",
		Line:       1,
		Width:      60,
		Colors:     testColors(),
		RenderBody: renderBody,
	}

	p.View()
	assert.True(t, called, "RenderBody should be called for TextBlocks")
}

func TestCommentPanel_View_WithToolGroup(t *testing.T) {
	p := &CommentPanel{
		Comments: []RenderComment{
			{
				ID:        1,
				Author:    "copilot",
				CreatedAt: time.Now(),
				Blocks: []comments.ContentBlock{
					comments.TextBlock{Text: "Looking at the code..."},
					comments.ToolGroupBlock{
						Label: "Analyzing",
						Tools: []comments.ToolCall{
							{Name: "read_file", Status: "done", Arguments: "main.go"},
							{Name: "search", Status: "running", Arguments: "ToolCall"},
						},
					},
				},
			},
		},
		FilePath: "main.go",
		Side:     "RIGHT",
		Line:     1,
		Width:    60,
		Colors:   testColors(),
	}

	view := p.View()
	stripped := stripANSI(view)
	assert.Contains(t, stripped, "Analyzing")
	assert.Contains(t, stripped, "read_file")
	assert.Contains(t, stripped, "main.go")
	assert.Contains(t, stripped, "search")
	assert.Contains(t, stripped, "ToolCall")
}

func TestCommentPanel_ContentLines(t *testing.T) {
	p := &CommentPanel{
		Comments: []RenderComment{
			makeRenderComment("octocat", "Hello"),
		},
		FilePath: "main.go",
		Side:     "RIGHT",
		Line:     1,
		Width:    60,
		Colors:   testColors(),
	}

	lines := p.ContentLines()
	assert.Greater(t, lines, 0, "should have positive line count")
}

func TestCommentPanel_FallbackView(t *testing.T) {
	p := &CommentPanel{
		Comments: []RenderComment{
			makeRenderComment("octocat", "Fix this bug"),
		},
		FilePath: "main.go",
		Side:     "RIGHT",
		Line:     10,
		Width:    60,
		Colors:   testColors(),
	}

	contextLines := []string{
		"  9 │ func main() {",
		" 10 │+    return err",
		" 11 │ }",
	}

	view := p.RenderFallbackView(contextLines, 30)
	stripped := stripANSI(view)

	// Should contain both diff context and comments
	assert.Contains(t, stripped, "func main()")
	assert.Contains(t, stripped, "return err")
	assert.Contains(t, stripped, "@octocat")
	assert.Contains(t, stripped, "Fix this bug")
}

func TestCommentPanel_View_WithDiffContext(t *testing.T) {
	p := &CommentPanel{
		Comments: []RenderComment{
			makeRenderComment("octocat", "Bug here"),
		},
		FilePath: "main.go",
		Side:     "RIGHT",
		Line:     10,
		Width:    60,
		DiffContext: []string{
			"  9 │ func main() {",
			" 10 │+    return err",
		},
		Colors: testColors(),
	}

	view := p.View()
	stripped := stripANSI(view)

	// Diff context should appear before comments
	assert.Contains(t, stripped, "func main()")
	assert.Contains(t, stripped, "return err")
	assert.Contains(t, stripped, "@octocat")
	assert.Contains(t, stripped, "Bug here")

	// Context should come before the comment
	ctxIdx := strings.Index(stripped, "func main()")
	commentIdx := strings.Index(stripped, "Bug here")
	assert.Less(t, ctxIdx, commentIdx, "diff context should appear before comments")
}

func TestCommentPanel_View_ReplyGitHubMode(t *testing.T) {
	p := &CommentPanel{
		Comments:  []RenderComment{makeRenderComment("octocat", "Fix this")},
		FilePath:  "main.go",
		Side:      "RIGHT",
		Line:      5,
		Width:     60,
		Colors:    testColors(),
		ReplyView: "draft reply text",
		ReplyMode: ReplyModeGitHub,
		HelpMode:  true,
	}

	view := p.View()
	stripped := stripANSI(view)

	assert.Contains(t, stripped, "Replying on GitHub")
	assert.Contains(t, stripped, "shift+tab")
	assert.Contains(t, stripped, "draft reply text")

	// Every line should be <= width
	for _, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
		w := lipgloss.Width(line)
		assert.LessOrEqual(t, w, 60, "line should not exceed panel width: %q", stripANSI(line))
	}
}

func TestCommentPanel_View_ReplyCopilotMode(t *testing.T) {
	p := &CommentPanel{
		Comments:  []RenderComment{makeRenderComment("octocat", "Fix this")},
		FilePath:  "main.go",
		Side:      "RIGHT",
		Line:      5,
		Width:     60,
		Colors:    testColors(),
		ReplyView: "ask copilot something",
		ReplyMode: ReplyModeCopilot,
		HelpMode:  true,
	}

	view := p.View()
	stripped := stripANSI(view)

	assert.Contains(t, stripped, "Asking Copilot")
	assert.Contains(t, stripped, "shift+tab")
	assert.Contains(t, stripped, "ask copilot something")
}

func TestCommentPanel_View_ReplyNoHelpMode(t *testing.T) {
	p := &CommentPanel{
		Comments:  []RenderComment{makeRenderComment("octocat", "Fix this")},
		FilePath:  "main.go",
		Side:      "RIGHT",
		Line:      5,
		Width:     60,
		Colors:    testColors(),
		ReplyView: "reply text",
		ReplyMode: ReplyModeGitHub,
		HelpMode:  false,
	}

	view := p.View()
	stripped := stripANSI(view)

	assert.Contains(t, stripped, "Replying on GitHub")
	assert.NotContains(t, stripped, "shift+tab")
}

// --- helpers ---

func stripANSI(s string) string {
	// Simple ANSI stripper for tests
	result := strings.Builder{}
	inEscape := false
	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		result.WriteRune(r)
	}
	return result.String()
}
