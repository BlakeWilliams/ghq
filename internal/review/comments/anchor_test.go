package comments

import (
	"testing"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/components"
)

func intPtr(n int) *int { return &n }

func makeDiffLines(patch string) []components.DiffLine {
	return components.ParsePatchLines(patch)
}

func makeComment(line int, side string) github.ReviewComment {
	return github.ReviewComment{
		ID:           1,
		Line:         &line,
		OriginalLine: &line,
		Side:         side,
		User:         github.User{Login: "reviewer"},
	}
}

// Test 1: Exact line match — comment on line 2, line 2 has same content locally.
func TestAnchor_ExactLineMatch(t *testing.T) {
	patch := "@@ -1,3 +1,3 @@\n context\n+added line\n context end"
	localDiffs := makeDiffLines(patch)

	comment := makeComment(2, "RIGHT")
	result, ok := AnchorComment(comment, localDiffs, patch)

	if !ok {
		t.Fatal("expected match")
	}
	if result.Line != 2 || result.Side != "RIGHT" {
		t.Errorf("expected line 2 RIGHT, got %d %s", result.Line, result.Side)
	}
}

// Test 2: Shifted lines — 5 lines inserted above, content moved from line 2 to line 7.
func TestAnchor_ShiftedLines(t *testing.T) {
	// PR patch: comment on line 2 "target line"
	prPatch := "@@ -1,3 +1,3 @@\n context\n+target line\n context end"

	// Local diff: 5 new lines inserted before, "target line" now at newLineNo 7.
	localPatch := "@@ -1,3 +1,8 @@\n context\n+line a\n+line b\n+line c\n+line d\n+line e\n+target line\n context end"
	localDiffs := makeDiffLines(localPatch)

	comment := makeComment(2, "RIGHT")
	result, ok := AnchorComment(comment, localDiffs, prPatch)

	if !ok {
		t.Fatal("expected match for shifted line")
	}
	if result.Line != 7 {
		t.Errorf("expected line 7, got %d", result.Line)
	}
}

// Test 3: Deleted line — commented line no longer exists.
func TestAnchor_DeletedLine(t *testing.T) {
	prPatch := "@@ -1,3 +1,3 @@\n context\n+unique content xyz\n context end"
	// Local diff doesn't contain "unique content xyz" at all.
	localPatch := "@@ -1,3 +1,3 @@\n context\n+completely different\n context end"
	localDiffs := makeDiffLines(localPatch)

	comment := makeComment(2, "RIGHT")
	_, ok := AnchorComment(comment, localDiffs, prPatch)

	if ok {
		t.Error("expected no match for deleted line")
	}
}

// Test 4: Outdated comment — GitHub marked it outdated (Line != OriginalLine).
func TestAnchor_OutdatedComment(t *testing.T) {
	patch := "@@ -1,3 +1,3 @@\n context\n+added line\n context end"
	localDiffs := makeDiffLines(patch)

	comment := github.ReviewComment{
		ID:           1,
		Line:         intPtr(5),
		OriginalLine: intPtr(2), // Different from Line = outdated
		Side:         "RIGHT",
	}

	_, ok := AnchorComment(comment, localDiffs, patch)
	if ok {
		t.Error("expected no match for outdated comment")
	}
}

// Test 5: Multiple matches — same content on multiple lines, pick closest.
func TestAnchor_MultipleMatches_PicksClosest(t *testing.T) {
	prPatch := "@@ -1,5 +1,5 @@\n return nil\n+middle\n return nil\n return nil\n return nil"

	// "return nil" appears at lines 1, 3, 4, 5. Comment was on line 4.
	localPatch := "@@ -1,5 +1,5 @@\n return nil\n+middle\n return nil\n return nil\n return nil"
	localDiffs := makeDiffLines(localPatch)

	comment := makeComment(4, "RIGHT")
	result, ok := AnchorComment(comment, localDiffs, prPatch)

	if !ok {
		t.Fatal("expected match")
	}
	if result.Line != 4 {
		t.Errorf("expected line 4 (closest to original), got %d", result.Line)
	}
}

// Test 6: Modified line — content changed, no match.
func TestAnchor_ModifiedLine(t *testing.T) {
	prPatch := "@@ -1,3 +1,3 @@\n context\n+x := 1\n context end"
	// Locally the line was changed.
	localPatch := "@@ -1,3 +1,3 @@\n context\n+x := 2\n context end"
	localDiffs := makeDiffLines(localPatch)

	comment := makeComment(2, "RIGHT")
	_, ok := AnchorComment(comment, localDiffs, prPatch)

	if ok {
		t.Error("expected no match for modified line")
	}
}

// Test 7: Context line — comment on unchanged context line, always matches.
func TestAnchor_ContextLine(t *testing.T) {
	prPatch := "@@ -1,4 +1,5 @@\n func main() {\n+\tnewLine()\n \tfmt.Println(\"hello\")\n \treturn\n }"

	// Local diff has same context lines.
	localPatch := "@@ -1,4 +1,5 @@\n func main() {\n+\tnewLine()\n \tfmt.Println(\"hello\")\n \treturn\n }"
	localDiffs := makeDiffLines(localPatch)

	// Comment on context line "fmt.Println" which is at new line 4.
	comment := makeComment(4, "RIGHT")
	result, ok := AnchorComment(comment, localDiffs, prPatch)

	if !ok {
		t.Fatal("expected match for context line")
	}
	if result.Line != 4 {
		t.Errorf("expected line 4, got %d", result.Line)
	}
}

