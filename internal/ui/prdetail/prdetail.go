package prdetail

import (
	"fmt"
	"strings"
	"time"

	"github.com/blakewilliams/ghq/internal/github"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
)

type descRenderedMsg struct {
	content  string
	prNumber int
}

type Model struct {
	pr       github.PullRequest
	client   *github.CachedClient
	width    int
	height   int
	viewport viewport.Model
	ready    bool
}

func New(pr github.PullRequest, client *github.CachedClient, width, height int) Model {
	return Model{
		pr:     pr,
		client: client,
		width:  width,
		height: height,
	}
}

func (m Model) PRNumber() int {
	return m.pr.Number
}

func (m Model) PRTitle() string {
	return m.pr.Title
}

func (m Model) Init() tea.Cmd {
	body := m.pr.Body
	width := m.width
	prNumber := m.pr.Number
	return func() tea.Msg {
		rendered := renderMarkdown(body, width)
		return descRenderedMsg{content: rendered, prNumber: prNumber}
	}
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.SetWidth(m.width)
		m.viewport.SetHeight(m.contentHeight())
		body := m.pr.Body
		width := m.width
		prNumber := m.pr.Number
		return m, func() tea.Msg {
			rendered := renderMarkdown(body, width)
			return descRenderedMsg{content: rendered, prNumber: prNumber}
		}

	case descRenderedMsg:
		if msg.prNumber == m.pr.Number {
			m.viewport = viewport.New()
			m.viewport.SetWidth(m.width)
			m.viewport.SetHeight(m.contentHeight())
			m.viewport.SetContent(msg.content)
			m.ready = true
		}
		return m, nil
	}

	if m.ready {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

var (
	userStyle = lipgloss.NewStyle().UnderlineStyle(lipgloss.UnderlineDotted)
	dimStyle  = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
)

func (m Model) View() string {
	var b strings.Builder

	// Metadata line: "opened 5 days ago by @username"
	b.WriteString("\n")
	b.WriteString(m.renderMeta())
	b.WriteString("\n")

	prURL := fmt.Sprintf("https://github.com/%s/pull/%d", m.client.RepoFullName(), m.pr.Number)
	title := lipgloss.NewStyle().Bold(true).
		UnderlineStyle(lipgloss.UnderlineDotted).
		Hyperlink(prURL).
		Render(fmt.Sprintf("#%d %s", m.pr.Number, m.pr.Title))
	b.WriteString(title)
	b.WriteString("\n\n")

	if m.ready {
		b.WriteString(m.viewport.View())
	}

	return b.String()
}

func (m Model) renderMeta() string {
	pr := m.pr
	author := formatUser(pr.User)

	if pr.Merged && pr.MergedBy != nil {
		if pr.MergedBy.Login == pr.User.Login {
			return dimStyle.Render(fmt.Sprintf(
				"%s opened %s, and merged %s",
				author, relativeTime(pr.CreatedAt), relativeTime(*pr.MergedAt),
			))
		}
		merger := formatUser(*pr.MergedBy)
		return dimStyle.Render(fmt.Sprintf(
			"%s opened %s — %s merged %s",
			author, relativeTime(pr.CreatedAt), merger, relativeTime(*pr.MergedAt),
		))
	}

	if pr.State == "closed" && pr.ClosedAt != nil {
		return dimStyle.Render(fmt.Sprintf(
			"%s opened %s, closed %s",
			author, relativeTime(pr.CreatedAt), relativeTime(*pr.ClosedAt),
		))
	}

	verb := "opened"
	if pr.Draft {
		verb = "drafted"
	}
	return dimStyle.Render(fmt.Sprintf(
		"%s %s %s by %s",
		verb, relativeTime(pr.CreatedAt), dimStyle.Render("by"), author,
	))
}

func formatUser(u github.User) string {
	return userStyle.
		Hyperlink(fmt.Sprintf("https://github.com/%s", u.Login)).
		Render("@" + u.Login)
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	case d < 365*24*time.Hour:
		months := int(d.Hours() / 24 / 30)
		if months == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", months)
	default:
		years := int(d.Hours() / 24 / 365)
		if years == 1 {
			return "1 year ago"
		}
		return fmt.Sprintf("%d years ago", years)
	}
}

func (m Model) contentHeight() int {
	h := m.height - 4 // metadata + title + blank lines
	if h < 0 {
		return 0
	}
	return h
}

var markdownStyle = ansi.StyleConfig{
	Document: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{},
	},
	Heading: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
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
		Format: "\n────────\n",
	},
	Item: ansi.StylePrimitive{
		BlockPrefix: "• ",
	},
	Enumeration: ansi.StylePrimitive{
		BlockPrefix: ". ",
	},
	Task: ansi.StyleTask{
		Ticked:   "[x] ",
		Unticked: "[ ] ",
	},
	Link: ansi.StylePrimitive{
		Underline: boolPtr(true),
	},
	Code: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: "`",
			Suffix: "`",
		},
	},
	CodeBlock: ansi.StyleCodeBlock{
		StyleBlock: ansi.StyleBlock{
			Margin: uintPtr(2),
		},
	},
	BlockQuote: ansi.StyleBlock{
		Indent:      uintPtr(1),
		IndentToken: stringPtr("│ "),
	},
	List: ansi.StyleList{
		LevelIndent: 4,
	},
	Table: ansi.StyleTable{
		CenterSeparator: stringPtr("│"),
		ColumnSeparator: stringPtr("│"),
		RowSeparator:    stringPtr("─"),
	},
}

func boolPtr(b bool) *bool  { return &b }
func stringPtr(s string) *string { return &s }
func uintPtr(u uint) *uint  { return &u }

func renderMarkdown(body string, width int) string {
	if width <= 0 || body == "" {
		return body
	}
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
