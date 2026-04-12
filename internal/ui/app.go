package ui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/blakewilliams/ghq/internal/git"
	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/terminal"
	"github.com/blakewilliams/ghq/internal/copilot"
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

// gcTickMsg triggers garbage collection of stale cache entries.
type gcTickMsg struct{}

// userLoadedMsg is sent when the authenticated user is loaded.
type userLoadedMsg struct {
	User github.User
}

func fetchAuthenticatedUser(c *github.CachedClient) tea.Cmd {
	return func() tea.Msg {
		user, err := c.FetchAuthenticatedUser()
		if err != nil {
			return uictx.QueryErrMsg{Err: err}
		}
		return userLoadedMsg{User: user}
	}
}

func gcTickCmd(c *github.CachedClient) tea.Cmd {
	return tea.Tick(c.GCInterval(), func(t time.Time) tea.Msg {
		return gcTickMsg{}
	})
}

type inputMode int
type quitTimeoutMsg struct{}

const (
	modeNormal inputMode = iota
	modeCommand
	modePicker
	modeCopilotChat
)

const chromeHeight = 2


type Model struct {
	activeView uictx.View
	prList     prlist.Model // retained so we can restore it on back-navigation
	history    []uictx.View // back stack (views we navigated away from)
	forward    []uictx.View // forward stack (views we went back from)
	mode       inputMode
	commandBar commandbar.Model
	picker      picker.Model
	pickerKind  string // "command", "help", "view" — routes ResultMsg
	copilotChat    copilot.ChatModel
	chatClient     *copilot.Client // shared copilot client for chat
	chatInitialized bool
	ctx        *uictx.Context
	quitPending bool // true after first ctrl+c
	palette    terminal.Palette
	repoRoot   string // local git repo root, if any
	width      int
	height     int
}

// AppConfig holds the arguments for creating a new App.
type AppConfig struct {
	Client   *github.CachedClient
	Owner    string
	Repo     string
	RepoRoot string // local git repo root, if any
}

