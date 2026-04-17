package components

import (
	"strings"
	"testing"

	"github.com/blakewilliams/ghq/internal/github"
)

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
	var comments []github.ReviewComment
	for _, body := range bodies {
		comments = append(comments, github.ReviewComment{
			User: github.User{Login: "testuser"},
			Body: body,
		})
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

func TestBuildRenderList_MatchesFormatDiffFile(t *testing.T) {
	// Build a realistic highlighted diff with comments.
	patch := `@@ -1,3 +1,4 @@
 context line 1
+added line 2
 context line 3
 context line 4`

	file := github.PullRequestFile{
		Filename: "test.go",
		Patch:    patch,
		Status:   "modified",
	}

	diffLines := ParsePatchLines(patch)
	colW := GutterColWidth(diffLines)

	// Pre-format lines (same as FormatDiffFile does internally).
	formattedLines := make([]DiffLine, len(diffLines))
	copy(formattedLines, diffLines)
	colors := testColors()
	formatDiffLinesFromHL(formattedLines, nil, nil, "test.go", 80, colors, colW)

	comments := []github.ReviewComment{
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

	// Get the old way's output.
	hd := HighlightedDiff{
		File:      file,
		DiffLines: diffLines,
		Filename:  "test.go",
	}
	oldResult := FormatDiffFile(hd, 80, colors, comments)

	// Build render list from pre-formatted lines.
	list := BuildRenderList(formattedLines, comments)
	rc := RenderContext{Width: 80, Colors: colors, ColW: colW}
	newResult := list.String(rc)

	if oldResult.Content != newResult {
		// Find first difference for debugging.
		oldLines := strings.Split(oldResult.Content, "\n")
		newLines := strings.Split(newResult, "\n")
		maxLen := len(oldLines)
		if len(newLines) > maxLen {
			maxLen = len(newLines)
		}
		for i := 0; i < maxLen; i++ {
			var ol, nl string
			if i < len(oldLines) {
				ol = oldLines[i]
			}
			if i < len(newLines) {
				nl = newLines[i]
			}
			if ol != nl {
				t.Errorf("first difference at line %d:\n  old: %q\n  new: %q", i, ol, nl)
				break
			}
		}
		t.Errorf("output lengths: old=%d new=%d", len(oldResult.Content), len(newResult))
	}

	// Verify DiffLineOffsets match.
	oldOffsets := oldResult.DiffLineOffsets
	newOffsets := list.DiffLineOffsets(len(diffLines), rc)
	if len(oldOffsets) != len(newOffsets) {
		t.Fatalf("offset count mismatch: old=%d new=%d", len(oldOffsets), len(newOffsets))
	}
	for i := range oldOffsets {
		if oldOffsets[i] != newOffsets[i] {
			t.Errorf("DiffLineOffsets[%d]: old=%d new=%d", i, oldOffsets[i], newOffsets[i])
		}
	}
}
