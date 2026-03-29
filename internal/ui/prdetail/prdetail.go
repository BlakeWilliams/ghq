package prdetail

import (
	"fmt"
	"strings"
	"time"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/components"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
)

type prTab int

const (
	tabOverview prTab = iota
	tabCode
)

type descRenderedMsg struct {
	content  string
	prNumber int
}

type fileRenderedMsg struct {
	content  string
	index    int
	prNumber int
}

// prefetchDoneMsg signals that background prefetch of file contents completed.
type prefetchDoneMsg struct{}

type Model struct {
	pr     github.PullRequest
	client *github.CachedClient
	width  int
	height int
	tab    prTab

	// Overview tab
	overviewVP    viewport.Model
	overviewReady bool
	descContent   string
	comments      []github.IssueComment

	// Code tab
	codeVP         viewport.Model
	codeReady      bool
	files          []github.PullRequestFile
	renderedFiles  []string
	filesRendered  int
	filesLoading   bool
	currentFileIdx int

	// Shared
	filesListLoaded bool
	diffColors      styles.DiffColors
	waitingG        bool
}

func New(pr github.PullRequest, client *github.CachedClient, width, height int, diffColors styles.DiffColors) Model {
	return Model{
		pr:         pr,
		client:     client,
		diffColors: diffColors,
		width:  width,
		height: height,
		tab:    tabOverview,
	}
}

func (m Model) PRNumber() int {
	return m.pr.Number
}

func (m Model) PRTitle() string {
	return m.pr.Title
}

func (m *Model) SetDiffColors(c styles.DiffColors) {
	m.diffColors = c
}

func (m *Model) activeViewport() *viewport.Model {
	if m.tab == tabCode {
		return &m.codeVP
	}
	return &m.overviewVP
}

func (m Model) Tab() string {
	if m.tab == tabCode {
		return "Code"
	}
	return "Overview"
}

func (m Model) Init() tea.Cmd {
	body := m.pr.Body
	width := m.width - 4 // border (2) + padding (2)
	prNumber := m.pr.Number
	return tea.Batch(
		func() tea.Msg {
			rendered := renderMarkdown(body, width)
			return descRenderedMsg{content: rendered, prNumber: prNumber}
		},
		m.client.GetPullRequestFiles(m.pr.Number),
		m.client.GetIssueComments(m.pr.Number),
	)
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.overviewVP.SetWidth(m.width)
		m.overviewVP.SetHeight(m.height)
		m.codeVP.SetWidth(m.width)
		m.codeVP.SetHeight(m.height)
		body := m.pr.Body
		width := m.width - 4 // border + padding
		prNumber := m.pr.Number
		return m, func() tea.Msg {
			rendered := renderMarkdown(body, width)
			return descRenderedMsg{content: rendered, prNumber: prNumber}
		}

	case tea.KeyPressMsg:
		switch msg.String() {
		case "tab":
			if m.tab == tabOverview {
				m.tab = tabCode
				if m.filesListLoaded && !m.codeReady {
					return m, m.startFileRendering()
				}
				m.rebuildCode()
			} else {
				m.tab = tabOverview
			}
			return m, nil
		case "shift+tab":
			if m.tab == tabCode {
				m.tab = tabOverview
			} else {
				m.tab = tabCode
				if m.filesListLoaded && !m.codeReady {
					return m, m.startFileRendering()
				}
				m.rebuildCode()
			}
			return m, nil
		case "p", "h", "left":
			if m.tab == tabCode && m.currentFileIdx > 0 {
				m.currentFileIdx--
				m.rebuildCode()
				return m, nil
			}
		case "n", "l", "right":
			if m.tab == tabCode && m.currentFileIdx < len(m.files)-1 {
				m.currentFileIdx++
				m.rebuildCode()
				return m, nil
			}
		case "G":
			m.waitingG = false
			m.activeViewport().GotoBottom()
			return m, nil
		case "g":
			if m.waitingG {
				m.waitingG = false
				m.activeViewport().GotoTop()
				return m, nil
			}
			m.waitingG = true
			return m, nil
		default:
			m.waitingG = false
		}

	case descRenderedMsg:
		if msg.prNumber == m.pr.Number {
			m.descContent = msg.content
			m.rebuildOverview()
		}
		return m, nil

	case github.CommentsLoadedMsg:
		if msg.Number == m.pr.Number {
			// Reverse so newest comments appear first.
			comments := msg.Comments
			for i, j := 0, len(comments)-1; i < j; i, j = i+1, j-1 {
				comments[i], comments[j] = comments[j], comments[i]
			}
			m.comments = comments
			m.rebuildOverview()
		}
		return m, nil

	case github.PRFilesLoadedMsg:
		m.files = msg.Files
		m.renderedFiles = make([]string, len(msg.Files))
		m.filesListLoaded = true
		// Rebuild overview to show file summary.
		m.rebuildOverview()
		// Prefetch first 3 files into cache.
		cmds := m.prefetchFiles(3)
		// If already on Code tab, start rendering.
		if m.tab == tabCode {
			cmds = append(cmds, m.startFileRendering())
		}
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
		return m, nil

	case prefetchDoneMsg:
		return m, nil

	case fileRenderedMsg:
		if msg.prNumber != m.pr.Number || msg.index >= len(m.renderedFiles) {
			return m, nil
		}
		m.renderedFiles[msg.index] = msg.content
		m.filesRendered = msg.index + 1
		if m.filesRendered >= len(m.files) {
			m.filesLoading = false
		}
		m.rebuildCode()
		if m.filesRendered < len(m.files) {
			return m, m.renderFileCmd(m.filesRendered)
		}
		return m, nil

	case github.QueryErrMsg:
		return m, nil
	}

	if m.tab == tabOverview && m.overviewReady {
		var cmd tea.Cmd
		m.overviewVP, cmd = m.overviewVP.Update(msg)
		return m, cmd
	}
	if m.tab == tabCode && m.codeReady {
		var cmd tea.Cmd
		m.codeVP, cmd = m.codeVP.Update(msg)
		return m, cmd
	}
	return m, nil
}

