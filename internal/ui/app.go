package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/terminal"
	"github.com/blakewilliams/ghq/internal/ui/commandbar"
	"github.com/blakewilliams/ghq/internal/ui/prdetail"
	"github.com/blakewilliams/ghq/internal/ui/prlist"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	"github.com/blakewilliams/ghq/internal/ui/uictx"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type inputMode int

const (
	modeNormal inputMode = iota
	modeCommand
)

const chromeHeight = 2


type Model struct {
	activeView uictx.View
	prList     prlist.Model // retained so we can restore it on back-navigation
	history    []uictx.View // back stack (views we navigated away from)
	forward    []uictx.View // forward stack (views we went back from)
	mode       inputMode
	commandBar commandbar.Model
	ctx        *uictx.Context
	palette    terminal.Palette
	width      int
	height     int
}

func NewApp(client *github.CachedClient) Model {
	ctx := &uictx.Context{Client: client}
	pl := prlist.New(ctx)
	return Model{
		activeView: pl,
		prList:     pl,
		commandBar: commandbar.New(),
		ctx:        ctx,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.activeView.Init(), m.ctx.Client.GCTickCmd(), queryPaletteCmd(), tea.RequestBackgroundColor)
}

// queryPaletteCmd sends OSC 4 queries through Bubble Tea's output buffer.
// Uses DCS passthrough when in tmux.
func queryPaletteCmd() tea.Cmd {
	inTmux := os.Getenv("TMUX") != ""
	var cmds []tea.Cmd
	for i := 0; i < 16; i++ {
		var seq string
		if inTmux {
			seq = fmt.Sprintf("\x1bPtmux;\x1b\x1b]4;%d;?\x07\x1b\\", i)
		} else {
			seq = fmt.Sprintf("\x1b]4;%d;?\x07", i)
		}
		cmds = append(cmds, tea.Raw(seq))
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle palette responses.
	if cmd, handled := terminal.HandleMessage(msg, &m.palette); handled {
		if m.palette.Complete() {
			m.ctx.DiffColors = styles.ComputeDiffColors(m.palette)
		}
		return m, cmd
	}

	switch msg := msg.(type) {
	case github.GCTickMsg:
		m.ctx.Client.GC()
		return m, m.ctx.Client.GCTickCmd()

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.commandBar.SetWidth(msg.Width)
		contentMsg := tea.WindowSizeMsg{Width: msg.Width, Height: msg.Height - chromeHeight}
		var cmd tea.Cmd
		m.activeView, cmd = m.activeView.Update(contentMsg)
		return m, cmd

	case commandbar.CommandMsg:
		m.mode = modeNormal
		return m.handleCommand(msg)

	case commandbar.CancelledMsg:
		m.mode = modeNormal
		return m, nil

	case tea.KeyPressMsg:
		// Hard globals — always handled regardless of view/mode.
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

		// Command mode owns all input.
		if m.mode == modeCommand {
			var cmd tea.Cmd
			m.commandBar, cmd = m.commandBar.Update(msg)
			return m, cmd
		}

		// Delegate to active view first.
		view, cmd, handled := m.activeView.HandleKey(msg)
		if handled {
			m.activeView = view
			return m, cmd
		}

		// Global shortcuts (view didn't claim the key).
		switch msg.String() {
		case ":":
			m.mode = modeCommand
			return m, m.commandBar.Focus()
		case "<":
			if len(m.history) > 0 {
				return m.navigateBack()
			}
		case ">":
			if len(m.forward) > 0 {
				return m.navigateForward()
			}
		}

	default:
		if m.mode == modeCommand {
			var cmd tea.Cmd
			m.commandBar, cmd = m.commandBar.Update(msg)
			return m, cmd
		}

	case prlist.PRSelectedMsg:
		// Save prList state before switching away.
		if pl, ok := m.activeView.(prlist.Model); ok {
			m.prList = pl
		}
		m.history = append(m.history, m.activeView)
		m.forward = nil // clear forward stack on new navigation
		m.activeView = prdetail.New(msg.PR, m.ctx, m.width, m.height-chromeHeight)
		return m, m.activeView.Init()
	}

	// Forward non-key messages to active view.
	var cmd tea.Cmd
	m.activeView, cmd = m.activeView.Update(msg)
	return m, cmd
}

func (m Model) navigateBack() (tea.Model, tea.Cmd) {
	prev := m.history[len(m.history)-1]
	m.history = m.history[:len(m.history)-1]
	m.forward = append(m.forward, m.activeView)
	m.activeView = prev
	// Restore prList reference if we're going back to the list.
	if pl, ok := prev.(prlist.Model); ok {
		m.prList = pl
	}
	resize := tea.WindowSizeMsg{Width: m.width, Height: m.height - chromeHeight}
	m.activeView, _ = m.activeView.Update(resize)
	return m, nil
}

func (m Model) navigateForward() (tea.Model, tea.Cmd) {
	next := m.forward[len(m.forward)-1]
	m.forward = m.forward[:len(m.forward)-1]
	m.history = append(m.history, m.activeView)
	m.activeView = next
	if pl, ok := m.activeView.(prlist.Model); ok {
		m.prList = pl
	}
	resize := tea.WindowSizeMsg{Width: m.width, Height: m.height - chromeHeight}
	m.activeView, _ = m.activeView.Update(resize)
	return m, nil
}

func (m Model) handleCommand(msg commandbar.CommandMsg) (tea.Model, tea.Cmd) {
	switch msg.Command {
	case "q", "quit":
		return m, tea.Quit
	case "refresh":
		if _, ok := m.activeView.(prlist.Model); ok {
			m.ctx.Client.InvalidateAll()
			return m, m.ctx.Client.ListPullRequests()
		}
	case "back":
		if len(m.history) > 0 {
			return m.navigateBack()
		}
	}
	return m, nil
}

func (m Model) View() tea.View {
	header := m.renderHeader()

	contentHeight := m.height - chromeHeight
	if contentHeight < 0 {
		contentHeight = 0
	}

	content := lipgloss.NewStyle().Height(contentHeight).Render(m.activeView.View())

	var bar string
	if m.mode == modeCommand {
		bar = m.commandBar.View()
	} else {
		bar = m.renderStatusBar()
	}

	v := tea.NewView(header + "\n" + content + "\n" + bar)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) renderHeader() string {
	repo := styles.HeaderRepo.Render(m.ctx.Client.RepoFullName())
	sep := styles.HeaderSep.Render(" / ")

	crumb := " " + repo + sep + styles.HeaderSection.Render("Pulls")

	if detail, ok := m.activeView.(prdetail.Model); ok {
		crumb += sep + styles.HeaderActive.Render(fmt.Sprintf("#%d %s", detail.PRNumber(), detail.PRTitle()))
	}

	return styles.HeaderBar.Width(m.width).Render(crumb)
}

func (m Model) renderStatusBar() string {
	var left, right string
	sep := styles.StatusBarHint.Render("  ")

	switch v := m.activeView.(type) {
	case prlist.Model:
		left = formatHints([]string{":  cmd", "/  filter", "enter  open"})
	case prdetail.Model:
		leftHints, rightHints := v.StatusHints()
		leftHints = append([]string{styles.StatusBarKey.Render("<") + " " + styles.StatusBarHint.Render("back")}, leftHints...)
		left = strings.Join(leftHints, sep)
		right = strings.Join(rightHints, sep)
	}

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}

	return left + strings.Repeat(" ", gap) + right
}

func formatHints(hints []string) string {
	var parts []string
	for _, h := range hints {
		// Split on first space: "key desc"
		idx := strings.IndexByte(h, ' ')
		if idx > 0 {
			key := h[:idx]
			desc := h[idx+1:]
			parts = append(parts, styles.StatusBarKey.Render(key)+" "+styles.StatusBarHint.Render(desc))
		} else {
			parts = append(parts, styles.StatusBarHint.Render(h))
		}
	}
	return strings.Join(parts, styles.StatusBarHint.Render("  "))
}

func hint(key, desc string) string {
	return styles.StatusBarKey.Render(key) + " " + styles.StatusBarHint.Render(desc)
}
