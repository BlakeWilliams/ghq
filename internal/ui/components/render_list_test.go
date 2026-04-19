package components

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/review/comments"
	"github.com/blakewilliams/ghq/internal/ui/styles"
)

func testColors() styles.DiffColors {
	return styles.DiffColors{
		BorderFg:          "\033[90m",
		HighlightBorderFg: "\033[33m",
	}
}

func intPtr(i int) *int { return &i }

func makeComment(id int, body, path string, line int, side string, replyTo *int) github.ReviewComment {
	return github.ReviewComment{
		ID:           id,
		Body:         body,
		Path:         path,
		Line:         &line,
		OriginalLine: &line,
		Side:         side,
		InReplyToID:  replyTo,
		User:         github.User{Login: "testuser"},
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
}

func makeDiffLineItem(idx int, lt LineType, content string) *DiffLineItem {
	dl := &DiffLine{
		Type:      lt,
		NewLineNo: idx + 1,
		OldLineNo: idx + 1,
		Content:   content,
		Rendered:  content, // simplified: no ANSI for tests
	}
	return NewDiffLineItem(idx, dl)
}

func makeThreadItem(diffLineIdx int, side string, line int, bodies ...string) *CommentThreadItem {
	var comments []RenderComment
	for _, body := range bodies {
		comments = append(comments, ReviewCommentToRender(github.ReviewComment{
			User: github.User{Login: "testuser"},
			Body: body,
		}))
	}
	return NewCommentThreadItem(diffLineIdx, side, line, comments, LineAdd)
}

func testRC() RenderContext {
	return RenderContext{
		Width:  80,
		Colors: testColors(),
		ColW:   4,
	}
}

func TestFileRenderList_InsertAfterDiffLine(t *testing.T) {
	list := &FileRenderList{
		Items: []Renderable{
			makeDiffLineItem(0, LineAdd, "line 0"),
			makeDiffLineItem(1, LineAdd, "line 1"),
			makeDiffLineItem(2, LineAdd, "line 2"),
		},
	}

	thread := makeThreadItem(1, "RIGHT", 2, "nice code")
	list.InsertAfterDiffLine(1, thread)

	if len(list.Items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(list.Items))
	}
	s, l := list.Items[2].ThreadKey()
	if s != "RIGHT" || l != 2 {
		t.Errorf("thread has wrong position: side=%s line=%d", s, l)
	}
	// Diff line 2 should now be at index 3
	if !list.Items[3].IsDiffLine() || list.Items[3].DiffIdx() != 2 {
		t.Errorf("expected item 3 to be diff line 2")
	}
	if !list.dirty {
		t.Error("expected dirty flag to be set")
	}
}

func TestFileRenderList_InsertSkipsExistingThreads(t *testing.T) {
	existing := makeThreadItem(1, "RIGHT", 2, "existing comment")
	list := &FileRenderList{
		Items: []Renderable{
			makeDiffLineItem(0, LineAdd, "line 0"),
			makeDiffLineItem(1, LineAdd, "line 1"),
			existing,
			makeDiffLineItem(2, LineAdd, "line 2"),
		},
	}

	newThread := makeThreadItem(1, "LEFT", 2, "left side comment")
	list.InsertAfterDiffLine(1, newThread)

	if len(list.Items) != 5 {
		t.Fatalf("expected 5 items, got %d", len(list.Items))
	}
	s, _ := list.Items[3].ThreadKey()
	if s != "LEFT" {
		t.Errorf("new thread at wrong position: side=%s", s)
	}
}

