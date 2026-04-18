package ui

import (
	"fmt"
	"image/color"
	"strings"

	"github.com/blakewilliams/ghq/internal/ui/localdiff"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	"charm.land/lipgloss/v2"
)

// Powerline separator characters (require nerd font).
const (
	plRight = "\ue0b0" // right-pointing solid arrow
	plLeft  = "\ue0b2" // left-pointing solid arrow
)

func (m Model) renderStatusBar() string {
	ld, isLocal := m.activeView.(localdiff.Model)

	if !isLocal {
		return strings.Repeat(" ", m.width)
	}

	branch := ld.BranchName()
	mode := ld.DiffMode()
	modeBg := styles.ModeColor(mode)

	modeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Black).Background(modeBg)

	// Bar background derived from terminal bg.
	barBg := m.barBackground()
	barFg := m.barForeground()
	barStyle := lipgloss.NewStyle().Foreground(barFg).Background(barBg)
	branchStyle := lipgloss.NewStyle().Foreground(barFg).Background(barBg)

	// Left: MODE ▶ • branch
	modeText := modeStyle.Render(" " + strings.ToUpper(mode.String()) + " ")
	modeToBar := lipgloss.NewStyle().Foreground(modeBg).Background(barBg).Render(plRight)
	branchText := branchStyle.Render(" \u2022 " + branch + " ")

	left := modeText + modeToBar + branchText

	// Right side: PR badge
	var right string
	if pr := ld.PR(); pr != nil {
		prBg := lipgloss.Cyan
		prStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Black).Background(prBg)
		barToPr := lipgloss.NewStyle().Foreground(prBg).Background(barBg).Render(plLeft)
		right = barToPr + prStyle.Render(fmt.Sprintf(" PR #%d ", pr.Number))
	}

	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	gap := m.width - leftW - rightW
	if gap < 0 {
		gap = 0
	}

	return left + barStyle.Render(strings.Repeat(" ", gap)) + right
}

// brightnessModify applies lualine's brightness_modifier formula:
//
//	channel = clamp(channel + channel * pct / 100, 0, 255)
//
// Positive pct lightens, negative darkens. This is proportional — brighter
// channels shift more than darker ones, preserving the color's hue/warmth.
func brightnessModify(r, g, b float64, pct float64) (int, int, int) {
	return clampByte(int(r + r*pct/100)),
		clampByte(int(g + g*pct/100)),
		clampByte(int(b + b*pct/100))
}

// barBackground computes the status bar background matching lualine's auto
// theme: brightness_modifier(Normal bg, ±10%).
func (m Model) barBackground() color.Color {
	if m.termBg == nil {
		if m.hasDarkBg {
			return lipgloss.Black
		}
		return lipgloss.White
	}
	r, g, b, _ := m.termBg.RGBA()
	pct := 30.0
	if !m.hasDarkBg {
		pct = -15.0
	}
	rr, gg, bb := brightnessModify(float64(r>>8), float64(g>>8), float64(b>>8), pct)
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", rr, gg, bb))
}

// barForeground computes the status bar text color. Lualine's auto theme
// uses Normal fg, then iteratively adjusts contrast until
// |avg(fg) - avg(bg)| >= 0.3. We approximate with a large brightness shift.
func (m Model) barForeground() color.Color {
	if m.termBg == nil {
		if m.hasDarkBg {
			return lipgloss.BrightBlack
		}
		return lipgloss.BrightBlack
	}
	r, g, b, _ := m.termBg.RGBA()
	pct := 500.0
	if !m.hasDarkBg {
		pct = -60.0
	}
	rr, gg, bb := brightnessModify(float64(r>>8), float64(g>>8), float64(b>>8), pct)
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", rr, gg, bb))
}

func clampByte(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
