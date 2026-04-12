package ui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/inbox"
	"github.com/blakewilliams/ghq/internal/notify"
	"github.com/blakewilliams/ghq/internal/cache/persist"
	"github.com/blakewilliams/ghq/internal/ui/components"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	"github.com/blakewilliams/ghq/internal/ui/uictx"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// PRSelectedMsg is sent when the user selects a PR from the inbox.
type PRSelectedMsg struct {
	Owner  string
	Repo   string
	Number int
}

// InboxLoadedMsg is sent when the inbox PRs are loaded and enriched.
type InboxLoadedMsg struct {
	PRs []github.InboxPR
}

func fetchInbox(c *github.CachedClient, username, nwo string) tea.Cmd {
	return func() tea.Msg {
		prs, err := c.FetchInbox(username, nwo)
		if err != nil {
			return uictx.QueryErrMsg{Err: err}
		}
		return InboxLoadedMsg{PRs: prs}
	}
}

type filter string

const (
	filterAll  filter = "all"
	filterRepo filter = "repo"
)

var (
	dimStyle = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	sepStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	listBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240"))
)

const iconArrowRight = "\U000f0054" // 󰁔

type Model struct {
	ctx      *uictx.Context
	allPRs   []github.InboxPR
	filtered []github.InboxPR
	snapshot inbox.Snapshot // previous state for change detection
	cursor   int
	filter   filter
	loading  bool
	spinner  spinner.Model
	width    int
	height   int
	nwo      string // optional repo filter
}

func New(ctx *uictx.Context, nwo string) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Magenta)
	m := Model{
		ctx:     ctx,
		filter:  filterAll,
		nwo:     nwo,
		spinner: s,
	}

	// Load persisted inbox data for instant display.
	var cached []github.InboxPR
	if found, err := persist.Load(persistFile, &cached); found && err == nil && len(cached) > 0 {
		m.allPRs = cached
		m.applyFilter()
	}

	return m
}

const (
	persistFile    = "inbox.json"
	refreshInterval = 1 * time.Minute
)

// inboxRefreshMsg triggers a background inbox refresh.
type inboxRefreshMsg struct{}

func (m Model) Init() tea.Cmd {
	if m.ctx.Username == "" {
		return nil
	}
	m.loading = true
	return tea.Batch(
		fetchInbox(m.ctx.Client, m.ctx.Username, ""),
		m.spinner.Tick,
		m.scheduleRefresh(),
	)
}

func (m Model) scheduleRefresh() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg {
		return inboxRefreshMsg{}
	})
}

func (m Model) Update(msg tea.Msg) (uictx.View, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case inboxRefreshMsg:
		// Background refresh — don't show loading indicator.
		return m, tea.Batch(
			fetchInbox(m.ctx.Client, m.ctx.Username, ""),
			m.scheduleRefresh(),
		)

	case InboxLoadedMsg:
		m.loading = false
		newPRs := inbox.ProcessInbox(msg.PRs, m.ctx.Username)

		// Detect changes and send notifications.
		changes := inbox.DetectChanges(m.snapshot, newPRs)
		for _, c := range changes {
			title, body := c.NotificationText()
			go notify.Send(title, body,
				notify.WithURL(c.PR.HTMLURL),
				notify.WithGroup("ghq-"+c.PR.Repo.FullName()+"#"+fmt.Sprintf("%d", c.PR.Number)),
			)
		}

		m.snapshot = inbox.TakeSnapshot(newPRs)
		m.allPRs = newPRs
		m.applyFilter()
		go persist.Save(persistFile, m.allPRs)
		return m, nil

	case spinner.TickMsg:
		if m.loading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case uictx.QueryErrMsg:
		m.loading = false
		return m, nil

	case tea.KeyPressMsg:
		view, cmd, handled := m.handleKey(msg)
		if handled {
			return view, cmd
		}
	}
	return m, nil
}

func (m Model) HandleKey(msg tea.KeyPressMsg) (uictx.View, tea.Cmd, bool) {
	return m.handleKey(msg)
}

