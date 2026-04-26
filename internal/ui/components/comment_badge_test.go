package components

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/blakewilliams/gg/internal/github"
	"github.com/blakewilliams/gg/internal/ui/styles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testDiffColors() styles.DiffColors {
	return styles.DiffColors{
		PaletteRed:     color.RGBA{R: 220, G: 50, B: 50, A: 255},
		PaletteYellow:  color.RGBA{R: 200, G: 180, B: 0, A: 255},
		PaletteCyan:    color.RGBA{R: 0, G: 180, B: 200, A: 255},
		PaletteMagenta: color.RGBA{R: 180, G: 0, B: 180, A: 255},
		PaletteBg:      color.RGBA{R: 30, G: 30, B: 30, A: 255},
		PaletteFg:      color.RGBA{R: 255, G: 255, B: 255, A: 255},
		PaletteDim:     color.RGBA{R: 128, G: 128, B: 128, A: 255},
	}
}

func TestBadgeVisualWidth_SingleDigit(t *testing.T) {
	badge := &CommentBadge{TotalCount: 3, MaxUrgency: BadgeUnread}
	w := BadgeVisualWidth(badge, 0, testDiffColors())
	assert.Greater(t, w, 0, "badge should have positive width")
	// The pill should be reasonable: caps + spaces + icon + count
	assert.LessOrEqual(t, w, 12, "badge should not be excessively wide")
}

func TestBadgeVisualWidth_MultiDigit(t *testing.T) {
	badge1 := &CommentBadge{TotalCount: 9, MaxUrgency: BadgeUnread}
	badge2 := &CommentBadge{TotalCount: 10, MaxUrgency: BadgeUnread}
	colors := testDiffColors()
	w1 := BadgeVisualWidth(badge1, 0, colors)
	w2 := BadgeVisualWidth(badge2, 0, colors)
	assert.Equal(t, w2, w1+1, "double-digit badge should be 1 char wider")
}

func TestBadgeVisualWidth_Nil(t *testing.T) {
	colors := testDiffColors()
	assert.Equal(t, 0, BadgeVisualWidth(nil, 0, colors))
	assert.Equal(t, 0, BadgeVisualWidth(&CommentBadge{TotalCount: 0}, 0, colors))
}

func TestRenderBadgePill_AllUrgencies(t *testing.T) {
	colors := testDiffColors()
	urgencies := []BadgeUrgency{BadgeRead, BadgeResolved, BadgeUnread, BadgeChangesRequested}
	for _, u := range urgencies {
		badge := &CommentBadge{TotalCount: 5, MaxUrgency: u}
		pill := RenderBadgePill(badge, "\033[49m", 0, colors)
		require.NotEmpty(t, pill, "pill should render for urgency %d", u)
		assert.Equal(t, BadgeVisualWidth(badge, 0, colors), lipgloss.Width(pill),
			"pill visual width should match BadgeVisualWidth for urgency %d", u)
	}
}

func TestRenderBadgePill_Nil(t *testing.T) {
	colors := testDiffColors()
	assert.Empty(t, RenderBadgePill(nil, "\033[49m", 0, colors))
	assert.Empty(t, RenderBadgePill(&CommentBadge{TotalCount: 0}, "\033[49m", 0, colors))
}

func TestRenderBadgePill_Working(t *testing.T) {
	colors := testDiffColors()
	badge := &CommentBadge{TotalCount: 3, MaxUrgency: BadgeUnread, Working: true}

	widths := make([]int, len(badgeSpinFrames))
	for i := range badgeSpinFrames {
		pill := RenderBadgePill(badge, "\033[49m", i, colors)
		require.NotEmpty(t, pill, "working pill should render for frame %d", i)
		widths[i] = lipgloss.Width(pill)
	}
	for i := 1; i < len(widths); i++ {
		assert.Equal(t, widths[0], widths[i], "working badge width should be stable across frames")
	}

	// Working badge should show spinner glyph, not count
	pill := RenderBadgePill(badge, "\033[49m", 0, colors)
	assert.Contains(t, pill, "⠋", "working badge should show spinner glyph")

	// Non-working badge with same count should show "7"
	normalBadge := &CommentBadge{TotalCount: 7, MaxUrgency: BadgeUnread}
	normalPill := RenderBadgePill(normalBadge, "\033[49m", 0, colors)
	assert.Contains(t, normalPill, "7", "normal badge should show count")

	// Working badge should NOT show the count
	workingBadge7 := &CommentBadge{TotalCount: 7, MaxUrgency: BadgeUnread, Working: true}
	workingPill7 := RenderBadgePill(workingBadge7, "\033[49m", 0, colors)
	assert.NotContains(t, workingPill7, "7", "working badge should not show count")

	// Should contain magenta bg ANSI code (from palette)
	magentaBg := styles.ColorToBgCode(colors.PaletteMagenta)
	assert.Contains(t, pill, magentaBg, "working badge should use magenta background")
}