// NewApp creates and returns a new top-level UI model.
func NewApp(cfg AppConfig) Model {
	ctx := &uictx.Context{Client: cfg.Client, Owner: cfg.Owner, Repo: cfg.Repo}
	var initialView uictx.View
	if cfg.RepoRoot != "" && cfg.Owner == "" {
		initialView = localdiff.New(ctx, cfg.RepoRoot, 0, 0)
	} else {
		nwo := ""
		if cfg.Owner != "" {
			nwo = cfg.Owner + "/" + cfg.Repo
		}
		initialView = home.New(ctx, nwo)
	}
	return Model{
		activeView: initialView,
		commandBar: commandbar.New(),
		ctx:        ctx,
		repoRoot:   cfg.RepoRoot,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchAuthenticatedUser(m.ctx.Client),
		gcTickCmd(m.ctx.Client),
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
	case gcTickMsg:
		m.ctx.Client.GC()
		return m, gcTickCmd(m.ctx.Client)

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

	case quitTimeoutMsg:
		m.quitPending = false
		return m, nil

	case tea.KeyPressMsg:
		// Hard globals — always handled regardless of view/mode.
		if msg.String() == "ctrl+c" {
			if m.quitPending {
				return m, tea.Quit
			}
			m.quitPending = true
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
				return quitTimeoutMsg{}
			})
		}
		// Any other key resets quit pending.
		m.quitPending = false

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

		// Copilot chat mode owns all input.
		if m.mode == modeCopilotChat {
			var cmd tea.Cmd
			m.copilotChat, cmd = m.copilotChat.Update(msg)
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
			m.pickerKind = "command"
			m.picker = picker.New("Commands", m.commandPickerItems(), m.pickerInnerWidth(), m.height-chromeHeight)
			return m, nil
		case "?":
			m.mode = modePicker
			m.pickerKind = "help"
			m.picker = picker.New("Help", m.helpPickerItems(), m.pickerInnerWidth(), m.height-chromeHeight)
			return m, nil
		case "C":
			return m.openCopilotChat()
		case "m":
			// In prdetail, m goes back to localdiff.
			if _, ok := m.activeView.(prdetail.Model); ok && m.repoRoot != "" {
				if len(m.history) > 0 {
					return m.navigateBack()
				}
			}
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
		kind := m.pickerKind
		m.pickerKind = ""

		if !msg.Selected && msg.Value != "" {
			// Raw query — try line number.
			if lineNo, err := strconv.Atoi(msg.Value); err == nil && lineNo > 0 {
				var cmd tea.Cmd
				m.activeView, cmd = m.activeView.Update(localdiff.GoToLineMsg{Line: lineNo})
				return m, cmd
			}
		}
		if !msg.Selected || msg.Value == "" {
			return m, nil
		}

		switch kind {
		case "command":
			return m.handleCommand(commandbar.CommandMsg{Command: msg.Value})
		case "view":
			return m.handleViewPickerResult(msg.Value)
		}
		return m, nil

	case copilot.CloseMsg:
		m.mode = modeNormal
		return m, nil

	case localdiff.OpenViewPickerMsg:
		m.mode = modePicker
		m.pickerKind = "view"
		m.picker = picker.New("View", msg.Items, m.pickerInnerWidth(), m.height-chromeHeight)
		return m, nil

	case localdiff.SwitchToPRMsg:
		m.history = append(m.history, m.activeView)
		m.forward = nil
		m.activeView = m.newPRDetail(msg.PR)
		return m, m.activeView.Init()

	// Route copilot messages by comment ID — "chat" goes to chat, others to active view.
	case copilot.ReplyMsg:
		if msg.CommentID == "chat" {
			var cmd tea.Cmd
			m.copilotChat, cmd = m.copilotChat.Update(msg)
			return m, cmd
		}
		// Inline comment reply — forward to active view.
		var cmd tea.Cmd
		m.activeView, cmd = m.activeView.Update(msg)
		return m, cmd

	case copilot.ErrorMsg:
		if msg.CommentID == "chat" {
			var cmd tea.Cmd
			m.copilotChat, cmd = m.copilotChat.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.activeView, cmd = m.activeView.Update(msg)
		return m, cmd

	case copilot.ToolMsg:
		if msg.CommentID == "chat" {
			var cmd tea.Cmd
			m.copilotChat, cmd = m.copilotChat.Update(msg)
			return m, cmd
		}
		return m, nil // ignore tool events for inline comments

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
		if m.mode == modeCopilotChat {
			var cmd tea.Cmd
			m.copilotChat, cmd = m.copilotChat.Update(msg)
			return m, cmd
		}

	case userLoadedMsg:
		m.ctx.Username = msg.User.Login
		// Now that we have the username, init the home view.
		return m, m.activeView.Init()

	case home.PRSelectedMsg:
		m.history = append(m.history, m.activeView)
		m.forward = nil
		return m, uictx.FetchPR(m.ctx.Client, msg.Owner, msg.Repo, msg.Number)

	case uictx.PRLoadedMsg:
		m.activeView = m.newPRDetail(msg.PR)
		return m, m.activeView.Init()

	case prlist.PRSelectedMsg:
		m.history = append(m.history, m.activeView)
		m.forward = nil
		m.activeView = m.newPRDetail(msg.PR)
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
			return m, prlist.ListPullRequests(m.ctx.Client)
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
			nwo := ""
			if m.ctx.Owner != "" {
				nwo = m.ctx.Owner + "/" + m.ctx.Repo
			}
			h := home.New(m.ctx, nwo)
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
				return m, uictx.FetchPRByBranch(m.ctx.Client, branch)
			}
		}
	case "working":
		var cmd tea.Cmd
		m.activeView, cmd = m.activeView.Update(localdiff.SwitchModeMsg{Mode: git.DiffWorking})
		return m, cmd
	case "staged":
		var cmd tea.Cmd
		m.activeView, cmd = m.activeView.Update(localdiff.SwitchModeMsg{Mode: git.DiffStaged})
		return m, cmd
	case "branch":
		var cmd tea.Cmd
		m.activeView, cmd = m.activeView.Update(localdiff.SwitchModeMsg{Mode: git.DiffBranch})
		return m, cmd
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

	// Overlay modals if open.
	if m.mode == modePicker {
		content = m.renderPickerOverlay(content, contentHeight)
	} else if m.mode == modeCopilotChat {
		content = m.renderChatOverlay(content, contentHeight)
	}

	var bar string
	if m.quitPending {
		bar = styles.StatusBarKey.Render("Press ctrl+c again to quit")
	} else if m.mode == modeCommand {
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

	// Always show repo name if available.
	repo := m.ctx.Client.RepoFullName()
	var crumb string

	switch v := m.activeView.(type) {
	case home.Model:
		if repo != "" {
			crumb = " " + styles.HeaderRepo.Render(repo) + sep + styles.HeaderActive.Render("Inbox")
		} else {
			crumb = " " + styles.HeaderActive.Render("Inbox")
		}
	case localdiff.Model:
		dot := styles.HeaderSep.Render(" · ")
		crumb = " "
		if repo != "" {
			crumb += styles.HeaderRepo.Render(repo) + sep
		}
		crumb += styles.HeaderActive.Render(v.BranchName()) + dot + styles.HeaderSection.Render(v.DiffMode().String())
		if pr := v.PR(); pr != nil {
			prBadge := styles.HeaderSep.Render(fmt.Sprintf(" PR #%d", pr.Number))
			gap := m.width - lipgloss.Width(crumb) - lipgloss.Width(prBadge) - 1
			if gap > 0 {
				crumb += strings.Repeat(" ", gap) + prBadge
			}
		}
	case prlist.Model:
		crumb = " " + styles.HeaderRepo.Render(repo) + sep + styles.HeaderActive.Render("Pulls")
	case prdetail.Model:
		crumb = " " + styles.HeaderRepo.Render(v.RepoFullName()) + sep +
			styles.HeaderSection.Render("Pulls") + sep +
			styles.HeaderActive.Render(fmt.Sprintf("#%d %s", v.PRNumber(), v.PRTitle()))
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

// newPRDetail creates a prdetail.Model and sets local context if applicable.
func (m Model) newPRDetail(pr github.PullRequest) prdetail.Model {
	pd := prdetail.New(pr, m.ctx, m.width, m.height-chromeHeight)
	if m.repoRoot != "" {
		branch, _ := git.CurrentBranch(m.repoRoot)
		if branch == pr.Head.Ref {
			pd.SetLocalContext(m.repoRoot)
		}
	}
	return pd
}

func (m Model) handleViewPickerResult(value string) (tea.Model, tea.Cmd) {
	switch value {
	case "working":
		var cmd tea.Cmd
		m.activeView, cmd = m.activeView.Update(localdiff.SwitchModeMsg{Mode: git.DiffWorking})
		return m, cmd
	case "staged":
		var cmd tea.Cmd
		m.activeView, cmd = m.activeView.Update(localdiff.SwitchModeMsg{Mode: git.DiffStaged})
		return m, cmd
	case "branch":
		var cmd tea.Cmd
		m.activeView, cmd = m.activeView.Update(localdiff.SwitchModeMsg{Mode: git.DiffBranch})
		return m, cmd
	case "pr":
		if ld, ok := m.activeView.(localdiff.Model); ok && ld.PR() != nil {
			m.history = append(m.history, m.activeView)
			m.forward = nil
			pr := *ld.PR()
			m.activeView = m.newPRDetail(pr)
			return m, m.activeView.Init()
		}
	}
	return m, nil
}

func (m Model) openCopilotChat() (tea.Model, tea.Cmd) {
	// Create or reuse copilot client.
	if m.chatClient == nil {
		repoRoot := m.repoRoot
		if repoRoot == "" {
			repoRoot = "."
		}
		cp, err := copilot.New(repoRoot)
		if err != nil {
			return m, nil
		}
		m.chatClient = cp
	}

	// Build diff context from the active view.
	ctx := copilot.DiffContext{
		RepoRoot: m.repoRoot,
	}
	switch v := m.activeView.(type) {
	case localdiff.Model:
		ctx.Files = v.Files()
		ctx.Branch = v.BranchName()
		if v.PR() != nil {
			ctx.PRNumber = v.PR().Number
		}
	case prdetail.Model:
		ctx.Files = v.Files()
		ctx.PRNumber = v.PRNumber()
	}

	chatW := m.width * 2 / 3
	if chatW < 50 {
		chatW = 50
	}
	chatH := m.height * 2 / 3
	if chatH > m.height-chromeHeight-4 {
		chatH = m.height - chromeHeight - 4
	}
	if chatH < 10 {
		chatH = 10
	}

	if !m.chatInitialized {
		m.copilotChat = copilot.NewChat(m.chatClient, ctx, m.ctx.Username, chatW-4, chatH)
		m.chatInitialized = true
	}
	m.mode = modeCopilotChat
	cmds := []tea.Cmd{m.chatClient.ListenCmd()}
	// Restart spinner if chat is still waiting for a response.
	if resume := m.copilotChat.ResumeCmd(); resume != nil {
		cmds = append(cmds, resume)
	}
	return m, tea.Batch(cmds...)
}

func (m Model) renderChatOverlay(bg string, bgHeight int) string {
	chatView := m.copilotChat.View()
	chatLines := strings.Split(chatView, "\n")

	modalW := m.width * 2 / 3
	if modalW < 50 {
		modalW = 50
	}
	if modalW > m.width-4 {
		modalW = m.width - 4
	}
	innerW := modalW - 4
	modalH := len(chatLines) + 2

	padY := (bgHeight - modalH) / 3
	if padY < 1 {
		padY = 1
	}

	bc := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	titleStr := " " + lipgloss.NewStyle().Bold(true).Render("Copilot") + " "
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

	var modalLines []string
	modalLines = append(modalLines, topBorder)
	for _, cl := range chatLines {
		clW := lipgloss.Width(cl)
		pad := innerW - clW
		if pad < 0 {
			pad = 0
		}
		modalLines = append(modalLines, side+" "+cl+strings.Repeat(" ", pad)+" "+side)
	}
	modalLines = append(modalLines, bottomBorder)

	bgLines := strings.Split(bg, "\n")
	padX := (m.width - modalW) / 2
	rightStart := padX + modalW

	for i, ml := range modalLines {
		row := padY + i
		if row >= 0 && row < len(bgLines) {
			bgLine := bgLines[row]
			bgW := lipgloss.Width(bgLine)

			left := ""
			if padX > 0 {
				left = truncateVisible(bgLine, padX)
				leftW := lipgloss.Width(left)
				if leftW < padX {
					left += strings.Repeat(" ", padX-leftW)
				}
			}

			right := ""
			if bgW > rightStart {
				right = xansi.Cut(bgLine, rightStart, bgW)
			}

			bgLines[row] = left + "\033[0m" + ml + "\033[0m" + right
		}
	}

	return strings.Join(bgLines, "\n")
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

		// Mode switching (available when in localdiff).
		if _, ok := m.activeView.(localdiff.Model); ok {
			items = append(items,
				picker.Item{Label: "Working Tree", Description: "Uncommitted changes", Value: "working", Keywords: []string{"mode", "unstaged"}},
				picker.Item{Label: "Staged", Description: "Staged changes", Value: "staged", Keywords: []string{"mode", "cached", "index"}},
			)
			branch, _ := git.CurrentBranch(m.repoRoot)
			defaultBranch, _ := git.DefaultBranch(m.repoRoot)
			if branch != defaultBranch {
				items = append(items, picker.Item{Label: "Branch Diff", Description: "vs " + defaultBranch, Value: "branch", Keywords: []string{"mode", "compare"}})
			}
		}
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
