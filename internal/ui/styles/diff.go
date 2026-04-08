package styles

import (
	"fmt"
	"image/color"

	"github.com/alecthomas/chroma/v2"
	"github.com/blakewilliams/ghq/internal/terminal"
)

// DiffColors holds pre-computed raw ANSI escape codes for diff line rendering.
type DiffColors struct {
	AddBg    string // bg code for added lines (gutter + code)
	AddFg    string // fg code for add marker/line numbers
	DelBg    string // bg code for deleted lines (gutter + code)
	DelFg    string // fg code for del marker/line numbers
	HunkBg   string // bg code for hunk headers
	HunkFg   string // fg code for hunk header text
	SelectBg string // raw ANSI bg code for selected context lines

	// Selected line bg codes — more prominent tints for cursor highlight.
	SelectedAddBg string // bg for selected add lines
	SelectedDelBg string // bg for selected del lines
	SelectedCtxBg string // bg for selected context lines

	// SelectColor is the computed selection bg as a color.Color for use with lipgloss.
	SelectColor color.Color

	// BorderFg is a raw ANSI fg code for borders (derived from BrightBlack).
	BorderFg string
	// BorderColor is the border color as a color.Color for use with lipgloss.
	BorderColor color.Color
	// HighlightBorderFg is a raw ANSI fg code for highlighted borders (from palette yellow).
	HighlightBorderFg string

	// ChromaStyle is a chroma style built from the terminal palette,
	// suitable for syntax highlighting on both normal and tinted backgrounds.
	ChromaStyle *chroma.Style

	// Palette colors for use with lipgloss (resolved from terminal).
	PaletteGreen   color.Color
	PaletteRed     color.Color
	PaletteYellow  color.Color
	PaletteCyan    color.Color
	PaletteMagenta color.Color
	PaletteBg      color.Color
	PaletteFg      color.Color // bright white or equivalent
	PaletteDim     color.Color // mid-gray, safe on light and dark
}

// ComputeDiffColors derives colors from the terminal's resolved palette.
func ComputeDiffColors(p terminal.Palette) DiffColors {
	bg := p.Get(terminal.Black)
	if bg == nil {
		// No bg resolved — use a dark fallback.
		bg = color.RGBA{R: 30, G: 30, B: 30, A: 255}
	}

	green := p.Get(terminal.Green)
	red := p.Get(terminal.Red)
	blue := p.Get(terminal.Blue)
	white := p.Get(terminal.BrightWhite)
	yellow := p.Get(terminal.Yellow)
	cyan := p.Get(terminal.Cyan)
	magenta := p.Get(terminal.Magenta)
	brightBlack := p.Get(terminal.BrightBlack)

	// Compute selection bg: slightly lighter on dark, slightly darker on light.
	bgLum := relativeLuminance(bg)
	var selectTint color.Color
	if bgLum < 0.5 {
		selectTint = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	} else {
		selectTint = color.RGBA{R: 0, G: 0, B: 0, A: 255}
	}
	selectBg := blendColor(selectTint, bg, 0.20)

	borderColor := brightBlack
	if borderColor == nil {
		borderColor = color.RGBA{R: 128, G: 128, B: 128, A: 255}
	}

	// Compute a safe mid-gray for dimmed badges — ensures contrast on both light/dark.
	dimColor := blendColor(selectTint, bg, 0.35)

	colors := DiffColors{
		SelectBg:      colorToBgCode(selectBg),
		SelectedCtxBg: colorToBgCode(selectBg),
		SelectColor:   selectBg,
		BorderFg:           colorToFgCode(borderColor),
		BorderColor:        borderColor,
		HighlightBorderFg:  colorToFgCode(orDefault(yellow, color.RGBA{R: 200, G: 180, B: 0, A: 255})),
		PaletteGreen:   orDefault(green, color.RGBA{R: 0, G: 180, B: 0, A: 255}),
		PaletteRed:     orDefault(red, color.RGBA{R: 220, G: 50, B: 50, A: 255}),
		PaletteYellow:  orDefault(yellow, color.RGBA{R: 200, G: 180, B: 0, A: 255}),
		PaletteCyan:    orDefault(cyan, color.RGBA{R: 0, G: 180, B: 200, A: 255}),
		PaletteMagenta: orDefault(magenta, color.RGBA{R: 180, G: 0, B: 180, A: 255}),
		PaletteBg:      bg,
		PaletteFg:      orDefault(white, color.RGBA{R: 255, G: 255, B: 255, A: 255}),
		PaletteDim:     dimColor,
	}

	// Subtle bg tints.
	if green != nil {
		addBg := blendColor(green, bg, 0.08)
		colors.AddBg = colorToBgCode(addBg)
		colors.AddFg = colorToFgCode(ensureContrast(green, addBg))
		// Selected add: stronger green tint.
		selectedAddBg := blendColor(green, bg, 0.25)
		colors.SelectedAddBg = colorToBgCode(selectedAddBg)
	} else {
		colors.AddBg = "\033[48;5;22m"
		colors.AddFg = "\033[32m"
		colors.SelectedAddBg = "\033[48;5;28m"
	}

	if red != nil {
		delBg := blendColor(red, bg, 0.08)
		colors.DelBg = colorToBgCode(delBg)
		colors.DelFg = colorToFgCode(ensureContrast(red, delBg))
		// Selected del: stronger red tint.
		selectedDelBg := blendColor(red, bg, 0.25)
		colors.SelectedDelBg = colorToBgCode(selectedDelBg)
	} else {
		colors.DelBg = "\033[48;5;52m"
		colors.DelFg = "\033[31m"
		colors.SelectedDelBg = "\033[48;5;88m"
	}

	if blue != nil && white != nil {
		hunkBg := blendColor(blue, bg, 0.10)
		colors.HunkBg = colorToBgCode(hunkBg)
		colors.HunkFg = colorToFgCode(ensureContrast(white, hunkBg))
	} else {
		colors.HunkBg = "\033[48;5;17m"
		colors.HunkFg = "\033[97;1m"
	}

	// Build chroma style from palette colors.
	colors.ChromaStyle = buildChromaStyle(p, bg, white, red, green, blue, yellow, cyan, magenta, brightBlack)

	return colors
}