func TestOverlayBadge_PadsToWidth(t *testing.T) {
	colors := testDiffColors()
	badge := &CommentBadge{TotalCount: 3, MaxUrgency: BadgeUnread}
	width := 80
	line := padWithBg("hello", width, "\033[49m")
	result := OverlayBadge(line, badge, "\033[49m", width, 0, colors)

	resultW := lipgloss.Width(result)
	assert.Equal(t, width, resultW, "overlaid line should maintain exact width")
}

func TestOverlayBadge_NarrowTerminal(t *testing.T) {
	colors := testDiffColors()
	badge := &CommentBadge{TotalCount: 3, MaxUrgency: BadgeUnread}
	width := 5 // too narrow for any badge
	line := padWithBg("hi", width, "\033[49m")
	result := OverlayBadge(line, badge, "\033[49m", width, 0, colors)
	assert.Equal(t, line, result, "should return line unchanged when too narrow")
}

func TestOverlayBadge_Nil(t *testing.T) {
	line := "hello world"
	assert.Equal(t, line, OverlayBadge(line, nil, "\033[49m", 80, 0, testDiffColors()))
}

func TestOverlayBadge_DifferentBackgrounds(t *testing.T) {
	colors := testDiffColors()
	badge := &CommentBadge{TotalCount: 1, MaxUrgency: BadgeChangesRequested}
	width := 80

	backgrounds := []string{
		"\033[49m",      // default
		"\033[48;5;22m", // dark green (add)
		"\033[48;5;52m", // dark red (del)
	}
	for _, bg := range backgrounds {
		line := padWithBg("test line", width, bg)
		result := OverlayBadge(line, badge, bg, width, 0, colors)
		resultW := lipgloss.Width(result)
		assert.Equal(t, width, resultW, "should maintain width for bg %q", bg)
	}
}

func TestBuildRenderList_BadgesAttached(t *testing.T) {
	line10 := 10
	diffLines := []DiffLine{
		{Type: LineContext, OldLineNo: 10, NewLineNo: 10, Content: " context", Rendered: " context"},
		{Type: LineAdd, OldLineNo: 0, NewLineNo: 11, Content: "+added", Rendered: "+added"},
	}
	comments := []makeCommentArgs{
		{id: 1, line: &line10, side: "RIGHT"},
	}
	ghComments := makeGHComments(comments)

	list := BuildRenderList(diffLines, ghComments)

	// First diff line (context at line 10) should have a badge
	dli0 := list.Items[0].(*DiffLineItem)
	require.NotNil(t, dli0.Badge, "context line with comment should have badge")
	assert.Equal(t, 1, dli0.Badge.TotalCount)
	assert.Equal(t, BadgeUnread, dli0.Badge.MaxUrgency)

	// Second diff line (add at line 11) should NOT have a badge
	// Find it — it might be after a CommentThreadItem
	for _, item := range list.Items {
		if dli, ok := item.(*DiffLineItem); ok && dli.DiffIdx() == 1 {
			assert.Nil(t, dli.Badge, "line without comments should have no badge")
		}
	}
}

func TestBuildRenderList_BadgesOnly(t *testing.T) {
	line10 := 10
	diffLines := []DiffLine{
		{Type: LineAdd, OldLineNo: 0, NewLineNo: 10, Content: "+added", Rendered: "+added"},
	}
	ghComments := makeGHComments([]makeCommentArgs{
		{id: 1, line: &line10, side: "RIGHT"},
	})

	list := BuildRenderList(diffLines, ghComments, DiffFormatOptions{BadgesOnly: true})

	// Should have badge but no CommentThreadItem
	hasBadge := false
	hasThread := false
	for _, item := range list.Items {
		if dli, ok := item.(*DiffLineItem); ok && dli.Badge != nil {
			hasBadge = true
		}
		if _, ok := item.(*CommentThreadItem); ok {
			hasThread = true
		}
	}
	assert.True(t, hasBadge, "should have badge")
	assert.False(t, hasThread, "BadgesOnly should suppress thread items")
}

