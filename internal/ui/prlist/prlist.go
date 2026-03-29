package prlist

import (
	"fmt"

	"github.com/blakewilliams/ghq/internal/github"
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
	prefix := ""
	if i.pr.Draft {
		prefix = "[draft] "
	}
	return fmt.Sprintf("#%d %s%s", i.pr.Number, prefix, i.pr.Title)
}

func (i prItem) Description() string {
	user := lipgloss.NewStyle().
		UnderlineStyle(lipgloss.UnderlineDotted).
		Hyperlink(fmt.Sprintf("https://github.com/%s", i.pr.User.Login)).
		Render(i.pr.User.Login)
	return fmt.Sprintf("%s • %s → %s", user, i.pr.Head.Ref, i.pr.Base.Ref)
}

func (i prItem) FilterValue() string {
	return i.pr.Title
}

type Model struct {
	list   list.Model
	client *github.CachedClient
	err    error
}

func New(client *github.CachedClient) Model {
	delegate := list.NewDefaultDelegate()
	delegate.Styles.NormalTitle = delegate.Styles.NormalTitle.Bold(true)
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Bold(true).
		Foreground(lipgloss.Magenta).
		BorderLeftForeground(lipgloss.Magenta)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Foreground(lipgloss.BrightBlack).
		BorderLeftForeground(lipgloss.Magenta)
	delegate.SetSpacing(1)

	l := list.New(nil, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.SetSpinner(spinner.Dot)
	l.Styles.Spinner = lipgloss.NewStyle().Foreground(lipgloss.Magenta)

	return Model{
		list:   l,
		client: client,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.list.StartSpinner(), m.client.ListPullRequests())
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height)

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
		if msg.String() == "enter" && !m.list.SettingFilter() {
			if item := m.list.SelectedItem(); item != nil {
				if pi, ok := item.(prItem); ok {
					return m, func() tea.Msg {
						return PRSelectedMsg{PR: pi.pr}
					}
				}
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) View() string {
	if m.err != nil {
		return fmt.Sprintf("Error: %v\n\nPress q to quit.", m.err)
	}

	items := m.list.VisibleItems()
	total := len(m.list.Items())
	status := fmt.Sprintf(" %d pull requests", total)
	if len(items) != total {
		status = fmt.Sprintf(" %d of %d pull requests", len(items), total)
	}
	statusBar := lipgloss.NewStyle().
		Foreground(lipgloss.BrightBlack).
		Render(status)

	return m.list.View() + "\n" + statusBar
}