var (
	userStyle      = lipgloss.NewStyle().UnderlineStyle(lipgloss.UnderlineDotted)
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	separatorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func (m Model) View() string {
	if m.tab == tabCode && m.codeReady {
		return m.codeVP.View()
	}
	if m.overviewReady {
		return m.overviewVP.View()
	}
	return ""
}

// --- Overview tab ---

func (m *Model) rebuildOverview() {
	var content strings.Builder

	// Description card with metadata in the top border.
	meta := " " + styles.PRStatusBadge(m.pr.State, m.pr.Draft, m.pr.Merged) +
		" " + m.renderMeta() + " "

	metaWidth := lipgloss.Width(meta)
	fillWidth := m.width - 2 - metaWidth - 1
	if fillWidth < 0 {
		fillWidth = 0
	}
	topBorder := borderColor.Render("╭─") + meta + borderColor.Render(strings.Repeat("─", fillWidth)+"╮")

	// Title + description inside the card.
	prURL := fmt.Sprintf("https://github.com/%s/pull/%d", m.client.RepoFullName(), m.pr.Number)
	title := lipgloss.NewStyle().Bold(true).
		UnderlineStyle(lipgloss.UnderlineDotted).
		Hyperlink(prURL).
		Render(fmt.Sprintf("#%d %s", m.pr.Number, m.pr.Title))

	descBody := m.descContent
	if descBody == "" {
		descBody = dimStyle.Render("No description provided.")
	}
	cardContent := title + "\n\n" + descBody
	bottom := commentBodyStyle.Width(m.width).Render(cardContent)

	content.WriteString("\n" + topBorder + "\n" + bottom)

	if len(m.comments) > 0 {
		label := fmt.Sprintf("%d comment", len(m.comments))
		if len(m.comments) != 1 {
			label += "s"
		}
		content.WriteString("\n\n")
		content.WriteString("  " + lipgloss.NewStyle().Bold(true).Render(label))
		content.WriteString("\n")

		for _, c := range m.comments {
			content.WriteString(m.renderComment(c))
		}
	}

	if !m.overviewReady {
		m.overviewVP = viewport.New()
		m.overviewReady = true
	}
	m.overviewVP.SetWidth(m.width)
	m.overviewVP.SetHeight(m.height)
	m.overviewVP.SetContent(content.String())
}

// --- Code tab ---

func (m Model) startFileRendering() tea.Cmd {
	if len(m.files) == 0 || m.filesRendered > 0 {
		return nil
	}
	m.filesLoading = true
	return m.renderFileCmd(0)
}

func (m Model) renderFileCmd(index int) tea.Cmd {
	f := m.files[index]
	ref := m.pr.Head.SHA
	prNumber := m.pr.Number
	width := m.width
	client := m.client
	colors := m.diffColors

	return func() tea.Msg {
		var fileContent string
		if f.Status != "removed" && f.Patch != "" {
			if content, err := client.FetchFileContent(f.Filename, ref); err == nil {
				fileContent = content
			}
		}
		rendered := components.RenderDiffFile(f, fileContent, width, colors)
		return fileRenderedMsg{content: rendered, index: index, prNumber: prNumber}
	}
}

func (m *Model) rebuildCode() {
	var content strings.Builder

	// File position indicator.
	if len(m.files) > 0 {
		pos := dimStyle.Render(fmt.Sprintf("File %d of %d", m.currentFileIdx+1, len(m.files)))
		nav := dimStyle.Render("← p  n →")
		gap := m.width - lipgloss.Width(pos) - lipgloss.Width(nav)
		if gap < 1 {
			gap = 1
		}
		content.WriteString(pos + strings.Repeat(" ", gap) + nav)
		content.WriteString("\n\n")
	}

	idx := m.currentFileIdx
	if idx < m.filesRendered {
		content.WriteString(m.renderedFiles[idx])
	} else {
		content.WriteString(dimStyle.Render("  Loading..."))
	}

	// Next file hint below the current file.
	if idx < len(m.files)-1 {
		next := m.files[idx+1]
		content.WriteString("\n\n")
		hint := dimStyle.Render("n → ") + dimStyle.Render(next.Filename)
		content.WriteString(hint)
	}

	if !m.codeReady {
		m.codeVP = viewport.New()
		m.codeReady = true
	}
	m.codeVP.SetWidth(m.width)
	m.codeVP.SetHeight(m.height)
	m.codeVP.SetContent(content.String())
}

// prefetchFiles kicks off background fetches for the first n files' content,
// warming the cache so Code tab renders are fast.
func (m Model) prefetchFiles(n int) []tea.Cmd {
	limit := n
	if limit > len(m.files) {
		limit = len(m.files)
	}
	if limit == 0 {
		return nil
	}

	ref := m.pr.Head.SHA
	client := m.client
	var cmds []tea.Cmd
	for i := 0; i < limit; i++ {
		f := m.files[i]
		if f.Status == "removed" || f.Patch == "" {
			continue
		}
		filename := f.Filename
		cmds = append(cmds, func() tea.Msg {
			client.FetchFileContent(filename, ref)
			return prefetchDoneMsg{}
		})
	}
	return cmds
}

// --- Comments ---

var (
	commentBodyStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderTop(false).
				BorderForeground(lipgloss.BrightBlack).
				Padding(0, 1)

	commentAuthor = lipgloss.NewStyle().Bold(true)
	authorBadge   = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Black).
			Background(lipgloss.Yellow)

	borderColor = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
)