func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
		return m, nil, true
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil, true
	case "ctrl+d":
		m.moveCursorBy(m.height / 2)
		return m, nil, true
	case "ctrl+u":
		m.moveCursorBy(-m.height / 2)
		return m, nil, true
	case "ctrl+f":
		m.moveCursorBy(m.height)
		return m, nil, true
	case "ctrl+b":
		m.moveCursorBy(-m.height)
		return m, nil, true
	case "enter":
		if m.cursor < len(m.filtered) {
			pr := m.filtered[m.cursor]
			return m, func() tea.Msg {
				return PRSelectedMsg{
					Owner:  pr.Repo.Owner,
					Repo:   pr.Repo.Name,
					Number: pr.Number,
				}
			}, true
		}
	case "r":
		m.loading = true
		return m, tea.Batch(fetchInbox(m.ctx.Client, m.ctx.Username, ""), m.spinner.Tick), true
	case "tab":
		// Toggle between all repos and current repo.
		if m.detectedRepo() == "" {
			return m, nil, true // no repo to filter to
		}
		if m.filter == filterAll {
			m.filter = filterRepo
		} else {
			m.filter = filterAll
		}
		m.applyFilter()
		return m, nil, true
	case "G":
		m.cursor = len(m.filtered) - 1
		return m, nil, true
	case "g":
		// Simple gg — just go to top on any g press for now.
		m.cursor = 0
		return m, nil, true
	}
	return m, nil, false
}

func (m *Model) moveCursorBy(delta int) {
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *Model) applyFilter() {
	m.cursor = 0
	if m.filter == filterAll {
		m.filtered = m.allPRs
		return
	}

	// Filter to current repo.
	repo := m.detectedRepo()
	m.filtered = nil
	for _, pr := range m.allPRs {
		if pr.Repo.FullName() == repo {
			m.filtered = append(m.filtered, pr)
		}
	}
}

// detectedRepo returns the repo nwo from --nwo flag or the client's detected repo.
func (m Model) detectedRepo() string {
	if m.nwo != "" {
		return m.nwo
	}
	r := m.ctx.Owner + "/" + m.ctx.Repo
	if r == "/" {
		return "" // no repo detected
	}
	return r
}

func (m Model) KeyBindings() []uictx.KeyBinding {
	return []uictx.KeyBinding{
		{Key: "j / k", Description: "Move cursor down / up", Keywords: []string{"navigate"}},
		{Key: "enter", Description: "Open selected PR"},
		{Key: "tab", Description: "Toggle repo filter"},
		{Key: "/", Description: "Filter PRs", Keywords: []string{"search"}},
		{Key: "r", Description: "Refresh inbox"},
	}
}

func (m Model) StatusHints() (left, right []string) {
	left = append(left, styles.StatusBarKey.Render("r")+" "+styles.StatusBarHint.Render("refresh"))
	if m.detectedRepo() != "" {
		left = append(left, styles.StatusBarKey.Render("tab")+" "+styles.StatusBarHint.Render("scope"))
	}
	// Show active filter.
	if m.filter == filterAll {
		right = append(right, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Magenta).Render("All repos"))
	} else {
		right = append(right, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Magenta).Render(m.detectedRepo()))
	}
	return
}

