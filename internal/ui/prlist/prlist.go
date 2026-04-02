package prlist

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/components"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	"github.com/blakewilliams/ghq/internal/ui/uictx"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type PRSelectedMsg struct {
	PR github.PullRequest
}

type prItem struct {
	pr github.PullRequest
}

func (i prItem) Title() string {
	return i.pr.Title
}

func (i prItem) Description() string {
	return i.pr.User.Login
}

func (i prItem) FilterValue() string {
	return i.pr.Title
}

const (
	iconArrowRight = "\U000f0054" // 󰁔 nf-md-arrow_right
)

var (
	labelStyle = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
)

// rowStyles holds the lipgloss styles for a single row, parameterized by
// an optional background color for the selected state.
type rowStyles struct {
	dim    lipgloss.Style
	number lipgloss.Style
	title  lipgloss.Style
	label  lipgloss.Style
	row    lipgloss.Style // full-width row wrapper
}

func makeRowStyles(bg color.Color) rowStyles {
	base := lipgloss.NewStyle()
	if bg != nil {
		base = base.Background(bg)
	}
	return rowStyles{
		dim:    base.Foreground(lipgloss.BrightBlack),
		number: base.Foreground(lipgloss.BrightBlack),
		title:  base.Bold(true),
		label:  base.Foreground(lipgloss.BrightBlack),
		row:    base,
	}
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/24/365))
	}
}

func renderLabels(labels []github.Label) string {
	if len(labels) == 0 {
		return ""
	}
	var parts []string
	for _, l := range labels {
		bg := lipgloss.Color("#" + l.Color)
		// Pick black or white foreground based on luminance.
		fg := lipgloss.Color("#fff")
		if isLightColor(l.Color) {
			fg = lipgloss.Color("#000")
		}
		pill := lipgloss.NewStyle().
			Background(bg).
			Foreground(fg).
			Padding(0, 1).
			Render(l.Name)
		parts = append(parts, pill)
	}
	return strings.Join(parts, " ")
}

func isLightColor(hex string) bool {
	if len(hex) != 6 {
		return false
	}
	r, _ := strconv.ParseUint(hex[0:2], 16, 8)
	g, _ := strconv.ParseUint(hex[2:4], 16, 8)
	b, _ := strconv.ParseUint(hex[4:6], 16, 8)
	// Relative luminance.
	lum := 0.2126*float64(r)/255 + 0.7152*float64(g)/255 + 0.0722*float64(b)/255
	return lum > 0.5
}

type Model struct {
	list   list.Model
	ctx    *uictx.Context
	width  int
	height int
	err    error
}

func New(ctx *uictx.Context) Model {
	delegate := list.NewDefaultDelegate()
	delegate.Styles.NormalTitle = lipgloss.NewStyle()
	delegate.Styles.NormalDesc = lipgloss.NewStyle()
	delegate.Styles.SelectedTitle = lipgloss.NewStyle()
	delegate.Styles.SelectedDesc = lipgloss.NewStyle()
	delegate.SetSpacing(0)
	delegate.SetHeight(3)

	l := list.New(nil, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(true)
	l.SetSpinner(spinner.Dot)
	l.Styles.Spinner = lipgloss.NewStyle().Foreground(lipgloss.Magenta)

	return Model{
		list: l,
		ctx:  ctx,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.list.StartSpinner(), m.ctx.Client.ListPullRequests())
}

func (m Model) Update(msg tea.Msg) (uictx.View, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width, msg.Height-1)

	case github.PRsLoadedMsg:
		items := make([]list.Item, len(msg.PRs))
		for i, pr := range msg.PRs {
			items[i] = prItem{pr: pr}
		}
		cmd := m.list.SetItems(items)
		m.list.StopSpinner()
		return m, cmd

	case github.QueryErrMsg:
		m.err = msg.Err
		m.list.StopSpinner()

	case tea.KeyPressMsg:
		var cmd tea.Cmd
		var handled bool
		m, cmd, handled = m.handleKey(msg)
		if handled {
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) HandleKey(msg tea.KeyPressMsg) (uictx.View, tea.Cmd, bool) {
	return m.handleKey(msg)
}

func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	switch msg.String() {
	case "enter":
		if !m.list.SettingFilter() {
			if item := m.list.SelectedItem(); item != nil {
				if pi, ok := item.(prItem); ok {
					return m, func() tea.Msg {
						return PRSelectedMsg{PR: pi.pr}
					}, true
				}
			}
		}
	}
	return m, nil, false
}