func (m Model) renderComment(c github.IssueComment) string {
	name := userStyle.
		Hyperlink(fmt.Sprintf("https://github.com/%s", c.User.Login)).
		Render("@" + c.User.Login)
	author := commentAuthor.Render(name)

	if c.User.Login == m.pr.User.Login {
		author += " " + authorBadge.Render(" Author ")
	}

	age := dimStyle.Render(relativeTime(c.CreatedAt))
	title := " " + author + " " + age + " "

	// Build top border with title embedded: ╭─ @author 2d ───╮
	// Chrome: ╭─ (2) + title + ─...─╮ (fill+1) = width
	titleWidth := lipgloss.Width(title)
	fillWidth := m.width - 2 - titleWidth - 1
	if fillWidth < 0 {
		fillWidth = 0
	}
	topBorder := borderColor.Render("╭─") + title + borderColor.Render(strings.Repeat("─", fillWidth)+"╮")

	// Render body inside bottom border (no top border).
	bodyWidth := m.width - 4 // border + padding
	if bodyWidth < 20 {
		bodyWidth = 20
	}
	body := renderMarkdown(c.Body, bodyWidth)
	bottom := commentBodyStyle.Width(m.width).Render(body)

	return "\n" + topBorder + "\n" + bottom
}

// --- Separator ---

func (m Model) renderFileSeparator() string {
	w := m.width
	if w < 10 {
		w = 10
	}

	fileCount := len(m.files)
	var totalAdd, totalDel int
	for _, f := range m.files {
		totalAdd += f.Additions
		totalDel += f.Deletions
	}

	left := fmt.Sprintf("%d File", fileCount)
	if fileCount != 1 {
		left += "s"
	}

	additions := fmt.Sprintf("+%d", totalAdd)
	deletions := fmt.Sprintf("-%d", totalDel)
	right := lipgloss.NewStyle().Foreground(lipgloss.Green).Render(additions) +
		" " +
		lipgloss.NewStyle().Foreground(lipgloss.Red).Render(deletions)

	rightPlain := fmt.Sprintf("+%d -%d", totalAdd, totalDel)
	gap := w - lipgloss.Width(left) - len(rightPlain)
	if gap < 1 {
		gap = 1
	}

	line := separatorStyle.Render(left) + strings.Repeat(" ", gap) + right
	separator := separatorStyle.Render(strings.Repeat("─", w))

	return "\n" + separator + "\n" + line + "\n"
}

// --- Meta / User ---

func (m Model) renderMeta() string {
	pr := m.pr
	author := formatUser(pr.User)

	if pr.Merged && pr.MergedBy != nil {
		if pr.MergedBy.Login == pr.User.Login {
			return dimStyle.Render(fmt.Sprintf(
				"%s by %s",
				relativeTime(*pr.MergedAt), author,
			))
		}
		merger := formatUser(*pr.MergedBy)
		return dimStyle.Render(fmt.Sprintf(
			"%s by %s",
			relativeTime(*pr.MergedAt), merger,
		))
	}

	if pr.State == "closed" && pr.ClosedAt != nil {
		return dimStyle.Render(fmt.Sprintf(
			"%s by %s",
			relativeTime(*pr.ClosedAt), author,
		))
	}

	return dimStyle.Render(fmt.Sprintf(
		"%s by %s",
		relativeTime(pr.CreatedAt), author,
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

// --- Glamour ---

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

func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }
func uintPtr(u uint) *uint       { return &u }

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