// Test 8: Empty diff — no local changes.
func TestAnchor_EmptyDiff(t *testing.T) {
	prPatch := "@@ -1,3 +1,3 @@\n context\n+added\n context end"

	comment := makeComment(2, "RIGHT")
	_, ok := AnchorComment(comment, nil, prPatch)

	if ok {
		t.Error("expected no match for empty diff")
	}
}

// Test 9: Side mismatch — comment on LEFT (deletion) but line only on RIGHT.
func TestAnchor_SideMismatch(t *testing.T) {
	prPatch := "@@ -1,3 +1,3 @@\n context\n-old line\n+new line\n context end"

	// Local diff only has additions.
	localPatch := "@@ -1,2 +1,3 @@\n context\n+old line\n context end"
	localDiffs := makeDiffLines(localPatch)

	// Comment was on the LEFT (deletion) side.
	comment := makeComment(2, "LEFT")
	_, ok := AnchorComment(comment, localDiffs, prPatch)

	// "old line" exists as an addition (RIGHT), not as a deletion (LEFT).
	if ok {
		t.Error("expected no match for side mismatch")
	}
}

// Test 10: Whitespace changes — content matches after trim.
func TestAnchor_WhitespaceNormalization(t *testing.T) {
	// PR has tabs.
	prPatch := "@@ -1,3 +1,3 @@\n context\n+\t\treturn nil\n context end"

	// Local has spaces instead of tabs (different indentation).
	localPatch := "@@ -1,3 +1,3 @@\n context\n+    return nil\n context end"
	localDiffs := makeDiffLines(localPatch)

	comment := makeComment(2, "RIGHT")
	result, ok := AnchorComment(comment, localDiffs, prPatch)

	if !ok {
		t.Fatal("expected match with whitespace normalization")
	}
	if result.Line != 2 {
		t.Errorf("expected line 2, got %d", result.Line)
	}
}

// Test 11: No line info on comment — should not match.
func TestAnchor_NoLineInfo(t *testing.T) {
	patch := "@@ -1,3 +1,3 @@\n context\n+added\n context end"
	localDiffs := makeDiffLines(patch)

	comment := github.ReviewComment{
		ID:   1,
		Side: "RIGHT",
	}

	_, ok := AnchorComment(comment, localDiffs, patch)
	if ok {
		t.Error("expected no match for comment without line info")
	}
}

// Test 12: Deletion comment on LEFT side matches correctly.
func TestAnchor_DeletionComment(t *testing.T) {
	prPatch := "@@ -1,3 +1,2 @@\n context\n-removed line\n context end"
	localPatch := "@@ -1,3 +1,2 @@\n context\n-removed line\n context end"
	localDiffs := makeDiffLines(localPatch)

	comment := makeComment(2, "LEFT")
	result, ok := AnchorComment(comment, localDiffs, prPatch)

	if !ok {
		t.Fatal("expected match for deletion comment")
	}
	if result.Line != 2 || result.Side != "LEFT" {
		t.Errorf("expected line 2 LEFT, got %d %s", result.Line, result.Side)
	}
}

// Test 13: AnchorComments filters and re-maps multiple comments.
func TestAnchorComments_Batch(t *testing.T) {
	prPatch := "@@ -1,4 +1,4 @@\n line one\n+line two\n-line three\n line four"

	localPatch := "@@ -1,4 +1,5 @@\n line one\n+extra\n+line two\n-line three\n line four"
	localDiffs := makeDiffLines(localPatch)

	comments := []github.ReviewComment{
		makeComment(2, "RIGHT"), // "line two" — shifted to line 3 locally
		makeComment(2, "LEFT"),  // "line three" — deletion at oldLine 2
		makeComment(99, "RIGHT"), // nonexistent line — should be filtered
	}

	result := AnchorComments(comments, localDiffs, prPatch)

	if len(result) != 2 {
		t.Fatalf("expected 2 anchored comments, got %d", len(result))
	}

	// First comment: "line two" should be at new local line 3.
	if *result[0].Line != 3 {
		t.Errorf("comment 0: expected line 3, got %d", *result[0].Line)
	}

	// Second comment: "line three" deletion at old line 2.
	if *result[1].Line != 2 || result[1].Side != "LEFT" {
		t.Errorf("comment 1: expected line 2 LEFT, got %d %s", *result[1].Line, result[1].Side)
	}
}

// Test 14: findLineContent extracts the right content from a patch.
func TestFindLineContent(t *testing.T) {
	patch := "@@ -1,4 +1,4 @@\n context\n+added line\n-removed line\n context end"

	tests := []struct {
		line    int
		side    string
		want    string
	}{
		{1, "RIGHT", "context"},
		{2, "RIGHT", "added line"},
		{2, "LEFT", "removed line"},
		{3, "RIGHT", "context end"},
		{99, "RIGHT", ""},
	}

	for _, tt := range tests {
		got := findLineContent(patch, tt.line, tt.side)
		if got != tt.want {
			t.Errorf("findLineContent(line=%d, side=%s): got %q, want %q", tt.line, tt.side, got, tt.want)
		}
	}
}

// Test 15: Empty patch returns no content.
func TestFindLineContent_EmptyPatch(t *testing.T) {
	got := findLineContent("", 1, "RIGHT")
	if got != "" {
		t.Errorf("expected empty for empty patch, got %q", got)
	}
}