func TestFileRenderList_ReplaceThread(t *testing.T) {
	list := &FileRenderList{
		Items: []Renderable{
			makeDiffLineItem(0, LineAdd, "line 0"),
			makeThreadItem(0, "RIGHT", 1, "original"),
			makeDiffLineItem(1, LineAdd, "line 1"),
		},
	}

	replacement := makeThreadItem(0, "RIGHT", 1, "original", "reply")
	ok := list.ReplaceThread("RIGHT", 1, replacement)

	if !ok {
		t.Fatal("ReplaceThread returned false")
	}
	ct := list.Items[1].(*CommentThreadItem)
	if len(ct.Comments) != 2 {
		t.Errorf("expected 2 comments, got %d", len(ct.Comments))
	}
	if !list.dirty {
		t.Error("expected dirty flag")
	}
}

func TestFileRenderList_ReplaceThread_NotFound(t *testing.T) {
	list := &FileRenderList{
		Items: []Renderable{
			makeDiffLineItem(0, LineAdd, "line 0"),
		},
	}

	ok := list.ReplaceThread("RIGHT", 99, makeThreadItem(0, "RIGHT", 99, "hello"))
	if ok {
		t.Error("expected ReplaceThread to return false for missing thread")
	}
}

func TestFileRenderList_RemoveThread(t *testing.T) {
	list := &FileRenderList{
		Items: []Renderable{
			makeDiffLineItem(0, LineAdd, "line 0"),
			makeThreadItem(0, "RIGHT", 1, "to remove"),
			makeDiffLineItem(1, LineAdd, "line 1"),
		},
	}

	ok := list.RemoveThread("RIGHT", 1)
	if !ok {
		t.Fatal("RemoveThread returned false")
	}
	if len(list.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(list.Items))
	}
	for _, item := range list.Items {
		if !item.IsDiffLine() {
			t.Error("expected all remaining items to be diff lines")
		}
	}
}

func TestFileRenderList_DiffLineOffset(t *testing.T) {
	list := &FileRenderList{
		Items: []Renderable{
			makeDiffLineItem(0, LineAdd, "line 0"),
			makeDiffLineItem(1, LineAdd, "line 1"),
			makeDiffLineItem(2, LineAdd, "line 2"),
		},
	}

	rc := testRC()

	if got := list.DiffLineOffset(0, rc); got != 0 {
		t.Errorf("offset for line 0: got %d, want 0", got)
	}
	if got := list.DiffLineOffset(1, rc); got != 1 {
		t.Errorf("offset for line 1: got %d, want 1", got)
	}
	if got := list.DiffLineOffset(2, rc); got != 2 {
		t.Errorf("offset for line 2: got %d, want 2", got)
	}
}

func TestFileRenderList_DiffLineOffsets_Slice(t *testing.T) {
	list := &FileRenderList{
		Items: []Renderable{
			makeDiffLineItem(0, LineAdd, "line 0"),
			makeDiffLineItem(1, LineAdd, "line 1"),
			makeDiffLineItem(2, LineAdd, "line 2"),
		},
	}

	rc := testRC()
	offsets := list.DiffLineOffsets(3, rc)
	if len(offsets) != 3 {
		t.Fatalf("expected 3 offsets, got %d", len(offsets))
	}
	for i, want := range []int{0, 1, 2} {
		if offsets[i] != want {
			t.Errorf("offsets[%d] = %d, want %d", i, offsets[i], want)
		}
	}
}

func TestFileRenderList_String(t *testing.T) {
	list := &FileRenderList{
		Items: []Renderable{
			makeDiffLineItem(0, LineAdd, "hello"),
			makeDiffLineItem(1, LineAdd, "world"),
		},
	}

	rc := testRC()
	result := list.String(rc)

	if !strings.Contains(result, "hello") {
		t.Error("result missing 'hello'")
	}
	if !strings.Contains(result, "world") {
		t.Error("result missing 'world'")
	}

	// Second call should be cached
	result2 := list.String(rc)
	if result != result2 {
		t.Error("second call returned different result (caching broken)")
	}
}