func TestBuildRenderList_BadgesWithThreads(t *testing.T) {
	line10 := 10
	diffLines := []DiffLine{
		{Type: LineAdd, OldLineNo: 0, NewLineNo: 10, Content: "+added", Rendered: "+added"},
	}
	ghComments := makeGHComments([]makeCommentArgs{
		{id: 1, line: &line10, side: "RIGHT"},
	})

	// Default: both badges and threads
	list := BuildRenderList(diffLines, ghComments)

	hasBadge := false
	hasThread := false
	for _, item := range list.Items {
		if dli, ok := item.(*DiffLineItem); ok && dli.Badge != nil {
			hasBadge = true
		}
		if _, ok := item.(*CommentThreadItem); ok {
			hasThread = true
		}
	}
	assert.True(t, hasBadge, "should have badge")
	assert.True(t, hasThread, "default mode should include thread items")
}

func TestBuildRenderList_BadgeAggregatesLeftRight(t *testing.T) {
	line5 := 5
	diffLines := []DiffLine{
		{Type: LineContext, OldLineNo: 5, NewLineNo: 5, Content: " ctx", Rendered: " ctx"},
	}
	ghComments := makeGHComments([]makeCommentArgs{
		{id: 1, line: &line5, side: "RIGHT"},
		{id: 2, line: &line5, side: "LEFT"},
	})

	list := BuildRenderList(diffLines, ghComments, DiffFormatOptions{BadgesOnly: true})

	dli := list.Items[0].(*DiffLineItem)
	require.NotNil(t, dli.Badge)
	assert.Equal(t, 2, dli.Badge.TotalCount, "should aggregate LEFT+RIGHT comments")
}

func TestBuildRenderList_BadgeDataOverridesUrgency(t *testing.T) {
	line10 := 10
	diffLines := []DiffLine{
		{Type: LineAdd, OldLineNo: 0, NewLineNo: 10, Content: "+added", Rendered: "+added"},
	}
	ghComments := makeGHComments([]makeCommentArgs{
		{id: 1, line: &line10, side: "RIGHT"},
	})

	opts := DiffFormatOptions{
		BadgesOnly: true,
		BadgeData: map[CommentKey]BadgeInfo{
			{Side: "RIGHT", Line: 10}: {Count: 5, Urgency: BadgeChangesRequested},
		},
	}
	list := BuildRenderList(diffLines, ghComments, opts)

	dli := list.Items[0].(*DiffLineItem)
	require.NotNil(t, dli.Badge)
	assert.Equal(t, 5, dli.Badge.TotalCount)
	assert.Equal(t, BadgeChangesRequested, dli.Badge.MaxUrgency)
}

func TestBuildRenderList_BadgeWorkingFromPendingComments(t *testing.T) {
	line10 := 10
	diffLines := []DiffLine{
		{Type: LineAdd, OldLineNo: 0, NewLineNo: 10, Content: "+added", Rendered: "+added"},
	}
	ghComments := makeGHComments([]makeCommentArgs{
		{id: 1, line: &line10, side: "RIGHT"},
	})

	opts := DiffFormatOptions{
		BadgesOnly: true,
		PendingComments: map[CommentKey][]RenderComment{
			{Side: "RIGHT", Line: 10}: {{Author: "copilot"}},
		},
	}
	list := BuildRenderList(diffLines, ghComments, opts)

	dli := list.Items[0].(*DiffLineItem)
	require.NotNil(t, dli.Badge)
	assert.True(t, dli.Badge.Working, "badge should be Working when PendingComments exist")
}

func TestBuildRenderList_BadgeNotWorkingWithoutPending(t *testing.T) {
	line10 := 10
	diffLines := []DiffLine{
		{Type: LineAdd, OldLineNo: 0, NewLineNo: 10, Content: "+added", Rendered: "+added"},
	}
	ghComments := makeGHComments([]makeCommentArgs{
		{id: 1, line: &line10, side: "RIGHT"},
	})

	list := BuildRenderList(diffLines, ghComments, DiffFormatOptions{BadgesOnly: true})

	dli := list.Items[0].(*DiffLineItem)
	require.NotNil(t, dli.Badge)
	assert.False(t, dli.Badge.Working, "badge should not be Working without pending comments")
}

// --- test helpers ---

type makeCommentArgs struct {
	id      int
	line    *int
	side    string
	replyTo *int
}

func makeGHComments(args []makeCommentArgs) []github.ReviewComment {
	out := make([]github.ReviewComment, len(args))
	for i, a := range args {
		out[i] = github.ReviewComment{
			ID:           a.id,
			Body:         "test comment",
			Line:         a.line,
			OriginalLine: a.line,
			Side:         a.side,
			InReplyToID:  a.replyTo,
			User:         github.User{Login: "testuser"},
		}
	}
	return out
}
