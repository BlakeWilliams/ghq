package prdetail

import (
	"regexp"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
)
// --- Glamour ---

var markdownStyle = ansi.StyleConfig{
	Document: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{},
	},
	Heading: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			BlockSuffix: "\n",
			Color:       stringPtr("5"), // magenta
			Bold:        boolPtr(true),
		},
	},
	H1: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	H2: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "## ",
			Bold:   boolPtr(true),
		},
	},
	H3: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "### ",
			Bold:   boolPtr(true),
		},
	},
	Emph: ansi.StylePrimitive{
		Italic: boolPtr(true),
	},
	Strong: ansi.StylePrimitive{
		Bold: boolPtr(true),
	},
	Strikethrough: ansi.StylePrimitive{
		CrossedOut: boolPtr(true),
	},
	HorizontalRule: ansi.StylePrimitive{
		Color:  stringPtr("8"), // bright black
		Format: "\n────────\n",
	},
	Item: ansi.StylePrimitive{
		BlockPrefix: "• ",
	},
	Enumeration: ansi.StylePrimitive{
		BlockPrefix: ". ",
	},
	Task: ansi.StyleTask{
		Ticked:   "\U000f0132 ", // 󰄲 nf-md-checkbox_marked
		Unticked: "\ue640 ",    // nf-seti-checkbox_unchecked
		StylePrimitive: ansi.StylePrimitive{
			Color: stringPtr("2"), // green
		},
	},
	Link: ansi.StylePrimitive{
		// Hide visible URL — link text already has OSC 8 hyperlink.
		Format: "{{/*hidden*/}}",
	},
	LinkText: ansi.StylePrimitive{
		Color:     stringPtr("4"), // blue
		Bold:      boolPtr(true),
		Underline: boolPtr(true),
	},
	Code: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color:  stringPtr("3"), // yellow
			Prefix: "`",
			Suffix: "`",
		},
	},
	CodeBlock: ansi.StyleCodeBlock{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr("8"), // bright black
			},
			Margin: uintPtr(2),
		},
	},
	BlockQuote: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color:  stringPtr("8"), // bright black
			Italic: boolPtr(true),
		},
		Indent:      uintPtr(1),
		IndentToken: stringPtr("│ "),
	},
	List: ansi.StyleList{
		StyleBlock: ansi.StyleBlock{
			Indent: uintPtr(2),
		},
		LevelIndent: 4,
	},
	Table: ansi.StyleTable{
		CenterSeparator: stringPtr("│"),
		ColumnSeparator: stringPtr("│"),
		RowSeparator:    stringPtr("─"),
	},
}

func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }
func uintPtr(u uint) *uint       { return &u }

var (
	// reImage matches markdown images: ![alt](url)
	reImage = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	// reHTMLImg matches <img ...> tags
	reHTMLImg = regexp.MustCompile(`(?i)<img[^>]*>`)
	// reHTMLVideo matches <video ...>...</video> and self-closing <video ... />
	reHTMLVideo = regexp.MustCompile(`(?is)<video[^>]*(?:/>|>.*?</video>)`)
	// reHTMLPicture matches <picture>...</picture>
	reHTMLPicture = regexp.MustCompile(`(?is)<picture>.*?</picture>`)
	// reBareAssetURL matches bare GitHub asset URLs on their own line (video/image embeds).
	reBareAssetURL = regexp.MustCompile(`(?m)^\s*(https://github\.com/user-attachments/assets/\S+)\s*$`)
)

func renderMarkdown(body string, width int) string {
	if width <= 0 || body == "" {
		return body
	}

	// Convert markdown images to short links.
	body = reImage.ReplaceAllStringFunc(body, func(match string) string {
		sub := reImage.FindStringSubmatch(match)
		text := sub[1]
		if text == "" {
			text = "image"
		}
		return "[" + text + "](" + sub[2] + ")"
	})
	// Strip HTML media tags.
	body = reHTMLPicture.ReplaceAllString(body, "")
	body = reHTMLVideo.ReplaceAllString(body, "")
	body = reHTMLImg.ReplaceAllString(body, "")
	// Convert bare GitHub asset URLs (video/image embeds) to short links.
	body = reBareAssetURL.ReplaceAllString(body, "[attached media]($1)")

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return body
	}
	rendered, err := renderer.Render(body)
	if err != nil {
		return body
	}
	return strings.TrimSpace(rendered)
}
