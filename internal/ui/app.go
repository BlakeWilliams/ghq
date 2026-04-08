package ui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/blakewilliams/ghq/internal/git"
	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/terminal"
	"github.com/blakewilliams/ghq/internal/ui/commandbar"
	"github.com/blakewilliams/ghq/internal/ui/home"
	"github.com/blakewilliams/ghq/internal/ui/localdiff"
	"github.com/blakewilliams/ghq/internal/ui/picker"
	"github.com/blakewilliams/ghq/internal/ui/prdetail"
	"github.com/blakewilliams/ghq/internal/ui/prlist"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	"github.com/blakewilliams/ghq/internal/ui/uictx"
	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"charm.land/lipgloss/v2"
)

type inputMode int

const (
	modeNormal inputMode = iota
	modeCommand
	modePicker
)

const chromeHeight = 2


type Model struct {
	activeView uictx.View
	prList     prlist.Model // retained so we can restore it on back-navigation
	history    []uictx.View // back stack (views we navigated away from)
	forward    []uictx.View // forward stack (views we went back from)
	mode       inputMode
	commandBar commandbar.Model
	picker     picker.Model
	ctx        *uictx.Context
	palette    terminal.Palette
	repoRoot   string // local git repo root, if any
	width      int
	height     int
}

// NewApp creates and returns a new top-level UI model. If repoRoot is non-empty
// and no GitHub repo (nwo) is specified, it opens directly to the local diff view.
func NewApp(client *github.CachedClient, nwo string, repoRoot string) Model {
	ctx := &uictx.Context{Client: client, NWO: nwo}
	var initialView uictx.View
	if repoRoot != "" && nwo == "" {
		initialView = localdiff.New(ctx, repoRoot, 0, 0)
	} else {
		initialView = home.New(ctx, nwo)
	}
	return Model{
		activeView: initialView,
		commandBar: commandbar.New(),
		ctx:        ctx,
		repoRoot:   repoRoot,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.ctx.Client.FetchAuthenticatedUser(),
		m.ctx.Client.GCTickCmd(),
		queryPaletteCmd(),
		tea.RequestBackgroundColor,
	)
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

		// Picker mode owns all input.
		if m.mode == modePicker {
			var cmd tea.Cmd
			m.picker, cmd = m.picker.Update(msg)
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
		case ":", "ctrl+t":
			m.mode = modePicker
			m.picker = picker.New("Commands", m.commandPickerItems(), m.pickerInnerWidth(), m.height-chromeHeight)
			return m, nil
		case "?":
			m.mode = modePicker
			m.picker = picker.New("Help", m.helpPickerItems(), m.pickerInnerWidth(), m.height-chromeHeight)
			return m, nil
		case "<":
			if len(m.history) > 0 {
				return m.navigateBack()
			}
		case ">":
			if len(m.forward) > 0 {
				return m.navigateForward()
			}
		}

	case picker.ResultMsg:
		m.mode = modeNormal
		if msg.Selected && msg.Value != "" {
			return m.handleCommand(commandbar.CommandMsg{Command: msg.Value})
		}
		// If no item was selected but query is a number, jump to that line
		if !msg.Selected && msg.Value != "" {
			if lineNo, err := strconv.Atoi(msg.Value); err == nil && lineNo > 0 {
				var cmd tea.Cmd
				m.activeView, cmd = m.activeView.Update(localdiff.GoToLineMsg{Line: lineNo})
				return m, cmd
			}
		}
		return m, nil

	default:
		if m.mode == modeCommand {
			var cmd tea.Cmd
			m.commandBar, cmd = m.commandBar.Update(msg)
			return m, cmd
		}
		if m.mode == modePicker {
			var cmd tea.Cmd
			m.picker, cmd = m.picker.Update(msg)
			return m, cmd
		}

	case github.UserLoadedMsg:
		m.ctx.Username = msg.User.Login
		// Now that we have the username, init the home view.
		return m, m.activeView.Init()

	case home.PRSelectedMsg:
		m.history = append(m.history, m.activeView)
		m.forward = nil
		return m, m.ctx.Client.FetchPR(msg.Owner, msg.Repo, msg.Number)

	case github.PRLoadedMsg:
		// Scope client to this PR's repo for all subsequent API calls.
		if owner := msg.PR.RepoOwner(); owner != "" {
			m.ctx.Client.SetRepo(owner, msg.PR.RepoName())
		}
		m.activeView = prdetail.New(msg.PR, m.ctx, m.width, m.height-chromeHeight)
		return m, m.activeView.Init()

	case prlist.PRSelectedMsg:
		m.history = append(m.history, m.activeView)
		m.forward = nil
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
	// Restore repo scope from the NWO flag (or clear it) when leaving PR detail.
	if m.ctx.NWO != "" {
		parts := strings.SplitN(m.ctx.NWO, "/", 2)
		if len(parts) == 2 {
			m.ctx.Client.SetRepo(parts[0], parts[1])
		}
	} else {
		m.ctx.Client.SetRepo("", "")
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
	// :N — jump to line number.
	if lineNo, err := strconv.Atoi(msg.Command); err == nil && lineNo > 0 {
		var cmd tea.Cmd
		m.activeView, cmd = m.activeView.Update(localdiff.GoToLineMsg{Line: lineNo})
		return m, cmd
	}

	switch msg.Command {
	case "q", "quit":
		return m, tea.Quit
	case "refresh":
		if _, ok := m.activeView.(prlist.Model); ok {
			m.ctx.Client.InvalidateAll()
			return m, m.ctx.Client.ListPullRequests()
		}
		if _, ok := m.activeView.(localdiff.Model); ok {
			m.activeView = localdiff.New(m.ctx, m.repoRoot, m.width, m.height-chromeHeight)
			return m, m.activeView.Init()
		}
	case "back":
		if len(m.history) > 0 {
			return m.navigateBack()
		}
	case "local":
		if m.repoRoot != "" {
			if _, ok := m.activeView.(localdiff.Model); !ok {
				m.history = append(m.history, m.activeView)
				m.forward = nil
				m.activeView = localdiff.New(m.ctx, m.repoRoot, m.width, m.height-chromeHeight)
				return m, m.activeView.Init()
			}
		}
	case "inbox":
		if _, ok := m.activeView.(home.Model); !ok {
			m.history = append(m.history, m.activeView)
			m.forward = nil
			h := home.New(m.ctx, m.ctx.NWO)
			m.activeView = h
			resize := tea.WindowSizeMsg{Width: m.width, Height: m.height - chromeHeight}
			m.activeView, _ = m.activeView.Update(resize)
			return m, m.activeView.Init()
		}
	case "pr":
		// Open the PR for the current branch.
		if m.repoRoot != "" {
			branch := ""
			if ld, ok := m.activeView.(localdiff.Model); ok {
				branch = ld.BranchName()
			} else {
				branch, _ = git.CurrentBranch(m.repoRoot)
			}
			if branch != "" {
				m.history = append(m.history, m.activeView)
				m.forward = nil
				return m, m.ctx.Client.FetchPRByBranch(branch)
			}
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

	// Overlay picker modal if open.
	if m.mode == modePicker {
		content = m.renderPickerOverlay(content, contentHeight)
	}

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
	sep := styles.HeaderSep.Render(" / ")

	var crumb string
	switch m.activeView.(type) {
	case home.Model:
		crumb = " " + styles.HeaderActive.Render("Inbox")
	case localdiff.Model:
		detail := m.activeView.(localdiff.Model)
		crumb = " " + styles.HeaderActive.Render("Local") + sep + styles.HeaderSection.Render(detail.BranchName()) +
			sep + styles.HeaderActive.Render(detail.DiffMode().String())
	case prlist.Model:
		repo := styles.HeaderRepo.Render(m.ctx.Client.RepoFullName())
		crumb = " " + repo + sep + styles.HeaderActive.Render("Pulls")
	case prdetail.Model:
		detail := m.activeView.(prdetail.Model)
		repo := styles.HeaderRepo.Render(detail.RepoFullName())
		crumb = " " + repo + sep + styles.HeaderSection.Render("Pulls") +
			sep + styles.HeaderActive.Render(fmt.Sprintf("#%d %s", detail.PRNumber(), detail.PRTitle()))
	default:
		crumb = " " + styles.HeaderActive.Render("ghq")
	}

	return styles.HeaderBar.Width(m.width).Render(crumb)
}

func (m Model) renderStatusBar() string {
	var left, right string
	sep := styles.StatusBarHint.Render("  ")

	switch v := m.activeView.(type) {
	case home.Model:
		leftHints, rightHints := v.StatusHints()
		leftHints = append([]string{formatHints([]string{":  cmd"})}, leftHints...)
		left = strings.Join(leftHints, sep)
		right = strings.Join(rightHints, sep)
	case localdiff.Model:
		leftHints, rightHints := v.StatusHints()
		leftHints = append([]string{formatHints([]string{":  cmd"})}, leftHints...)
		left = strings.Join(leftHints, sep)
		right = strings.Join(rightHints, sep)
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

func (m Model) commandPickerItems() []picker.Item {
	items := []picker.Item{
		{Label: "Inbox", Description: "Go to PR inbox", Value: "inbox", Keywords: []string{"home", "notifications"}},
		{Label: "Refresh", Description: "Reload current view", Value: "refresh"},
		{Label: "Quit", Description: "Exit ghq", Value: "quit", Keywords: []string{"exit", "close"}},
	}
	if m.repoRoot != "" {
		items = append([]picker.Item{
			{Label: "Local Diff", Description: "Local changes view", Value: "local", Keywords: []string{"diff", "changes", "working"}},
			{Label: "Open PR", Description: "PR for current branch", Value: "pr", Keywords: []string{"pull request", "branch"}},
		}, items...)
	}
	return items
}

func (m Model) helpPickerItems() []picker.Item {
	// Global keys.
	items := []picker.Item{
		{Label: ":", Description: "Open command picker", Keywords: []string{"command", "menu"}},
		{Label: "?", Description: "Open help", Keywords: []string{"keybindings", "shortcuts"}},
		{Label: "<", Description: "Navigate back"},
		{Label: ">", Description: "Navigate forward"},
		{Label: "ctrl+c", Description: "Quit"},
	}

	// View-specific keys from the view itself.
	for _, kb := range m.activeView.KeyBindings() {
		items = append(items, picker.Item{
			Label:       kb.Key,
			Description: kb.Description,
			Keywords:    kb.Keywords,
		})
	}

	return items
}

func (m Model) pickerModalWidth() int {
	w := m.width / 2
	if w < 40 {
		w = 40
	}
	if w > m.width-4 {
		w = m.width - 4
	}
	return w
}

func (m Model) pickerInnerWidth() int {
	return m.pickerModalWidth() - 4 // "│ " + " │"
}

// renderPickerOverlay renders the picker as a centered modal over the content.
func (m Model) renderPickerOverlay(bg string, bgHeight int) string {
	pickerView := m.picker.View()
	pickerLines := strings.Split(pickerView, "\n")

	modalW := m.pickerModalWidth()
	innerW := m.pickerInnerWidth()
	modalH := len(pickerLines) + 2

	// Center vertically — bias toward top third.
	padY := (bgHeight - modalH) / 3
	if padY < 1 {
		padY = 1
	}

	bc := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)

	// Build borders.
	titleStr := " " + lipgloss.NewStyle().Bold(true).Render(m.picker.Title()) + " "
	titleW := lipgloss.Width(titleStr)
	fillW := modalW - 3 - titleW
	if fillW < 0 {
		fillW = 0
	}
	topBorder := bc.Render("╭─") + titleStr + bc.Render(strings.Repeat("─", fillW)+"╮")
	bw := modalW - 2
	if bw < 0 {
		bw = 0
	}
	bottomBorder := bc.Render("╰" + strings.Repeat("─", bw) + "╯")
	side := bc.Render("│")

	// Build modal lines.
	var modalLines []string
	modalLines = append(modalLines, topBorder)
	for _, pl := range pickerLines {
		plW := lipgloss.Width(pl)
		pad := innerW - plW
		if pad < 0 {
			pad = 0
		}
		modalLines = append(modalLines, side+" "+pl+strings.Repeat(" ", pad)+" "+side)
	}
	modalLines = append(modalLines, bottomBorder)

	// Splice modal onto background.
	bgLines := strings.Split(bg, "\n")
	padX := (m.width - modalW) / 2
	rightStart := padX + modalW

	for i, ml := range modalLines {
		row := padY + i
		if row >= 0 && row < len(bgLines) {
			bgLine := bgLines[row]
			bgW := lipgloss.Width(bgLine)

			// Left side of background.
			left := ""
			if padX > 0 {
				left = truncateVisible(bgLine, padX)
				leftW := lipgloss.Width(left)
				if leftW < padX {
					left += strings.Repeat(" ", padX-leftW)
				}
			}

			// Right side of background after the modal.
			right := ""
			if bgW > rightStart {
				// Cut the background from rightStart onward.
				right = xansi.Cut(bgLine, rightStart, bgW)
			}

			bgLines[row] = left + "\033[0m" + ml + "\033[0m" + right
		}
	}

	return strings.Join(bgLines, "\n")
}

// truncateVisible truncates a string to n visible characters, preserving ANSI.
func truncateVisible(s string, n int) string {
	return xansi.Truncate(s, n, "")
}