var (
	listBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240"))
)

func (m Model) View() string {
	if m.err != nil {
		return fmt.Sprintf("Error: %v\n\nPress q to quit.", m.err)
	}

	normalStyles := makeRowStyles(nil)
	selectedStyles := makeRowStyles(nil)

	items := m.list.VisibleItems()
	selected := m.list.Index()
	w := m.width
	if w < 20 {
		w = 80
	}

	// Loading state: skeleton rows matching actual PR row layout.
	if len(m.list.Items()) == 0 {
		dim := normalStyles.dim
		innerW := w - 4
		sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		titleWidths := []int{25, 32, 18, 28, 22}

		var loadingLines []string
		for i := 0; i < 5; i++ {
			line1 := dim.Render("  " + strings.Repeat("─", 4) + " " + strings.Repeat("─", titleWidths[i]))
			line2 := dim.Render("  " + strings.Repeat("─", 8) + " " + strings.Repeat("─", 6) + " · " + strings.Repeat("─", 12))
			loadingLines = append(loadingLines, padLine(line1, innerW, lipgloss.NewStyle()))
			loadingLines = append(loadingLines, padLine(line2, innerW, lipgloss.NewStyle()))
			if i < 4 {
				loadingLines = append(loadingLines, sepStyle.Render(strings.Repeat("─", innerW)))
			}
		}
		content := strings.Join(loadingLines, "\n")
		bordered := listBorder.Render(content)
		return dim.Render(" Loading...") + "\n" + bordered
	}

	// Inner width accounts for the border (1 char each side).
	innerW := w - 2

	// Status line above the border takes 1 line, border takes 2 lines (top + bottom).
	const linesPerItem = 3
	maxVisible := (m.height - 3) / linesPerItem // -3 for status line + top/bottom border
	if maxVisible < 1 {
		maxVisible = 1
	}

	start := 0
	if selected >= maxVisible {
		start = selected - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(items) {
		end = len(items)
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		item := items[i]
		pi := item.(prItem)
		pr := pi.pr
		isSelected := i == selected

		s := normalStyles
		if isSelected {
			s = selectedStyles
		}

		verb := styles.PRStatusBadge(pr.State, pr.Draft, pr.Merged)

		num := s.number.Render(fmt.Sprintf("#%d", pr.Number))
		title := pr.Title
		if isSelected {
			title = s.title.Render(title)
		}
		age := s.dim.Render(relativeTime(pr.CreatedAt))

		prefix := " "
		if isSelected {
			prefix = lipgloss.NewStyle().Foreground(lipgloss.Magenta).Render("│")
		}

		// Line 1: #number title                             time
		line1 := prefix + " " + num + " " + title
		gap1 := innerW - lipgloss.Width(line1) - lipgloss.Width(age) - 1
		if gap1 < 1 {
			gap1 = 1
		}
		line1 += s.row.Render(strings.Repeat(" ", gap1)) + age

		// Line 2: @user opened · branch → base · labels
		user := components.ColoredAuthor(pr.User.Login)
		branch := s.dim.Render(pr.Head.Ref + " " + iconArrowRight + " " + pr.Base.Ref)
		line2 := prefix + " " + user + " " + verb + s.dim.Render(" · ") + branch
		if labels := renderLabels(pr.Labels); labels != "" {
			line2 += s.dim.Render(" · ") + labels
		}

		if isSelected {
			b.WriteString(padLine(line1, innerW, s.row) + "\n")
			b.WriteString(padLine(line2, innerW, s.row) + "\n")
		} else {
			b.WriteString(padLine(line1, innerW, lipgloss.NewStyle()) + "\n")
			b.WriteString(padLine(line2, innerW, lipgloss.NewStyle()) + "\n")
		}

		if i < end-1 {
			sep := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("─", innerW))
			b.WriteString(sep + "\n")
		}
	}

	total := len(m.list.Items())
	visible := len(items)
	status := fmt.Sprintf(" %d pull requests", total)
	if visible != total {
		status = fmt.Sprintf(" %d of %d pull requests", visible, total)
	}

	content := strings.TrimRight(b.String(), "\n")
	bordered := listBorder.Render(content)

	return normalStyles.dim.Render(status) + "\n" + bordered
}

// padLine pads a line to full width with the row style's background.
func padLine(s string, width int, rowStyle lipgloss.Style) string {
	pad := width - lipgloss.Width(s)
	if pad < 0 {
		pad = 0
	}
	return s + rowStyle.Render(strings.Repeat(" ", pad))
}
