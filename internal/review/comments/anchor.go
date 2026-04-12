package comments

import (
	"strings"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/components"
)

// AnchorResult describes where a PR comment maps to in a local diff.
type AnchorResult struct {
	Line int    // local diff line number (OldLineNo or NewLineNo)
	Side string // "LEFT" or "RIGHT"
}

// AnchorComment maps a GitHub ReviewComment to a local diff line by matching
// the code content the comment was placed on. This is stable across line
// number shifts caused by insertions/deletions.
//
// Returns ok=false if:
//   - The comment is outdated (position is nil and original_line differs from line)
//   - The commented code no longer exists in the local diff
//   - The comment has no line information
func AnchorComment(comment github.ReviewComment, localDiffs []components.DiffLine, prPatch string) (AnchorResult, bool) {
	// Must have a line number to anchor.
	commentLine := 0
	if comment.Line != nil {
		commentLine = *comment.Line
	} else if comment.OriginalLine != nil {
		commentLine = *comment.OriginalLine
	}
	if commentLine == 0 {
		return AnchorResult{}, false
	}

	// Detect outdated: GitHub sets Line != OriginalLine when the code has changed.
	if comment.Line != nil && comment.OriginalLine != nil && *comment.Line != *comment.OriginalLine {
		return AnchorResult{}, false
	}

	// Get the content of the line the comment was placed on from the PR patch.
	commentContent := findLineContent(prPatch, commentLine, comment.Side)
	if commentContent == "" {
		// Fallback: if we can't extract from patch, we can't match.
		return AnchorResult{}, false
	}

	// Search local diff for a line with matching content.
	normalizedTarget := normalizeWhitespace(commentContent)
	side := comment.Side
	if side == "" {
		side = "RIGHT"
	}

	type candidate struct {
		line int
		side string
		dist int // distance from original line number
	}
	var candidates []candidate

	for _, dl := range localDiffs {
		if dl.Type == components.LineHunk {
			continue
		}

		normalized := normalizeWhitespace(dl.Content)
		if normalized != normalizedTarget {
			continue
		}

		// Check side compatibility.
		if side == "LEFT" && dl.Type == components.LineDel {
			dist := abs(dl.OldLineNo - commentLine)
			candidates = append(candidates, candidate{dl.OldLineNo, "LEFT", dist})
		} else if side == "RIGHT" && (dl.Type == components.LineAdd || dl.Type == components.LineContext) {
			dist := abs(dl.NewLineNo - commentLine)
			candidates = append(candidates, candidate{dl.NewLineNo, "RIGHT", dist})
		} else if side == "RIGHT" && dl.Type == components.LineContext {
			// Context lines appear on both sides.
			dist := abs(dl.NewLineNo - commentLine)
			candidates = append(candidates, candidate{dl.NewLineNo, "RIGHT", dist})
		}
	}

	if len(candidates) == 0 {
		return AnchorResult{}, false
	}

	// Pick the candidate closest to the original line number.
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.dist < best.dist {
			best = c
		}
	}

	return AnchorResult{Line: best.line, Side: best.side}, true
}

// AnchorComments maps multiple PR review comments to local diff lines.
// Returns only the comments that could be anchored, with updated line/side.
func AnchorComments(prComments []github.ReviewComment, localDiffs []components.DiffLine, prPatch string) []github.ReviewComment {
	var result []github.ReviewComment

	for _, c := range prComments {
		anchor, ok := AnchorComment(c, localDiffs, prPatch)
		if !ok {
			continue
		}

		// Create a copy with the local line number.
		anchored := c
		line := anchor.Line
		anchored.Line = &line
		anchored.OriginalLine = &line
		anchored.Side = anchor.Side
		result = append(result, anchored)
	}

	return result
}

// findLineContent extracts the content of a specific line from a unified diff patch.
// Side "LEFT" looks for deletion lines, "RIGHT" for addition/context lines.
func findLineContent(patch string, lineNo int, side string) string {
	if patch == "" {
		return ""
	}

	lines := strings.Split(patch, "\n")
	oldNum, newNum := 0, 0

	for _, line := range lines {
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "@@") {
			// Parse hunk header.
			oldNum, newNum = parseHunkLineNums(line)
			continue
		}

		switch {
		case strings.HasPrefix(line, "+"):
			if side == "RIGHT" && newNum == lineNo {
				return line[1:]
			}
			newNum++
		case strings.HasPrefix(line, "-"):
			if side == "LEFT" && oldNum == lineNo {
				return line[1:]
			}
			oldNum++
		default:
			content := line
			if len(line) > 0 && line[0] == ' ' {
				content = line[1:]
			}
			if side == "RIGHT" && newNum == lineNo {
				return content
			}
			if side == "LEFT" && oldNum == lineNo {
				return content
			}
			oldNum++
			newNum++
		}
	}

	return ""
}

// parseHunkLineNums extracts the starting line numbers from a @@ hunk header.
func parseHunkLineNums(header string) (oldStart, newStart int) {
	// Format: @@ -oldStart[,count] +newStart[,count] @@
	parts := strings.Fields(header)
	if len(parts) < 3 {
		return 0, 0
	}

	old := strings.TrimPrefix(parts[1], "-")
	if comma := strings.IndexByte(old, ','); comma >= 0 {
		old = old[:comma]
	}

	new_ := strings.TrimPrefix(parts[2], "+")
	if comma := strings.IndexByte(new_, ','); comma >= 0 {
		new_ = new_[:comma]
	}

	oldStart = atoi(old)
	newStart = atoi(new_)
	return
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

func normalizeWhitespace(s string) string {
	return strings.TrimSpace(s)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