func TestFileRenderList_StringCacheInvalidation(t *testing.T) {
	list := &FileRenderList{
		Items: []Renderable{
			makeDiffLineItem(0, LineAdd, "hello"),
		},
	}

	rc := testRC()
	result1 := list.String(rc)

	list.InsertAfterDiffLine(0, makeDiffLineItem(1, LineAdd, "world"))
	result2 := list.String(rc)

	if result1 == result2 {
		t.Error("string cache not invalidated after insert")
	}
}

func TestFileRenderList_FindThread(t *testing.T) {
	list := &FileRenderList{
		Items: []Renderable{
			makeDiffLineItem(0, LineAdd, "line 0"),
			makeThreadItem(0, "RIGHT", 1, "comment"),
			makeDiffLineItem(1, LineAdd, "line 1"),
		},
	}

	if idx := list.FindThread("RIGHT", 1); idx != 1 {
		t.Errorf("FindThread RIGHT/1: got %d, want 1", idx)
	}
	if idx := list.FindThread("LEFT", 1); idx != -1 {
		t.Errorf("FindThread LEFT/1: got %d, want -1", idx)
	}
}

func TestFileRenderList_InvalidateAll(t *testing.T) {
	list := &FileRenderList{
		Items: []Renderable{
			makeDiffLineItem(0, LineAdd, "hello"),
		},
	}

	rc := testRC()
	list.String(rc)

	list.InvalidateAll()

	if !list.dirty {
		t.Error("expected dirty after InvalidateAll")
	}
}

func TestBuildRenderList_OutputStructure(t *testing.T) {
	// Build a realistic highlighted diff with comments and verify the render
	// list produces structurally valid output (offsets, comment positions, etc.).
	patch := `@@ -1,3 +1,4 @@
 context line 1
+added line 2
 context line 3
 context line 4`

	diffLines := ParsePatchLines(patch)
	colW := GutterColWidth(diffLines)

	// Pre-format lines.
	formattedLines := make([]DiffLine, len(diffLines))
	copy(formattedLines, diffLines)
	colors := testColors()
	formatDiffLinesFromHL(formattedLines, nil, nil, "test.go", 80, colors, colW)

	reviewComments := []github.ReviewComment{
		{
			ID:   1,
			Body: "looks good",
			User: github.User{Login: "alice"},
			Side: "RIGHT",
			Line: intPtr(2),
			OriginalLine: intPtr(2),
			Path: "test.go",
		},
	}

	// Build render list.
	list := BuildRenderList(formattedLines, reviewComments)
	rc := RenderContext{Width: 80, Colors: colors, ColW: colW}
	result := list.String(rc)

	// Verify output is non-empty and has expected number of lines.
	if result == "" {
		t.Fatal("render list produced empty output")
	}

	resultLines := strings.Split(result, "\n")
	// Should have 4 diff lines + comment thread lines.
	if len(resultLines) < 4 {
		t.Errorf("expected at least 4 lines, got %d", len(resultLines))
	}

	// Verify DiffLineOffsets: should have one entry per diff line.
	offsets := list.DiffLineOffsets(len(diffLines), rc)
	if len(offsets) != len(diffLines) {
		t.Fatalf("offset count mismatch: got %d, want %d", len(offsets), len(diffLines))
	}

	// Offsets should be non-decreasing.
	for i := 1; i < len(offsets); i++ {
		if offsets[i] < offsets[i-1] {
			t.Errorf("offsets not non-decreasing: [%d]=%d [%d]=%d", i-1, offsets[i-1], i, offsets[i])
		}
	}

	// Comment positions should exist and point to valid lines.
	positions := list.CommentPositions(rc)
	if len(positions) != 1 {
		t.Fatalf("expected 1 comment position, got %d", len(positions))
	}
	if positions[0].Line != 2 || positions[0].Side != "RIGHT" {
		t.Errorf("unexpected position: line=%d side=%s", positions[0].Line, positions[0].Side)
	}
}