func (m Model) View() string {
	w := m.width
	if w < 20 {
		w = 80
	}
	innerW := w - 4 // border padding

	// Loading skeleton — only when loading with no data at all.
	if m.loading && len(m.allPRs) == 0 {
		return m.renderLoading(innerW)
	}

	if len(m.filtered) == 0 {
		content := dimStyle.Render("  No pull requests.")
		bordered := listBorder.Render(content)
		return dimStyle.Render(m.statusLine()) + "\n" + bordered
	}

	// Render rows.
	const linesPerItem = 3
	maxVisible := (m.height - 3) / linesPerItem
	if maxVisible < 1 {
		maxVisible = 1
	}

	start := 0
	if m.cursor >= maxVisible {
		start = m.cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(m.filtered) {
		end = len(m.filtered)
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		pr := m.filtered[i]
		isSelected := i == m.cursor

		// Line 1: ActionBadge #number title                    2d ago
		badge := m.actionBadge(pr.Action)
		num := dimStyle.Render(fmt.Sprintf("#%d", pr.Number))
		title := pr.Title
		if isSelected {
			title = lipgloss.NewStyle().Bold(true).Render(title)
		}
		age := dimStyle.Render(relativeTime(pr.UpdatedAt))

		prefix := " "
		if isSelected {
			prefix = lipgloss.NewStyle().Foreground(lipgloss.Magenta).Render("│")
		}

		line1 := prefix + " " + badge + " " + num + " " + title
		gap1 := innerW - lipgloss.Width(line1) - lipgloss.Width(age) - 1
		if gap1 < 1 {
			gap1 = 1
		}
		line1 += strings.Repeat(" ", gap1) + age

		// Line 2: @author  owner/repo  branch
		author := components.ColoredAuthor(pr.User.Login)
		repo := dimStyle.Render(pr.Repo.FullName())
		line2 := prefix + " " + "  " + author + " " + repo

		b.WriteString(padLine(line1, innerW) + "\n")
		b.WriteString(padLine(line2, innerW) + "\n")
		if i < end-1 {
			b.WriteString(sepStyle.Render(strings.Repeat("─", innerW)) + "\n")
		}
	}

	content := strings.TrimRight(b.String(), "\n")
	bordered := listBorder.Render(content)
	return dimStyle.Render(m.statusLine()) + "\n" + bordered
}

func (m Model) statusLine() string {
	total := len(m.filtered)
	var line string
	if m.filter == filterRepo {
		line = fmt.Sprintf(" %d pull requests in %s", total, m.detectedRepo())
	} else {
		line = fmt.Sprintf(" %d pull requests", total)
	}
	if m.loading {
		line += " " + m.spinner.View()
	}
	return line
}

func (m Model) renderLoading(innerW int) string {
	titleWidths := []int{25, 32, 18, 28, 22}
	var lines []string
	for i := 0; i < 5; i++ {
		line1 := dimStyle.Render("  " + strings.Repeat("─", 4) + " " + strings.Repeat("─", titleWidths[i]))
		line2 := dimStyle.Render("  " + strings.Repeat("─", 8) + " " + strings.Repeat("─", 12))
		lines = append(lines, padLine(line1, innerW))
		lines = append(lines, padLine(line2, innerW))
		if i < 4 {
			lines = append(lines, sepStyle.Render(strings.Repeat("─", innerW)))
		}
	}
	content := strings.Join(lines, "\n")
	bordered := listBorder.Render(content)
	return dimStyle.Render(" Loading...") + "\n" + bordered
}

func (m Model) actionBadge(action github.ActionReason) string {
	var label string
	var bg, fg color.Color

	c := m.ctx.DiffColors
	fg = c.PaletteBg // dark text on colored bg

	switch action {
	case github.ActionReadyToMerge:
		label = "Ready"
		bg = c.PaletteGreen
	case github.ActionApproved:
		label = "Approved"
		bg = c.PaletteGreen
	case github.ActionCIFailed:
		label = "CI Failed"
		bg = c.PaletteRed
		fg = c.PaletteFg
	case github.ActionChangesRequested:
		label = "Changes"
		bg = c.PaletteRed
		fg = c.PaletteFg
	case github.ActionMergeConflicts:
		label = "Conflicts"
		bg = c.PaletteRed
		fg = c.PaletteFg
	case github.ActionReviewRequested:
		label = "Review"
		bg = c.PaletteYellow
	case github.ActionReReviewRequested:
		label = "Re-review"
		bg = c.PaletteYellow
	case github.ActionCIPending:
		label = "CI"
		bg = c.PaletteCyan
	case github.ActionWaitingForReview:
		label = "Waiting"
		bg = c.PaletteDim
		fg = c.PaletteFg
	case github.ActionDraft:
		label = "Draft"
		bg = c.PaletteDim
		fg = c.PaletteFg
	case github.ActionMentioned:
		label = "Mention"
		bg = c.PaletteDim
		fg = c.PaletteFg
	case github.ActionMerged:
		label = "Merged"
		bg = c.PaletteMagenta
	case github.ActionClosed:
		label = "Closed"
		bg = c.PaletteDim
		fg = c.PaletteFg
	default:
		return ""
	}

	return lipgloss.NewStyle().
		Foreground(fg).
		Background(bg).
		Padding(0, 1).
		Render(label)
}

func padLine(s string, width int) string {
	pad := width - lipgloss.Width(s)
	if pad < 0 {
		pad = 0
	}
	return s + strings.Repeat(" ", pad)
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1m"
		}
		return fmt.Sprintf("%dm", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1h"
		}
		return fmt.Sprintf("%dh", h)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d"
		}
		return fmt.Sprintf("%dd", days)
	default:
		months := int(d.Hours() / 24 / 30)
		if months == 1 {
			return "1mo"
		}
		return fmt.Sprintf("%dmo", months)
	}
}
