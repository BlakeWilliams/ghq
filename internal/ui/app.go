package ui

import (
	"fmt"
	"strings"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/commandbar"
	"github.com/blakewilliams/ghq/internal/ui/prdetail"
	"github.com/blakewilliams/ghq/internal/ui/prlist"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type inputMode int

const (
	modeNormal inputMode = iota
	modeCommand
)

type view int

const (
	viewPRList view = iota
	viewPRDetail
)

const (
	headerHeight    = 2 // breadcrumb + blank line
	statusBarHeight = 1
	chromeHeight    = headerHeight + statusBarHeight
)

type Model struct {
	currentView view
	mode        inputMode
	prList      prlist.Model
	prDetail    prdetail.Model
	commandBar  commandbar.Model
	client      *github.CachedClient
	width       int
	height      int
}

func NewApp(client *github.CachedClient) Model {
	return Model{
		currentView: viewPRList,
		prList:      prlist.New(client),
		commandBar:  commandbar.New(),
		client:      client,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.prList.Init(), m.client.GCTickCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Y == 0 {
			return m.handleBreadcrumbClick(msg.X)
		}

	case github.GCTickMsg:
		m.client.GC()
		return m, m.client.GCTickCmd()

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		contentMsg := tea.WindowSizeMsg{Width: msg.Width, Height: msg.Height - chromeHeight}
		m.commandBar.SetWidth(msg.Width)

		var cmd tea.Cmd
		switch m.currentView {
		case viewPRList:
			m.prList, cmd = m.prList.Update(contentMsg)
		case viewPRDetail:
			m.prDetail, cmd = m.prDetail.Update(contentMsg)
		}
		return m, cmd

	case commandbar.CommandMsg:
		m.mode = modeNormal
		return m.handleCommand(msg)

	case commandbar.CancelledMsg:
		m.mode = modeNormal
		return m, nil

	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

		if m.mode == modeCommand {
			var cmd tea.Cmd
			m.commandBar, cmd = m.commandBar.Update(msg)
			return m, cmd
		}

		// Normal mode
		switch msg.String() {
		case ":":
			m.mode = modeCommand
			return m, m.commandBar.Focus()
		case "esc":
			if m.currentView == viewPRDetail {
				m.currentView = viewPRList
				return m, nil
			}
		}

	default:
		if m.mode == modeCommand {
			var cmd tea.Cmd
			m.commandBar, cmd = m.commandBar.Update(msg)
			return m, cmd
		}

	case prlist.PRSelectedMsg:
		m.currentView = viewPRDetail
		m.prDetail = prdetail.New(msg.PR, m.client, m.width, m.height-chromeHeight)
		return m, m.prDetail.Init()
	}

	var cmd tea.Cmd
	switch m.currentView {
	case viewPRList:
		m.prList, cmd = m.prList.Update(msg)
	case viewPRDetail:
		m.prDetail, cmd = m.prDetail.Update(msg)
	}
	return m, cmd
}

func (m Model) handleCommand(msg commandbar.CommandMsg) (tea.Model, tea.Cmd) {
	switch msg.Command {
	case "q", "quit":
		return m, tea.Quit
	case "refresh":
		switch m.currentView {
		case viewPRList:
			m.client.InvalidateAll()
			return m, m.client.ListPullRequests()
		}
	case "back":
		if m.currentView == viewPRDetail {
			m.currentView = viewPRList
			return m, nil
		}
	}
	return m, nil
}

func (m Model) View() tea.View {
	header := m.renderHeader()

	var content string
	switch m.currentView {
	case viewPRDetail:
		content = m.prDetail.View()
	default:
		content = m.prList.View()
	}

	var bar string
	if m.mode == modeCommand {
		bar = m.commandBar.View()
	} else {
		bar = m.renderStatusBar()
	}

	v := tea.NewView(header + "\n" + content + "\n" + bar)
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) renderHeader() string {
	repo := styles.HeaderRepo.Render(m.client.RepoFullName())
	sep := styles.HeaderSep.Render(" > ")

	pulls := styles.HeaderSection.Render("Pulls")
	crumb := repo + sep + pulls

	if m.currentView == viewPRDetail {
		crumb += sep + styles.HeaderSection.Render(fmt.Sprintf("#%d %s", m.prDetail.PRNumber(), m.prDetail.PRTitle()))
	}

	return crumb
}

func (m Model) handleBreadcrumbClick(x int) (tea.Model, tea.Cmd) {
	repoWidth := lipgloss.Width(styles.HeaderRepo.Render(m.client.RepoFullName()))
	sepWidth := lipgloss.Width(styles.HeaderSep.Render(" > "))
	pullsWidth := lipgloss.Width(styles.HeaderSection.Render("Pulls"))

	pullsStart := repoWidth + sepWidth
	pullsEnd := pullsStart + pullsWidth

	// Click on repo or "Pulls" navigates to the PR list
	if m.currentView == viewPRDetail && x < pullsEnd {
		m.currentView = viewPRList
		return m, nil
	}

	return m, nil
}

func (m Model) renderStatusBar() string {
	mode := styles.StatusBarMode.Render("NORMAL ")

	var hints []string
	switch m.currentView {
	case viewPRList:
		hints = []string{
			hint(":", "command"),
			hint("/", "filter"),
			hint("enter", "open"),
		}
	case viewPRDetail:
		hints = []string{
			hint(":", "command"),
			hint("tab", "switch tab"),
			hint("esc", "back"),
		}
	}

	right := strings.Join(hints, styles.StatusBarHint.Render("  "))
	gap := m.width - lipgloss.Width(mode) - lipgloss.Width(right) - 2
	if gap < 0 {
		gap = 0
	}

	return fmt.Sprintf("%s%s%s", mode, strings.Repeat(" ", gap), right)
}

func hint(key, desc string) string {
	return styles.StatusBarKey.Render(key) + " " + styles.StatusBarHint.Render(desc)
}
