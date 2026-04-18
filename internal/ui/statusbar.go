package ui

import (
	"fmt"
	"image/color"

	"charm.land/lipgloss/v2"
)

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

// chromeColor computes the border/separator color derived from the terminal
// background. Slightly lighter than bg in dark mode, slightly darker in light.
func (m Model) chromeColor() color.Color {
	if m.termBg == nil {
		if m.hasDarkBg {
			return lipgloss.BrightBlack
		}
		return lipgloss.BrightBlack
	}
	r, g, b, _ := m.termBg.RGBA()
	pct := 40.0
	if !m.hasDarkBg {
		pct = -20.0
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