func TestToolGroupRendering(t *testing.T) {
	width := 80
	colors := testColors()
	colW := 4

	// Create a RenderComment with text + tool group + text blocks.
	rc := RenderComment{
		Author:    "copilot",
		CreatedAt: time.Now(),
		Blocks: []comments.ContentBlock{
			comments.TextBlock{Text: "Let me check the code."},
			comments.ToolGroupBlock{Tools: []comments.ToolCall{
				{Name: "read_file", Status: "done"},
				{Name: "search_code", Status: "done"},
			}},
			comments.TextBlock{Text: "Here is the fix."},
		},
	}

	thread := NewCommentThreadItem(0, "RIGHT", 1, []RenderComment{rc}, LineAdd)
	rendered := thread.Render(RenderContext{Width: width, Colors: colors, ColW: colW})

	lines := strings.Split(rendered, "\n")

	// Every non-empty line must be exactly `width` visual columns.
	for i, line := range lines {
		if line == "" {
			continue
		}
		visW := lipgloss.Width(line)
		if visW != width {
			t.Errorf("line %d: visual width %d, want %d: %q", i, visW, width, line)
		}
	}

	// Should contain tool-related symbols.
	raw := strings.Join(lines, "\n")
	if !strings.Contains(raw, "read_file") {
		t.Error("expected 'read_file' in rendered output")
	}
	if !strings.Contains(raw, "search_code") {
		t.Error("expected 'search_code' in rendered output")
	}
	// Done tools use ● marker.
	if !strings.Contains(raw, "●") {
		t.Error("expected ● marker for done tools")
	}
	// Rounded borders (no "Tools" label — implied).
	if !strings.Contains(raw, "╭") || !strings.Contains(raw, "╯") {
		t.Error("expected rounded border characters ╭ and ╯")
	}
}

func TestToolGroupRendering_Running(t *testing.T) {
	width := 80
	colors := testColors()
	colW := 4

	rc := RenderComment{
		Author:    "copilot",
		CreatedAt: time.Now(),
		Blocks: []comments.ContentBlock{
			comments.ToolGroupBlock{Tools: []comments.ToolCall{
				{Name: "read_file", Status: "done"},
				{Name: "apply_edit", Status: "running"},
			}},
		},
	}

	thread := NewCommentThreadItem(0, "RIGHT", 1, []RenderComment{rc}, LineAdd)
	rendered := thread.Render(RenderContext{Width: width, Colors: colors, ColW: colW})

	// Running tools use a braille spinner (first frame ⠋ at animFrame=0).
	spinFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	hasSpinner := false
	for _, f := range spinFrames {
		if strings.Contains(rendered, f) {
			hasSpinner = true
			break
		}
	}
	if !hasSpinner {
		t.Error("expected braille spinner marker for running tools")
	}
	// Done tools still get ●.
	if !strings.Contains(rendered, "●") {
		t.Error("expected ● marker for done tools")
	}
}

func TestToolGroupRendering_Failed(t *testing.T) {
	width := 80
	colors := testColors()
	colW := 4

	rc := RenderComment{
		Author:    "copilot",
		CreatedAt: time.Now(),
		Blocks: []comments.ContentBlock{
			comments.ToolGroupBlock{Tools: []comments.ToolCall{
				{Name: "write_file", Status: "failed"},
			}},
		},
	}

	thread := NewCommentThreadItem(0, "RIGHT", 1, []RenderComment{rc}, LineAdd)
	rendered := thread.Render(RenderContext{Width: width, Colors: colors, ColW: colW})

	// Failed tools should use ✕ marker.
	if !strings.Contains(rendered, "✕") {
		t.Error("expected ✕ marker for failed tools")
	}
}

