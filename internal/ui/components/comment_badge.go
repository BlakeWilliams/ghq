package components

import (
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blakewilliams/gg/internal/ui/styles"
)

// BadgeUrgency represents the visual priority of a comment badge.
// Higher values = more urgent = hotter colors.
type BadgeUrgency int

const (
	BadgeRead              BadgeUrgency = iota
	BadgeResolved
	BadgeUnread
	BadgeChangesRequested
)

// BadgeInfo holds caller-computed badge data for a comment thread position.
type BadgeInfo struct {
	Count   int
	Urgency BadgeUrgency
	Working bool
}

// CommentBadge is the aggregated badge data for a single diff line.
// A diff line may host threads on both LEFT and RIGHT sides (e.g. context
// lines); the badge aggregates them into a single pill.
type CommentBadge struct {
	TotalCount int
	MaxUrgency BadgeUrgency
	Working    bool         // when true, show animated spinner instead of count
	Keys       []CommentKey // thread positions this badge represents
}

const (
	badgeIcon     = "\U000f0188" // 󰆈 nf-md-comment_text_outline
	badgeCapLeft  = "\ue0b6"    // left rounded powerline cap
	badgeCapRight = "\ue0b4"    // right rounded powerline cap
)

var badgeSpinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// badgeColors returns the pill color and text fg color for an urgency level.
// When working is true, returns magenta (most prominent transient color).
func badgeColors(urgency BadgeUrgency, working bool, colors styles.DiffColors) (pill, textFg color.Color) {
	if working {
		return colors.PaletteMagenta, colors.PaletteFg
	}
	switch urgency {
	case BadgeChangesRequested:
		return colors.PaletteRed, colors.PaletteFg
	case BadgeUnread:
		return colors.PaletteYellow, colors.PaletteBg
	case BadgeResolved:
		return colors.PaletteCyan, colors.PaletteBg
	default: // BadgeRead
		return colors.PaletteDim, colors.PaletteFg
	}
}

// RenderBadgePill renders a floating rounded pill badge string.
// lineBg is the ANSI bg code of the diff line the badge sits on (used for
// the pill cap transitions). animFrame drives the spinner when Working is true.
func RenderBadgePill(badge *CommentBadge, lineBg string, animFrame int, colors styles.DiffColors) string {
	if badge == nil || badge.TotalCount == 0 {
		return ""
	}
	pill, textColor := badgeColors(badge.MaxUrgency, badge.Working, colors)
	pillFg := styles.ColorToFgCode(pill)
	pillBg := styles.ColorToBgCode(pill)
	textFg := styles.ColorToFgCode(textColor)

	content := strconv.Itoa(badge.TotalCount)
	if badge.Working {
		content = badgeSpinFrames[animFrame%len(badgeSpinFrames)]
	}

	var b strings.Builder
	// Left rounded cap: pill color on line bg
	b.WriteString(lineBg)
	b.WriteString(pillFg)
	b.WriteString(badgeCapLeft)
	// Pill content: text on pill bg
	b.WriteString(pillBg)
	b.WriteString(textFg)
	b.WriteString(" ")
	b.WriteString(badgeIcon)
	b.WriteString(" ")
	b.WriteString(content)
	b.WriteString(" ")
	// Right rounded cap: pill color on line bg
	b.WriteString(lineBg)
	b.WriteString(pillFg)
	b.WriteString(badgeCapRight)
	b.WriteString("\033[0m")
	return b.String()
}

// AggregateBadges reduces a per-position badge map to a single file-level badge.
// The result has the total thread count, the maximum urgency, and working=true
// if any position is working.
func AggregateBadges(badges map[CommentKey]BadgeInfo) BadgeInfo {
	var agg BadgeInfo
	for _, b := range badges {
		agg.Count += b.Count
		if b.Urgency > agg.Urgency {
			agg.Urgency = b.Urgency
		}
		agg.Working = agg.Working || b.Working
	}
	return agg
}

// TreeBadgeStyle returns the lipgloss style for a file tree badge based on
// urgency and working state, using the same color semantics as diff badges.
func TreeBadgeStyle(urgency BadgeUrgency, working bool) lipgloss.Style {
	if working {
		return lipgloss.NewStyle().Foreground(lipgloss.Magenta)
	}
	switch urgency {
	case BadgeChangesRequested:
		return lipgloss.NewStyle().Foreground(lipgloss.Red)
	case BadgeUnread:
		return lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	case BadgeResolved:
		return lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	}
}

// BadgeVisualWidth returns the rendered visual width of a badge pill by
// measuring the actual rendered string. This avoids hardcoding assumptions
// about glyph widths.
func BadgeVisualWidth(badge *CommentBadge, animFrame int, colors styles.DiffColors) int {
	if badge == nil || badge.TotalCount == 0 {
		return 0
	}
	pill := RenderBadgePill(badge, "", animFrame, colors)
	return lipgloss.Width(pill)
}

// OverlayBadge places a badge pill at the right edge of a rendered line.
// The line should already be padded to `width` by wrapRenderedLine.
// lineBg is the ANSI bg code for the diff line (for cap transitions and
// re-padding after truncation).
func OverlayBadge(line string, badge *CommentBadge, lineBg string, width int, animFrame int, colors styles.DiffColors) string {
	if badge == nil || badge.TotalCount == 0 {
		return line
	}
	pill := RenderBadgePill(badge, lineBg, animFrame, colors)
	pillW := lipgloss.Width(pill)

	// 1 char gap between content and badge
	totalReserved := pillW + 1
	if totalReserved >= width {
		return line // terminal too narrow for badge
	}

	truncW := width - totalReserved
	truncated := ansi.Truncate(line, truncW, "")
	// Re-pad with line bg to ensure clean ANSI state before pill
	padded := padWithBg(truncated, truncW, lineBg)
	return padded + lineBg + " " + pill
}