func buildChromaStyle(p terminal.Palette, bg, white, red, green, blue, yellow, cyan, magenta, brightBlack color.Color) *chroma.Style {
	hex := func(c color.Color) string {
		if c == nil {
			return ""
		}
		r, g, b, _ := c.RGBA()
		return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	}

	// Use palette colors but slightly brighten them so they read on tinted bgs.
	brightGreen := p.Get(terminal.BrightGreen)
	brightRed := p.Get(terminal.BrightRed)
	brightYellow := p.Get(terminal.BrightYellow)
	brightCyan := p.Get(terminal.BrightCyan)
	brightMagenta := p.Get(terminal.BrightMagenta)
	brightBlue := p.Get(terminal.BrightBlue)

	// Pick the brighter variant when available for better readability.
	keyword := pick(brightMagenta, magenta)
	str := pick(brightGreen, green)
	number := pick(brightCyan, cyan)
	comment := brightBlack
	funcName := pick(brightBlue, blue)
	typ := pick(brightCyan, cyan)
	op := pick(brightYellow, yellow)
	text := white
	deleted := pick(brightRed, red)
	inserted := pick(brightGreen, green)

	builder := chroma.NewStyleBuilder("ghq")
	builder.Add(chroma.Text, hex(text))
	builder.Add(chroma.Keyword, "bold "+hex(keyword))
	builder.Add(chroma.KeywordType, hex(typ))
	builder.Add(chroma.KeywordNamespace, hex(keyword))
	builder.Add(chroma.KeywordReserved, "bold "+hex(keyword))
	builder.Add(chroma.NameFunction, hex(funcName))
	builder.Add(chroma.NameClass, "bold "+hex(funcName))
	builder.Add(chroma.NameBuiltin, hex(funcName))
	builder.Add(chroma.NameDecorator, hex(op))
	builder.Add(chroma.NameTag, hex(keyword))
	builder.Add(chroma.NameAttribute, hex(funcName))
	builder.Add(chroma.LiteralString, hex(str))
	builder.Add(chroma.LiteralStringEscape, hex(op))
	builder.Add(chroma.LiteralNumber, hex(number))
	builder.Add(chroma.Comment, "italic "+hex(comment))
	builder.Add(chroma.CommentPreproc, hex(op))
	builder.Add(chroma.Operator, hex(op))
	builder.Add(chroma.Punctuation, hex(text))
	builder.Add(chroma.Name, hex(text))
	builder.Add(chroma.GenericDeleted, hex(deleted))
	builder.Add(chroma.GenericInserted, hex(inserted))
	builder.Add(chroma.GenericEmph, "italic")
	builder.Add(chroma.GenericStrong, "bold")
	builder.Add(chroma.GenericSubheading, hex(comment))

	style, err := builder.Build()
	if err != nil {
		return nil
	}
	return style
}

func orDefault(c color.Color, fallback color.Color) color.Color {
	if c != nil {
		return c
	}
	return fallback
}

func pick(preferred, fallback color.Color) color.Color {
	if preferred != nil {
		return preferred
	}
	return fallback
}

// blendColor blends fg into bg at the given alpha (0.0 = pure bg, 1.0 = pure fg).
func blendColor(fg, bg color.Color, alpha float64) color.Color {
	fr, fgG, fb, _ := fg.RGBA()
	br, bgG, bb, _ := bg.RGBA()

	r := uint8(float64(fr>>8)*alpha + float64(br>>8)*(1-alpha))
	g := uint8(float64(fgG>>8)*alpha + float64(bgG>>8)*(1-alpha))
	b := uint8(float64(fb>>8)*alpha + float64(bb>>8)*(1-alpha))

	return color.RGBA{R: r, G: g, B: b, A: 255}
}

// ensureContrast brightens fg if it doesn't have enough contrast against bg.
func ensureContrast(fg, bg color.Color) color.Color {
	if relativeLuminance(fg)-relativeLuminance(bg) > 0.15 {
		return fg
	}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	return blendColor(white, fg, 0.3)
}

func relativeLuminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	return 0.2126*float64(r>>8)/255 + 0.7152*float64(g>>8)/255 + 0.0722*float64(b>>8)/255
}

func colorToBgCode(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("\033[48;2;%d;%d;%dm", r>>8, g>>8, b>>8)
}

func colorToFgCode(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r>>8, g>>8, b>>8)
}