func TestBuildThreadedRenderComments_PreservesBlocks(t *testing.T) {
	// Simulate: user comment (no blocks) + copilot reply (with tool blocks).
	// The block lookup should preserve tool calls on the copilot reply.
	line := 5
	replyTo := 100
	reviewComments := []github.ReviewComment{
		{
			ID:   100,
			Body: "please fix this",
			Side: "RIGHT",
			Line: &line,
			User: github.User{Login: "you"},
		},
		{
			ID:          200,
			Body:        "Here is the fix",
			Side:        "RIGHT",
			InReplyToID: &replyTo,
			User:        github.User{Login: "copilot"},
		},
	}

	// Block lookup: copilot reply (ID=200) has text + tool group + text.
	blockLookup := map[int][]comments.ContentBlock{
		200: {
			comments.TextBlock{Text: "Let me check"},
			comments.ToolGroupBlock{Tools: []comments.ToolCall{
				{Name: "read_file", Status: "done"},
				{Name: "apply_edit", Status: "done"},
			}},
			comments.TextBlock{Text: "Here is the fix"},
		},
	}

	threaded := BuildThreadedRenderComments(reviewComments, blockLookup)
	key := CommentKey{Side: "RIGHT", Line: 5}
	thread, ok := threaded[key]
	if !ok {
		t.Fatal("expected thread at RIGHT:5")
	}
	if len(thread) != 2 {
		t.Fatalf("expected 2 comments in thread, got %d", len(thread))
	}

	// First comment (user): should be plain TextBlock from Body.
	if len(thread[0].Blocks) != 1 {
		t.Errorf("user comment: expected 1 block, got %d", len(thread[0].Blocks))
	}
	if tb, ok := thread[0].Blocks[0].(comments.TextBlock); !ok || tb.Text != "please fix this" {
		t.Errorf("user comment block = %+v", thread[0].Blocks[0])
	}

	// Second comment (copilot): should preserve all 3 blocks from lookup.
	if len(thread[1].Blocks) != 3 {
		t.Fatalf("copilot comment: expected 3 blocks, got %d", len(thread[1].Blocks))
	}
	if _, ok := thread[1].Blocks[0].(comments.TextBlock); !ok {
		t.Error("copilot block 0: expected TextBlock")
	}
	if tg, ok := thread[1].Blocks[1].(comments.ToolGroupBlock); !ok {
		t.Error("copilot block 1: expected ToolGroupBlock")
	} else if len(tg.Tools) != 2 {
		t.Errorf("copilot block 1: expected 2 tools, got %d", len(tg.Tools))
	}
	if _, ok := thread[1].Blocks[2].(comments.TextBlock); !ok {
		t.Error("copilot block 2: expected TextBlock")
	}

	// Now render it and verify tool markers appear.
	width := 80
	colors := testColors()
	ct := NewCommentThreadItem(0, "RIGHT", 5, thread, LineAdd)
	out := ct.Render(RenderContext{Width: width, Colors: colors, ColW: 4})
	if !strings.Contains(out, "●") {
		t.Error("expected ● marker for done tools in rendered output")
	}
	if !strings.Contains(out, "read_file") {
		t.Error("expected 'read_file' in rendered output")
	}
}

func TestToolGroupRendering_CustomLabel(t *testing.T) {
	width := 80
	colors := testColors()
	colW := 4

	// Tool group with a custom label (from report_intent).
	rc := RenderComment{
		Author:    "copilot",
		CreatedAt: time.Now(),
		Blocks: []comments.ContentBlock{
			comments.ToolGroupBlock{
				Label: "Exploring codebase",
				Tools: []comments.ToolCall{
					{Name: "grep", Status: "done", Arguments: "pattern=TODO"},
					{Name: "view", Status: "done", Arguments: "path=/src/main.go"},
				},
			},
		},
	}

	thread := NewCommentThreadItem(0, "RIGHT", 1, []RenderComment{rc}, LineAdd)
	rendered := thread.Render(RenderContext{Width: width, Colors: colors, ColW: colW})

	// Should show custom label, not "Tools".
	if !strings.Contains(rendered, "Exploring codebase") {
		t.Error("expected custom label 'Exploring codebase' in rendered output")
	}
	if strings.Contains(rendered, " Tools ") {
		t.Error("should not contain default 'Tools' label when custom label is set")
	}
	// Should show arguments.
	if !strings.Contains(rendered, "pattern=TODO") {
		t.Error("expected tool arguments in rendered output")
	}
	// Width check.
	for i, line := range strings.Split(rendered, "\n") {
		if line == "" {
			continue
		}
		if visW := lipgloss.Width(line); visW != width {
			t.Errorf("line %d: visual width %d, want %d", i, visW, width)
		}
	}
}

