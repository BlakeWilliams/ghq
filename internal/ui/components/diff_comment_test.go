package components

import (
	"strings"
	"testing"
	"time"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/styles"
)

func testColors() styles.DiffColors {
	return styles.DiffColors{
		BorderFg:          "\033[90m",
		HighlightBorderFg: "\033[33m",
	}
}

func makeComment(id int, body, path string, line int, side string, replyTo *int) github.ReviewComment {
	return github.ReviewComment{
		ID:          id,
		Body:        body,
		Path:        path,
		Line:        &line,
		OriginalLine: &line,
		Side:        side,
		InReplyToID: replyTo,
		User:        github.User{Login: "testuser"},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

func intPtr(i int) *int { return &i }

func TestCommentPositions_SingleComment(t *testing.T) {
	patch := "@@ -1,3 +1,4 @@\n context\n+added line\n+another\n context"
	file := github.PullRequestFile{Filename: "test.go", Patch: patch, Status: "modified"}
	hl := HighlightDiffFile(file, "", nil)

	comments := []github.ReviewComment{
		makeComment(1, "This looks good", "test.go", 2, "RIGHT", nil),
	}

	result := FormatDiffFile(hl, 80, testColors(), comments)

	if len(result.CommentPositions) != 1 {
		t.Fatalf("expected 1 comment position, got %d", len(result.CommentPositions))
	}
	cp := result.CommentPositions[0]
	if cp.Line != 2 || cp.Side != "RIGHT" || cp.Idx != 0 {
		t.Errorf("unexpected position: line=%d side=%s idx=%d", cp.Line, cp.Side, cp.Idx)
	}
	// The offset should be after the diff line for line 2 + blank line above thread.
	if cp.Offset <= 0 {
		t.Errorf("expected positive offset, got %d", cp.Offset)
	}
	// Verify the offset points to a line containing the author.
	lines := strings.Split(result.Content, "\n")
	if cp.Offset >= len(lines) {
		t.Fatalf("offset %d out of range (content has %d lines)", cp.Offset, len(lines))
	}
	if !strings.Contains(lines[cp.Offset], "testuser") {
		t.Errorf("offset %d should contain author, got: %s", cp.Offset, lines[cp.Offset])
	}
}

func TestCommentPositions_Thread(t *testing.T) {
	patch := "@@ -1,3 +1,4 @@\n context\n+added line\n+another\n context"
	file := github.PullRequestFile{Filename: "test.go", Patch: patch, Status: "modified"}
	hl := HighlightDiffFile(file, "", nil)

	comments := []github.ReviewComment{
		makeComment(1, "First comment", "test.go", 2, "RIGHT", nil),
		makeComment(2, "Reply here", "test.go", 2, "RIGHT", intPtr(1)),
		makeComment(3, "Another reply", "test.go", 2, "RIGHT", intPtr(1)),
	}

	result := FormatDiffFile(hl, 80, testColors(), comments)

	if len(result.CommentPositions) != 3 {
		t.Fatalf("expected 3 comment positions, got %d", len(result.CommentPositions))
	}

	lines := strings.Split(result.Content, "\n")

	// Each comment should have a distinct offset and point to its author line.
	for i, cp := range result.CommentPositions {
		if cp.Idx != i {
			t.Errorf("position %d: expected idx %d, got %d", i, i, cp.Idx)
		}
		if cp.Offset >= len(lines) {
			t.Fatalf("position %d: offset %d out of range", i, cp.Offset)
		}
		if !strings.Contains(lines[cp.Offset], "testuser") {
			t.Errorf("position %d: offset %d should contain author, got: %s", i, cp.Offset, lines[cp.Offset])
		}
	}

	// Offsets should be increasing.
	for i := 1; i < len(result.CommentPositions); i++ {
		if result.CommentPositions[i].Offset <= result.CommentPositions[i-1].Offset {
			t.Errorf("offsets not increasing: pos[%d]=%d pos[%d]=%d",
				i-1, result.CommentPositions[i-1].Offset,
				i, result.CommentPositions[i].Offset)
		}
	}
}

func TestCommentPositions_MultilineBody(t *testing.T) {
	patch := "@@ -1,3 +1,4 @@\n context\n+added line\n+another\n context"
	file := github.PullRequestFile{Filename: "test.go", Patch: patch, Status: "modified"}
	hl := HighlightDiffFile(file, "", nil)

	comments := []github.ReviewComment{
		makeComment(1, "Line one\n\nLine three\nLine four", "test.go", 2, "RIGHT", nil),
		makeComment(2, "Short reply", "test.go", 2, "RIGHT", intPtr(1)),
	}

	result := FormatDiffFile(hl, 80, testColors(), comments)

	if len(result.CommentPositions) != 2 {
		t.Fatalf("expected 2 comment positions, got %d", len(result.CommentPositions))
	}

	lines := strings.Split(result.Content, "\n")

	// First comment has 4 body lines (including the blank), second has 1.
	// So the gap between their offsets should be > 4 (header + 4 body lines).
	gap := result.CommentPositions[1].Offset - result.CommentPositions[0].Offset
	if gap < 5 {
		t.Errorf("expected gap >= 5 between comments with multiline body, got %d", gap)
	}

	// Verify blank line is preserved in rendered content.
	firstOffset := result.CommentPositions[0].Offset
	// Find the blank body line (should be between first comment header and second comment header).
	foundBlank := false
	for i := firstOffset + 1; i < result.CommentPositions[1].Offset; i++ {
		line := lines[i]
		// A blank body line has borders but only spaces in between.
		if strings.Contains(line, "│") {
			stripped := strings.TrimSpace(line)
			// If it's just borders and spaces (no text content).
			inner := strings.ReplaceAll(stripped, "│", "")
			inner = strings.ReplaceAll(inner, " ", "")
			// Remove ANSI codes for checking.
			clean := stripANSI(inner)
			if clean == "" {
				foundBlank = true
				break
			}
		}
	}
	if !foundBlank {
		t.Error("expected a blank line in the multiline comment body (paragraph break)")
	}
}

func stripANSI(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\033' {
			// Skip to 'm'.
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // skip 'm'
		} else {
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}