// ---------------------------------------------------------------------------
// BuildRenderList: orphan / LEFT-on-context tests
// ---------------------------------------------------------------------------

func TestBuildRenderList_LeftCommentOnContextLine(t *testing.T) {
	// A LEFT-side comment on a context line should be rendered.
	patch := "@@ -1,3 +1,4 @@\n context line 1\n+added line 2\n context line 3\n context line 4"
	diffLines := ParsePatchLines(patch)
	colW := GutterColWidth(diffLines)
	formatted := make([]DiffLine, len(diffLines))
	copy(formatted, diffLines)
	formatDiffLinesFromHL(formatted, nil, nil, "test.go", 80, testColors(), colW)

	comments := []github.ReviewComment{
		makeComment(1, "left side comment on context", "test.go", 1, "LEFT", nil),
	}

	list := BuildRenderList(formatted, comments)
	positions := list.CommentPositions(testRC())
	if len(positions) == 0 {
		t.Fatal("LEFT comment on context line was not rendered")
	}
	if positions[0].Side != "LEFT" || positions[0].Line != 1 {
		t.Errorf("unexpected position: side=%s line=%d", positions[0].Side, positions[0].Line)
	}
}

func TestBuildRenderList_OrphanCommentNotRendered(t *testing.T) {
	// Comment on line 50 — well outside the diff hunk (lines 1-4).
	// Orphan comments should NOT be rendered.
	patch := "@@ -1,3 +1,4 @@\n context line 1\n+added line 2\n context line 3\n context line 4"
	diffLines := ParsePatchLines(patch)
	colW := GutterColWidth(diffLines)
	formatted := make([]DiffLine, len(diffLines))
	copy(formatted, diffLines)
	formatDiffLinesFromHL(formatted, nil, nil, "test.go", 80, testColors(), colW)

	comments := []github.ReviewComment{
		makeComment(1, "orphan comment far away", "test.go", 50, "RIGHT", nil),
	}

	list := BuildRenderList(formatted, comments)
	positions := list.CommentPositions(testRC())
	if len(positions) != 0 {
		t.Fatalf("expected orphan comment to not be rendered, got %d positions", len(positions))
	}
}

func TestBuildRenderList_InlineOnly(t *testing.T) {
	// Only inline comments are rendered; orphans are skipped.
	patch := "@@ -1,3 +1,4 @@\n context line 1\n+added line 2\n context line 3\n context line 4"
	diffLines := ParsePatchLines(patch)
	colW := GutterColWidth(diffLines)
	formatted := make([]DiffLine, len(diffLines))
	copy(formatted, diffLines)
	formatDiffLinesFromHL(formatted, nil, nil, "test.go", 80, testColors(), colW)

	comments := []github.ReviewComment{
		makeComment(1, "inline on visible line", "test.go", 2, "RIGHT", nil),
		makeComment(2, "orphan far away", "test.go", 100, "RIGHT", nil),
	}

	list := BuildRenderList(formatted, comments)
	positions := list.CommentPositions(testRC())
	if len(positions) != 1 {
		t.Fatalf("expected 1 comment position (inline only), got %d", len(positions))
	}
	if positions[0].Line != 2 {
		t.Errorf("expected line 2, got %d", positions[0].Line)
	}
}
