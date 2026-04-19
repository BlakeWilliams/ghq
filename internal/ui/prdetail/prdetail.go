package prdetail

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/blakewilliams/ghq/internal/review/agents"
	"github.com/blakewilliams/ghq/internal/review/agents/copilot"
	"github.com/blakewilliams/ghq/internal/review/comments"
	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/git/watcher"
	"github.com/blakewilliams/ghq/internal/ui/components"
	"github.com/blakewilliams/ghq/internal/ui/diffviewer"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	"github.com/blakewilliams/ghq/internal/ui/uictx"
	"github.com/google/uuid"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"charm.land/lipgloss/v2"
)

// --- GitHub data messages and commands ---

type ReviewsLoadedMsg struct {
	Reviews []github.Review
	Number  int
}

type CommentsLoadedMsg struct {
	Comments []github.IssueComment
	Number   int
}

type ReviewCommentsLoadedMsg struct {
	Comments []github.ReviewComment
	Number   int
}

type CheckRunsLoadedMsg struct {
	CheckRuns []github.CheckRun
	Ref       string
}

type PRFilesLoadedMsg struct {
	Files  []github.PullRequestFile
	Number int
}

type CommentCreatedMsg struct {
	Comment github.ReviewComment
	Number  int
}

type CommentErrorMsg struct {
	Err error
}

func fetchPullRequestFiles(c *github.CachedClient, owner, repo string, number int) tea.Cmd {
	data, found, refetch := c.GetPullRequestFiles(owner, repo, number)
	return uictx.CachedCmd(data, found, refetch, func(files []github.PullRequestFile) tea.Msg {
		return PRFilesLoadedMsg{Files: files, Number: number}
	})
}

func fetchReviews(c *github.CachedClient, owner, repo string, number int) tea.Cmd {
	data, found, refetch := c.GetReviews(owner, repo, number)
	return uictx.CachedCmd(data, found, refetch, func(reviews []github.Review) tea.Msg {
		return ReviewsLoadedMsg{Reviews: reviews, Number: number}
	})
}

func fetchIssueComments(c *github.CachedClient, owner, repo string, number int) tea.Cmd {
	data, found, refetch := c.GetIssueComments(owner, repo, number)
	return uictx.CachedCmd(data, found, refetch, func(comments []github.IssueComment) tea.Msg {
		return CommentsLoadedMsg{Comments: comments, Number: number}
	})
}

func fetchReviewComments(c *github.CachedClient, owner, repo string, number int) tea.Cmd {
	data, found, refetch := c.GetReviewComments(owner, repo, number)
	return uictx.CachedCmd(data, found, refetch, func(comments []github.ReviewComment) tea.Msg {
		return ReviewCommentsLoadedMsg{Comments: comments, Number: number}
	})
}

func fetchCheckRuns(c *github.CachedClient, owner, repo, ref string) tea.Cmd {
	data, found, refetch := c.GetCheckRuns(owner, repo, ref)
	return uictx.CachedCmd(data, found, refetch, func(checks []github.CheckRun) tea.Msg {
		return CheckRunsLoadedMsg{CheckRuns: checks, Ref: ref}
	})
}

func createReviewComment(c *github.CachedClient, owner, repo string, number int, body, commitID, path string, line int, side string, startLine int, startSide string) tea.Cmd {
	return func() tea.Msg {
		comment, err := c.CreateReviewComment(owner, repo, number, body, commitID, path, line, side, startLine, startSide)
		if err != nil {
			return CommentErrorMsg{Err: err}
		}
		return CommentCreatedMsg{Comment: comment, Number: number}
	}
}

func replyToReviewComment(c *github.CachedClient, owner, repo string, number int, commentID int, body string) tea.Cmd {
	return func() tea.Msg {
		comment, err := c.ReplyToReviewComment(owner, repo, number, commentID, body)
		if err != nil {
			return CommentErrorMsg{Err: err}
		}
		return CommentCreatedMsg{Comment: comment, Number: number}
	}
}

// Nerdfont icon constants.
const (
	iconCheckCircle = "\U000f05e0" // 󰗠 nf-md-check_circle
	iconXCircle     = "\U000f0159" // 󰅙 nf-md-close_circle
	iconComment     = "\U000f0188" // 󰆈 nf-md-comment
	iconSlash       = "\U000f0737" // 󰜷 nf-md-cancel
	iconClock       = "\U000f0954" // 󰥔 nf-md-clock_outline
	iconReview      = "\U000f0567" // 󰕧 nf-md-shield_account
	iconComments    = "\U000f0e1c" // 󰸜 nf-md-comment_multiple
	iconAuthor      = "\U000f0004" // 󰀄 nf-md-account
	iconFile        = "\U000f0214" // 󰈔 nf-md-file
	iconFileTree    = "\U000f0253" // 󰉓 nf-md-file_tree
	iconFolder      = "\U000f024b" // 󰉋 nf-md-folder
	iconArrowUp     = "\U000f005d" // 󰁝 nf-md-arrow_up
	iconArrowDown   = "\U000f0045" // 󰁅 nf-md-arrow_down
	iconLoading     = "\U000f0772" // 󰝲 nf-md-loading
	iconGit         = "\U000f02a2" // 󰊢 nf-md-git
	iconPR          = "\U000f0041" // 󰁁 nf-md-arrow_top_right (source-branch)
	iconMerge       = "\U000f0261" // 󰉡 nf-md-source_merge (call_merge)
	iconClose       = "\U000f0156" // 󰅖 nf-md-close
	iconDraft       = "\U000f0613" // 󰘓 nf-md-pencil
	iconOpen        = "\U000f0130" // 󰄰 nf-md-checkbox_blank_circle_outline
	iconPlus        = "\U000f0415" // 󰐕 nf-md-plus
	iconMinus       = "\U000f0374" // 󰍴 nf-md-minus
	iconRename      = "\U000f0453" // 󰑓 nf-md-rename_box
	iconChevron     = "\U000f0142" // 󰅂 nf-md-chevron_right
	iconArrowRight  = "\U000f0054" // 󰁔 nf-md-arrow_right
	iconPointer     = "\U000f0142" // 󰅂 nf-md-chevron_right (cursor)
	iconChecks      = "\U000f0134" // 󰄴 nf-md-check
)

type sidebarType int

const (
	sidebarComments sidebarType = iota
	sidebarReviews
	sidebarChecks
)

type descRenderedMsg struct {
	content  string
	prNumber int
}

type fileHighlightedMsg struct {
	highlight components.HighlightedDiff
	index     int
	prNumber  int
}

// prefetchDoneMsg signals that background prefetch of file contents completed.
type prefetchDoneMsg struct{}

type Model struct {
	pr    github.PullRequest
	ctx   *uictx.Context
	owner string
	repo  string

	// Embedded diff viewer (shared with localdiff).
	dv diffviewer.DiffViewer

	// Content data
	descContent    string
	reviews        []github.Review
	comments       []github.IssueComment
	reviewComments []github.ReviewComment
	checkRuns      []github.CheckRun

	// Modal (comments/reviews/checks)
	showSidebar bool
	sidebarType sidebarType
	sidebarVP   viewport.Model

	// PR-specific comment state
	replyToID *int

	// Local comments
	localComments *comments.CommentStore
	repoRoot      string // non-empty if branch is checked out locally
	localBranch   bool   // true if PR branch == local branch
	refWatcher    *watcher.RefWatcher
}

func New(pr github.PullRequest, ctx *uictx.Context, width, height int) Model {
	owner, repo := ctx.Owner, ctx.Repo
	if pr.Base.Repo != nil {
		owner = pr.Base.Repo.Owner.Login
		repo = pr.Base.Repo.Name
	}
	repoNWO := owner + "/" + repo
	dv := diffviewer.DiffViewer{
		Ctx:             ctx,
		Width:           width,
		Height:          height,
		CurrentFileIdx:  -1,
		SelectionAnchor: -1,
		Tree: components.FileTree{
			Width:      35,
			Height:     height - 2,
			Focused:    true,
			ChromeRows: 2,
		},
		CopilotState: diffviewer.NewCopilotState(),
	}
	dv.InitSpinner()
	dv.HelpMode = ctx.Config.HelpMode

	return Model{
		pr:            pr,
		ctx:           ctx,
		owner:         owner,
		repo:          repo,
		localComments: comments.LoadComments(repoNWO),
		dv:            dv,
	}
}

// SetLocalContext enables local-aware features when the PR branch is checked out.
func (m *Model) SetLocalContext(repoRoot string) {
	m.repoRoot = repoRoot
	m.localBranch = true
	// Close existing copilot client before creating a new one.
	if m.dv.Agent != nil {
		m.dv.Agent.Stop()
	}
	cp, _ := copilot.New(repoRoot)
	go cp.Start()
	m.dv.Agent = agents.New(cp)
	// Close existing watcher before creating a new one.
	if m.refWatcher != nil {
		m.refWatcher.Close()
	}
	// Watch for pushes to trigger PR re-fetch.
	rw, _ := watcher.NewRefWatcher(repoRoot, m.pr.Head.Ref)
	m.refWatcher = rw
}

func (m Model) PRNumber() int                    { return m.pr.Number }
func (m Model) Files() []github.PullRequestFile  { return m.dv.Files }

func (m Model) PRTitle() string {
	return m.pr.Title
}

func (m Model) RepoFullName() string {
	return m.owner + "/" + m.repo
}

func (m *Model) activeViewport() *viewport.Model {
	if m.showSidebar {
		return &m.sidebarVP
	}
	return &m.dv.VP
}

func (m Model) KeyBindings() []uictx.KeyBinding {
	return []uictx.KeyBinding{
		{Key: "j / k", Description: "Move cursor down / up", Keywords: []string{"navigate"}},
		{Key: "J / K", Description: "Extend selection range"},
		{Key: "h / l", Description: "Focus left / right pane"},
		{Key: "f", Description: "Toggle tree focus"},
		{Key: "^j / ^k", Description: "Previous / next file"},
		{Key: "^d / ^u", Description: "Scroll half page down / up"},
		{Key: "^f / ^b", Description: "Scroll full page down / up"},
		{Key: "g g", Description: "Go to top"},
		{Key: "G", Description: "Go to bottom"},
		{Key: "a", Description: "Add review comment"},
		{Key: "enter", Description: "Select file / open comment"},
		{Key: "c", Description: "Toggle comments sidebar"},
		{Key: "r", Description: "Toggle reviews sidebar"},
		{Key: "s", Description: "Toggle checks sidebar"},
		{Key: "esc", Description: "Close sidebar / cancel"},
	}
}

// StatusHints returns left and right hint groups for the status bar.
// Entries are pre-rendered.
func (m Model) StatusHints() (left, right []string) {
	if m.dv.Tree.Focused {
		left = append(left, styles.StatusBarKey.Render("f")+" "+styles.StatusBarHint.Render("unfocus tree"))
	} else {
		left = append(left, styles.StatusBarKey.Render("f")+" "+styles.StatusBarHint.Render("focus tree"))
	}
	left = append(left, styles.StatusBarKey.Render("h/l")+" "+styles.StatusBarHint.Render("panes"))
	left = append(left, styles.StatusBarKey.Render("^j/^k")+" "+styles.StatusBarHint.Render("files"))
	if !m.dv.Tree.Focused && m.dv.CurrentFileIdx >= 0 {
		left = append(left, styles.StatusBarKey.Render("J/K")+" "+styles.StatusBarHint.Render("select range"))
	}
	right = append(right, highlightHint("comments", "c"))
	right = append(right, highlightHint("reviews", "r"))
	right = append(right, highlightHint("checks", "s"))
	if m.localBranch {
		right = append(right, styles.StatusBarKey.Render("Local"))
	}
	return
}

// highlightHint renders a word with a single letter highlighted in magenta.
func highlightHint(word, key string) string {
	i := strings.Index(word, key)
	if i < 0 {
		return styles.StatusBarHint.Render(word)
	}
	return styles.StatusBarHint.Render(word[:i]) +
		styles.StatusBarKey.Render(string(word[i])) +
		styles.StatusBarHint.Render(word[i+1:])
}


func (m Model) Init() tea.Cmd {
	body := m.pr.Body
	width := m.descWidth()
	prNumber := m.pr.Number
	cmds := []tea.Cmd{
		func() tea.Msg {
			rendered := renderMarkdown(body, width)
			return descRenderedMsg{content: rendered, prNumber: prNumber}
		},
		fetchPullRequestFiles(m.ctx.Client, m.owner, m.repo, m.pr.Number),
		fetchReviews(m.ctx.Client, m.owner, m.repo, m.pr.Number),
		fetchIssueComments(m.ctx.Client, m.owner, m.repo, m.pr.Number),
		fetchReviewComments(m.ctx.Client, m.owner, m.repo, m.pr.Number),
		fetchCheckRuns(m.ctx.Client, m.owner, m.repo, m.pr.Head.SHA),
	}
	if m.refWatcher != nil {
		cmds = append(cmds, m.refWatcher.WaitCmd())
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (uictx.View, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.dv.Width = msg.Width
		m.dv.Height = msg.Height
		m.dv.Tree.Height = msg.Height - 2
		m.dv.VP.SetWidth(m.dv.RightPanelInnerWidth())
		m.dv.VP.SetHeight(m.dv.ViewportHeight())
		body := m.pr.Body
		width := m.descWidth()
		prNumber := m.pr.Number
		cmds := []tea.Cmd{func() tea.Msg {
			rendered := renderMarkdown(body, width)
			return descRenderedMsg{content: rendered, prNumber: prNumber}
		}}
		// Re-format diff files at the new width.
		if m.dv.FilesListLoaded {
			m.reformatAllFiles()
		}
		m.rebuildContent()
		return m, tea.Batch(cmds...)

	case tea.KeyPressMsg:
		var cmd tea.Cmd
		var handled bool
		m, cmd, handled = m.handleKey(msg)
		if handled {
			return m, cmd
		}

	case uictx.SelectFileMsg:
		for i, f := range m.dv.Files {
			if f.Filename == msg.Filename {
				m.dv.Tree.Cursor = m.dv.Tree.IndexForFile(i)
				m.dv.Tree.Focused = false
				cmd := m.selectTreeEntry()
				return m, cmd
			}
		}
		return m, nil

	case descRenderedMsg:
		if msg.prNumber == m.pr.Number {
			m.descContent = msg.content
			m.rebuildContent()
		}
		return m, nil

	case ReviewsLoadedMsg:
		if msg.Number == m.pr.Number {
			m.reviews = msg.Reviews
			m.rebuildSidebar()
		}
		return m, nil

	case CommentsLoadedMsg:
		if msg.Number == m.pr.Number {
			// Reverse so newest comments appear first.
			comments := msg.Comments
			for i, j := 0, len(comments)-1; i < j; i, j = i+1, j-1 {
				comments[i], comments[j] = comments[j], comments[i]
			}
			m.comments = comments
			m.rebuildSidebar()
		}
		return m, nil

	case ReviewCommentsLoadedMsg:
		if msg.Number == m.pr.Number {
			m.reviewComments = msg.Comments
			// Re-format files to include comments (cheap, highlights cached).
			if m.dv.FilesHighlighted > 0 {
				m.reformatAllFiles()
				m.rebuildContent()
			}
		}
		return m, nil

	case CheckRunsLoadedMsg:
		if msg.Ref == m.pr.Head.SHA {
			m.checkRuns = msg.CheckRuns
			m.rebuildSidebar()
		}
		return m, nil

	case PRFilesLoadedMsg:
		m.dv.Files = msg.Files
		m.dv.InitFileSlices(len(msg.Files))
		m.dv.FilesListLoaded = true
		// Parse diff lines for each file (for cursor navigation).
		for i, f := range msg.Files {
			m.dv.FileDiffs[i] = components.ParsePatchLines(f.Patch)
		}
		m.dv.Tree.SetFiles(m.dv.Files)
		m.rebuildContent()
		// Prefetch first 3 files into cache, then start rendering.
		cmds := m.prefetchFiles(3)
		cmds = append(cmds, m.startFileRendering())
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
		return m, nil

	case prefetchDoneMsg:
		return m, nil

	case spinner.TickMsg:
		if m.dv.SpinnerActive {
			var cmd tea.Cmd
			m.dv.Spinner, cmd = m.dv.Spinner.Update(msg)
			m.rebuildContent()
			return m, cmd
		}
		return m, nil

	case fileHighlightedMsg:
		if msg.prNumber != m.pr.Number || msg.index >= len(m.dv.HighlightedFiles) {
			return m, nil
		}
		m.dv.HighlightedFiles[msg.index] = msg.highlight
		m.dv.FilesHighlighted = msg.index + 1
		// Stop spinner if this is the file we were waiting on.
		if msg.index == m.dv.CurrentFileIdx {
			m.dv.SpinnerActive = false
		}
		// Format at current width (cheap).
		m.formatFile(msg.index)
		if m.dv.FilesHighlighted >= len(m.dv.Files) {
			m.dv.FilesLoading = false
		}
		m.rebuildContent()
		if m.dv.FilesHighlighted < len(m.dv.Files) {
			return m, m.highlightFileCmd(m.dv.FilesHighlighted)
		}
		return m, nil

	case CommentCreatedMsg:
		if msg.Number == m.pr.Number {
			m.dv.Composing = false
			m.reviewComments = append(m.reviewComments, msg.Comment)
			// Re-format only the affected file to include the new comment.
			if fileIdx := m.dv.FileIndexForPath(msg.Comment.Path); fileIdx >= 0 {
				m.formatFile(fileIdx)
			}
			m.rebuildContent()
		}
		return m, nil

	case CommentErrorMsg:
		// TODO: show error to user
		return m, nil

	case uictx.QueryErrMsg:
		return m, nil

	case copilotTickMsg:
		if !m.dv.CopilotState.HasPending() {
			return m, nil
		}

		// Drain all buffered events, accumulate state only.
		dirtyFiles := map[int]bool{}
		if m.dv.Agent != nil {
			for _, ev := range m.dv.Agent.Drain() {
				if fileIdx := m.handleAgentEvent(ev); fileIdx >= 0 {
					dirtyFiles[fileIdx] = true
				}
			}
		}

		m.dv.CopilotState.AdvanceDots()

		// Re-render files that had completed/error events.
		for fileIdx := range dirtyFiles {
			m.formatFile(fileIdx)
		}

		// Re-render the current file if it has pending threads (for dots animation + streaming).
		for _, info := range m.dv.CopilotState.Pending {
			if fileIdx := m.dv.FileIndexForPath(info.Path); fileIdx >= 0 && fileIdx == m.dv.CurrentFileIdx {
				if !dirtyFiles[fileIdx] {
					m.formatFile(fileIdx)
				}
				break
			}
		}
		m.rebuildContent()
		if m.dv.CopilotState.HasPending() {
			return m, tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
				return copilotTickMsg{}
			})
		}
		return m, nil

	case watcher.RefChangedMsg:
		// Branch ref changed — likely a push. Wait 2s for GitHub to process, then re-fetch.
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
			return refetchPRMsg{}
		})

	case refetchPRMsg:
		// Invalidate and re-fetch PR data.
		m.ctx.Client.InvalidatePR(m.owner, m.repo, m.pr.Number)
		cmds := []tea.Cmd{
			fetchPullRequestFiles(m.ctx.Client, m.owner, m.repo, m.pr.Number),
			fetchReviews(m.ctx.Client, m.owner, m.repo, m.pr.Number),
			fetchReviewComments(m.ctx.Client, m.owner, m.repo, m.pr.Number),
			fetchCheckRuns(m.ctx.Client, m.owner, m.repo, m.pr.Head.SHA),
		}
		if m.refWatcher != nil {
			cmds = append(cmds, m.refWatcher.WaitCmd())
		}
		return m, tea.Batch(cmds...)

	case editorFinishedMsg:
		if msg.err == nil && msg.content != "" {
			m.dv.CommentInput.SetValue(msg.content)
		}
		m.rebuildContent()
		return m, nil
	}

	// When composing a comment, delegate all input to the textarea.
	if m.dv.Composing {
		var cmd tea.Cmd
		m.dv.CommentInput, cmd = m.dv.CommentInput.Update(msg)
		return m, cmd
	}

	if m.dv.VPReady {
		vp := m.activeViewport()
		prevOffset := vp.YOffset()
		var cmd tea.Cmd
		*vp, cmd = vp.Update(msg)
		// If the main viewport scrolled, sync the diff cursor within the current file.
		if vp == &m.dv.VP && m.dv.VP.YOffset() != prevOffset && m.dv.CurrentFileIdx >= 0 {
			m.dv.SyncDiffCursorToViewport()
		}
		return m, cmd
	}
	return m, nil
}

func (m Model) HandleKey(msg tea.KeyPressMsg) (uictx.View, tea.Cmd, bool) {
	return m.handleKey(msg)
}

func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	// When composing a comment, handle textarea keys.
	if m.dv.Composing {
		return m.handleCommentKey(msg)
	}

	// When searching, route all keys to the search handler.
	if m.dv.Searching {
		m.dv.HandleSearchKey(msg.String(), msg.Text)
		return m, nil, true
	}

	// Close modal on esc.
	if msg.String() == "esc" && m.showSidebar {
		m.showSidebar = false
		return m, nil, true
	}

	// Clear selection on esc.
	if msg.String() == "esc" && m.dv.SelectionAnchor >= 0 {
		m.dv.SelectionAnchor = -1
		return m, nil, true
	}

	switch msg.String() {
	case "f", "h", "left", "l", "right":
		if m.dv.HandleNavKey(msg.String()) == diffviewer.KeyHandled {
			return m, nil, true
		}
	case "c":
		m.toggleSidebar(sidebarComments)
		return m, nil, true
	case "r":
		m.toggleSidebar(sidebarReviews)
		return m, nil, true
	case "s":
		m.toggleSidebar(sidebarChecks)
		return m, nil, true
	case "ctrl+k":
		m.dv.Tree.MoveSelection(-1)
		cmd := m.selectTreeEntry()
		return m, cmd, true
	case "ctrl+j":
		m.dv.Tree.MoveSelection(1)
		cmd := m.selectTreeEntry()
		return m, cmd, true
	case "j", "down", "k", "up", "J", "shift+down", "K", "shift+up":
		if m.showSidebar {
			return m, nil, false // let viewport scroll
		}
		if m.dv.HandleNavKey(msg.String()) == diffviewer.KeyHandled {
			return m, nil, true
		}
	case "enter":
		if m.dv.Tree.Focused {
			// Select current tree entry — switch to right panel.
			cmd := m.selectTreeEntry()
			m.dv.Tree.Focused = false
			return m, cmd, true
		}
		// Open comment input on diff line.
		if !m.showSidebar && m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
			return m.openCommentInput()
		}
	case "a":
		if !m.showSidebar && m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
			return m.openCommentInput()
		}
	case "/":
		if !m.dv.Tree.Focused && m.dv.CurrentFileIdx >= 0 {
			m.dv.StartSearch()
			return m, nil, true
		}
	case "n":
		if !m.dv.Tree.Focused && m.dv.SearchPattern != nil && len(m.dv.SearchMatches) > 0 {
			m.dv.SearchNext()
			return m, nil, true
		}
	case "N":
		if !m.dv.Tree.Focused && m.dv.SearchPattern != nil && len(m.dv.SearchMatches) > 0 {
			m.dv.SearchPrev()
			return m, nil, true
		}
	case "ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b":
		if m.dv.HandleNavKey(msg.String()) == diffviewer.KeyHandled {
			return m, nil, true
		}
	case "G":
		m.dv.WaitingG = false
		if m.dv.Tree.Focused {
			totalEntries := 2 + len(m.dv.Tree.Entries)
			m.dv.Tree.MoveCursorBy(totalEntries)
		} else {
			m.activeViewport().GotoBottom()
			if m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
				m.dv.SyncDiffCursorToViewport()
			}
		}
		return m, nil, true
	case "g":
		if m.dv.WaitingG {
			m.dv.WaitingG = false
			if m.dv.Tree.Focused {
				m.dv.Tree.MoveCursorBy(-2 - len(m.dv.Tree.Entries))
			} else {
				m.activeViewport().GotoTop()
				if m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
					m.dv.SyncDiffCursorToViewport()
				}
			}
			return m, nil, true
		}
		m.dv.WaitingG = true
		return m, nil, true
	case "ctrl+g":
		// Absorb outside composing mode.
		return m, nil, true
	default:
		m.dv.WaitingG = false
	}
	return m, nil, false
}

// selectTreeEntry updates the right panel based on the current tree cursor.
func (m *Model) selectTreeEntry() tea.Cmd {
	m.dv.SelectionAnchor = -1
	// Don't clear search — re-run after switching files.
	fileIdx := m.dv.Tree.FileIndex()
	if fileIdx < 0 {
		// Directory or out-of-range — show overview.
		m.dv.SpinnerActive = false
		m.dv.CurrentFileIdx = -1
		m.rebuildContent()
		m.dv.VP.GotoTop()
		return nil
	}
	m.dv.CurrentFileIdx = fileIdx
	m.dv.DiffCursor = m.dv.FirstNonHunkLine(fileIdx)
	// If the file hasn't been highlighted yet, show a spinner
	// and kick off highlighting for this file directly (the
	// sequential chain may not have reached this index yet).
	needsHighlight := fileIdx < len(m.dv.RenderedFiles) && m.dv.RenderedFiles[fileIdx] == "" &&
		(fileIdx >= len(m.dv.HighlightedFiles) || m.dv.HighlightedFiles[fileIdx].File.Filename == "")
	if needsHighlight {
		m.dv.SpinnerActive = true
		m.rebuildContent()
		m.dv.VP.GotoTop()
		return tea.Batch(m.dv.Spinner.Tick, m.highlightFileCmd(fileIdx))
	}
	m.dv.SpinnerActive = false
	// Ensure file is formatted (may have been invalidated by resize/comment).
	if fileIdx < len(m.dv.RenderedFiles) && m.dv.RenderedFiles[fileIdx] == "" {
		m.formatFile(fileIdx)
	}
	m.rebuildContent()
	m.dv.VP.GotoTop()
	return nil
}

// editorFinishedMsg is sent when $EDITOR exits.
type copilotTickMsg struct{}
type refetchPRMsg struct{}


type editorFinishedMsg struct {
	content string
	err     error
}

// handleAgentEvent processes a single event drained from the agent.
// Returns the file index that needs re-rendering, or -1 if none.
func (m *Model) handleAgentEvent(ev agents.AgentEvent) int {
	switch ev.Kind {
	case agents.EventDelta:
		p := ev.Payload.(agents.DeltaPayload)
		blocks := m.dv.CopilotState.ReplyBuf[ev.CommentID]
		if n := len(blocks); n > 0 {
			if tb, ok := blocks[n-1].(comments.TextBlock); ok {
				blocks[n-1] = comments.TextBlock{Text: tb.Text + p.Delta}
			} else {
				blocks = append(blocks, comments.TextBlock{Text: p.Delta})
			}
		} else {
			blocks = append(blocks, comments.TextBlock{Text: p.Delta})
		}
		m.dv.CopilotState.ReplyBuf[ev.CommentID] = blocks
		return -1

	case agents.EventDone:
		blocks := m.dv.CopilotState.ReplyBuf[ev.CommentID]
		delete(m.dv.CopilotState.ReplyBuf, ev.CommentID)
		pendingPath := m.dv.CopilotState.PendingPath(ev.CommentID)
		m.dv.CopilotState.ClearPending(ev.CommentID)
		body := comments.BodyFromBlocks(blocks)
		if body != "" || len(blocks) > 0 {
			for _, c := range m.localComments.Comments {
				if c.ID == ev.CommentID {
					reply := comments.LocalComment{
						ID:          uuid.New().String(),
						Body:        strings.TrimSpace(body),
						Path:        c.Path,
						Line:        c.Line,
						Side:        c.Side,
						InReplyToID: c.ID,
						Author:      "copilot",
						CreatedAt:   time.Now(),
						Blocks:      blocks,
					}
					m.localComments.Add(reply)
					break
				}
			}
		}
		return m.dv.FileIndexForPath(pendingPath)

	case agents.EventError:
		pendingPath := m.dv.CopilotState.PendingPath(ev.CommentID)
		m.dv.CopilotState.ClearPending(ev.CommentID)
		return m.dv.FileIndexForPath(pendingPath)

	case agents.EventToolStart:
		p := ev.Payload.(agents.ToolPayload)
		blocks := m.dv.CopilotState.ReplyBuf[ev.CommentID]

		// report_intent is not a real tool — store as the current intent.
		if p.Name == "report_intent" {
			if m.dv.CopilotState.Intent == nil {
				m.dv.CopilotState.Intent = make(map[string]string)
			}
			if p.Arguments != "" {
				m.dv.CopilotState.Intent[ev.CommentID] = p.Arguments
			}
			if n := len(blocks); n > 0 {
				if tg, ok := blocks[n-1].(comments.ToolGroupBlock); ok && p.Arguments != "" {
					tg.Label = p.Arguments
					blocks[n-1] = tg
					m.dv.CopilotState.ReplyBuf[ev.CommentID] = blocks
				}
			}
			return -1
		}

		tc := comments.ToolCall{Name: p.Name, CallID: p.CallID, Status: "running", Arguments: p.Arguments}
		if n := len(blocks); n > 0 {
			if tg, ok := blocks[n-1].(comments.ToolGroupBlock); ok {
				tg.Tools = append(tg.Tools, tc)
				blocks[n-1] = tg
			} else {
				blocks = append(blocks, comments.ToolGroupBlock{Tools: []comments.ToolCall{tc}})
			}
		} else {
			blocks = append(blocks, comments.ToolGroupBlock{Tools: []comments.ToolCall{tc}})
		}
		m.dv.CopilotState.ReplyBuf[ev.CommentID] = blocks
		return -1

	case agents.EventToolComplete:
		p := ev.Payload.(agents.ToolPayload)
		if p.Name == "report_intent" {
			return -1
		}
		blocks := m.dv.CopilotState.ReplyBuf[ev.CommentID]
		for i := len(blocks) - 1; i >= 0; i-- {
			if tg, ok := blocks[i].(comments.ToolGroupBlock); ok {
				for j := range tg.Tools {
					if tg.Tools[j].Status != "running" {
						continue
					}
					matched := false
					if p.CallID != "" && tg.Tools[j].CallID == p.CallID {
						matched = true
					} else if p.CallID == "" && tg.Tools[j].Name == p.Name {
						matched = true
					}
					if matched {
						tg.Tools[j].Status = "done"
						blocks[i] = tg
						m.dv.CopilotState.ReplyBuf[ev.CommentID] = blocks
						return -1
					}
				}
			}
		}
		return -1
	}
	return -1
}

func (m Model) handleCommentKey(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		m.dv.Composing = false
		m.dv.SelectionAnchor = -1
		m.rebuildContent()
		return m, nil, true
	case "shift+enter":
		m.dv.CommentInput.InsertString("\n")
		m.rebuildContent()
		return m, nil, true
	case "enter":
		// Submit as local comment + send to copilot.
		body := strings.TrimSpace(m.dv.CommentInput.Value())
		if body == "" {
			m.dv.Composing = false
			m.dv.SelectionAnchor = -1
			m.rebuildContent()
			return m, nil, true
		}
		m.dv.Composing = false

		comment := comments.LocalComment{
			ID:        uuid.New().String(),
			Body:      body,
			Path:      m.dv.CommentFile,
			Line:      m.dv.CommentLine,
			Side:      m.dv.CommentSide,
			StartLine: m.dv.CommentStartLine,
			StartSide: m.dv.CommentStartSide,
			Author:    m.dv.AuthorName(),
			CreatedAt: time.Now(),
		}
		m.localComments.Add(comment)
		m.dv.SelectionAnchor = -1
		m.formatFile(m.dv.CurrentFileIdx)
		m.rebuildContent()

		if m.dv.Agent != nil {
			m.dv.CopilotState.SetPending(comment.ID, comment.Path, comment.Line, comment.Side)
			return m, tea.Batch(
				m.dv.Agent.SendComment(comment.ID, body, comment.Path, "Branch", "", nil),
				tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return copilotTickMsg{} }),
			), true
		}
		return m, nil, true
	case "alt+enter":
		// Submit comment to GitHub API.
		body := strings.TrimSpace(m.dv.CommentInput.Value())
		if body == "" {
			m.dv.Composing = false
			m.dv.SelectionAnchor = -1
			m.rebuildContent()
			return m, nil, true
		}
		m.dv.Composing = false
		var cmd tea.Cmd
		if m.replyToID != nil {
			cmd = replyToReviewComment(m.ctx.Client, m.owner, m.repo, m.pr.Number, *m.replyToID, body)
		} else {
			cmd = createReviewComment(
				m.ctx.Client, m.owner, m.repo, m.pr.Number, body, m.pr.Head.SHA,
				m.dv.CommentFile, m.dv.CommentLine, m.dv.CommentSide,
				m.dv.CommentStartLine, m.dv.CommentStartSide,
			)
		}
		m.dv.SelectionAnchor = -1
		m.rebuildContent()
		return m, cmd, true
	case "alt+s":
		// Insert a suggestion block with the selected code.
		suggestion := m.buildSuggestionBlock()
		if suggestion != "" {
			cur := m.dv.CommentInput.Value()
			if cur != "" && !strings.HasSuffix(cur, "\n") {
				cur += "\n"
			}
			m.dv.CommentInput.SetValue(cur + suggestion)
			m.rebuildContent()
		}
		return m, nil, true
	case "ctrl+g":
		return m.openEditorForComment()
	}
	// Delegate to textarea.
	var cmd tea.Cmd
	m.dv.CommentInput, cmd = m.dv.CommentInput.Update(msg)
	m.rebuildContent()
	return m, cmd, true
}

// buildSuggestionBlock returns a GitHub suggestion fenced code block
// pre-filled with the code from the commented line(s). Returns "" if the
// selection contains deleted lines, since suggestions only apply to the
// new side of the diff.
func (m Model) buildSuggestionBlock() string {
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.FileDiffs) {
		return ""
	}
	lines := m.dv.FileDiffs[idx]

	selStart, selEnd := m.dv.DiffCursor, m.dv.DiffCursor
	if m.dv.SelectionAnchor >= 0 && m.dv.SelectionAnchor != m.dv.DiffCursor {
		selStart, selEnd = m.dv.SelectionAnchor, m.dv.DiffCursor
		if selStart > selEnd {
			selStart, selEnd = selEnd, selStart
		}
	}

	var code []string
	for i := selStart; i <= selEnd; i++ {
		if i >= len(lines) {
			continue
		}
		dl := lines[i]
		if dl.Type == components.LineHunk {
			continue
		}
		if dl.Type == components.LineDel {
			return ""
		}
		code = append(code, dl.Content)
	}

	if len(code) == 0 {
		return ""
	}
	return "```suggestion\n" + strings.Join(code, "\n") + "\n```"
}

// canSuggest returns true when none of the commented lines are deletions,
// meaning a suggestion block would be valid.
func (m Model) canSuggest() bool {
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.FileDiffs) {
		return false
	}
	lines := m.dv.FileDiffs[idx]

	selStart, selEnd := m.dv.DiffCursor, m.dv.DiffCursor
	if m.dv.SelectionAnchor >= 0 && m.dv.SelectionAnchor != m.dv.DiffCursor {
		selStart, selEnd = m.dv.SelectionAnchor, m.dv.DiffCursor
		if selStart > selEnd {
			selStart, selEnd = selEnd, selStart
		}
	}

	for i := selStart; i <= selEnd; i++ {
		if i >= len(lines) {
			continue
		}
		if lines[i].Type == components.LineDel {
			return false
		}
	}
	return true
}

func (m Model) openEditorForComment() (Model, tea.Cmd, bool) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	tmpFile, err := os.CreateTemp("", "ghq-comment-*.md")
	if err != nil {
		return m, nil, true
	}

	// Seed with current textarea content.
	if v := m.dv.CommentInput.Value(); v != "" {
		tmpFile.WriteString(v)
	}
	tmpFile.Close()
	path := tmpFile.Name()

	c := exec.Command(editor, path)
	return m, tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			os.Remove(path)
			return editorFinishedMsg{err: err}
		}
		content, readErr := os.ReadFile(path)
		os.Remove(path)
		return editorFinishedMsg{content: string(content), err: readErr}
	}), true
}

func (m Model) openCommentInput() (Model, tea.Cmd, bool) {
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.FileDiffs) || idx >= len(m.dv.Files) {
		return m, nil, false
	}
	lines := m.dv.FileDiffs[idx]
	if m.dv.DiffCursor >= len(lines) {
		return m, nil, false
	}
	dl := lines[m.dv.DiffCursor]

	// Skip hunk headers — can't comment on those.
	if dl.Type == components.LineHunk {
		return m, nil, false
	}

	m.dv.CommentFile = m.dv.Files[idx].Filename

	// Determine if we have a multi-line selection.
	m.dv.CommentStartLine = 0
	m.dv.CommentStartSide = ""

	if m.dv.SelectionAnchor >= 0 && m.dv.SelectionAnchor != m.dv.DiffCursor {
		selStart, selEnd := m.dv.SelectionAnchor, m.dv.DiffCursor
		if selStart > selEnd {
			selStart, selEnd = selEnd, selStart
		}

		startDL := lines[selStart]
		endDL := lines[selEnd]

		// Both ends must be non-hunk lines.
		if startDL.Type == components.LineHunk || endDL.Type == components.LineHunk {
			return m, nil, false
		}

		// The end line is the "line" in the API; the start line is "start_line".
		if endDL.Type == components.LineDel {
			m.dv.CommentLine = endDL.OldLineNo
			m.dv.CommentSide = "LEFT"
		} else {
			m.dv.CommentLine = endDL.NewLineNo
			m.dv.CommentSide = "RIGHT"
		}

		if startDL.Type == components.LineDel {
			m.dv.CommentStartLine = startDL.OldLineNo
			m.dv.CommentStartSide = "LEFT"
		} else {
			m.dv.CommentStartLine = startDL.NewLineNo
			m.dv.CommentStartSide = "RIGHT"
		}
	} else {
		// Single-line comment.
		if dl.Type == components.LineDel {
			m.dv.CommentLine = dl.OldLineNo
			m.dv.CommentSide = "LEFT"
		} else {
			m.dv.CommentLine = dl.NewLineNo
			m.dv.CommentSide = "RIGHT"
		}
	}

	// Check if there's an existing comment thread on this line to reply to.
	// Only for single-line comments — multi-line always creates a new thread.
	if m.dv.CommentStartLine > 0 {
		m.replyToID = nil
	} else {
		m.replyToID = m.findThreadRootOnLine(m.dv.CommentFile, m.dv.CommentLine)
	}

	// Create textarea.
	ta := textarea.New()
	ta.SetWidth(m.dv.ContentWidth() - 10 - 6) // gutter + border + padding
	ta.SetHeight(5)
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Focus()
	if m.replyToID != nil {
		ta.Placeholder = "Reply to thread..."
	} else {
		ta.Placeholder = "Add a comment..."
	}
	m.dv.CommentInput = ta
	m.dv.Composing = true
	m.rebuildContent()
	return m, ta.Focus(), true
}

// findThreadRootOnLine returns the ID of the root comment on a given line, if any.
func (m Model) findThreadRootOnLine(path string, line int) *int {
	for _, c := range m.reviewComments {
		if c.Path != path || c.InReplyToID != nil {
			continue
		}
		cLine := 0
		if c.Line != nil {
			cLine = *c.Line
		} else if c.OriginalLine != nil {
			cLine = *c.OriginalLine
		}
		if cLine == line {
			id := c.ID
			return &id
		}
	}
	return nil
}

// insertCommentBox inserts the comment input textarea into the rendered file
// content at the cursor position. Only called when composing.
func (m Model) insertCommentBox(rendered string, fileIdx int) string {
	lines := strings.Split(rendered, "\n")
	cursorRenderedLine := -1
	if fileIdx < len(m.dv.FileDiffOffsets) && m.dv.DiffCursor < len(m.dv.FileDiffOffsets[fileIdx]) {
		cursorRenderedLine = m.dv.FileDiffOffsets[fileIdx][m.dv.DiffCursor]
	}
	if cursorRenderedLine >= 0 && cursorRenderedLine < len(lines) {
		inputBox := m.renderCommentBox()
		inputLines := strings.Split(inputBox, "\n")
		after := make([]string, len(lines)-cursorRenderedLine-1)
		copy(after, lines[cursorRenderedLine+1:])
		lines = append(lines[:cursorRenderedLine+1], inputLines...)
		lines = append(lines, after...)
	}
	return strings.Join(lines, "\n")
}


// renderCommentBox renders the inline comment textarea with a rounded border
// and hint line below, indented past the diff gutter.
func (m Model) renderCommentBox() string {
	var gutter int
	if m.dv.CurrentFileIdx >= 0 && m.dv.CurrentFileIdx < len(m.dv.FileDiffs) {
		gutter = components.TotalGutterWidth(components.GutterColWidth(m.dv.FileDiffs[m.dv.CurrentFileIdx]))
	} else {
		gutter = components.TotalGutterWidth(components.DefaultGutterColWidth)
	}
	indent := strings.Repeat(" ", gutter)
	boxW := m.dv.ContentWidth() - gutter - 2

	taView := m.dv.CommentInput.View()

	// Draw top/bottom borders manually.
	bc := m.dv.BorderStyle()
	topRule := bc.Render("╭" + strings.Repeat("─", boxW) + "╮")
	bottomRule := bc.Render("╰" + strings.Repeat("─", boxW) + "╯")
	side := bc.Render("│")

	var boxLines []string
	boxLines = append(boxLines, indent+topRule)
	for _, line := range strings.Split(taView, "\n") {
		visW := lipgloss.Width(line)
		padW := boxW - 2 - visW // -2 for " " padding each side
		if padW < 0 {
			padW = 0
		}
		boxLines = append(boxLines, indent+side+" "+line+strings.Repeat(" ", padW)+" "+side)
	}
	boxLines = append(boxLines, indent+bottomRule)

	leftHint := "esc cancel · ^g $EDITOR"
	if m.canSuggest() {
		leftHint += " · alt+s suggest"
	}
	left := dimStyle.Render(leftHint)
	right := dimStyle.Render("alt+enter submit")
	hintGap := boxW - lipgloss.Width(left) - lipgloss.Width(right)
	if hintGap < 1 {
		hintGap = 1
	}
	boxLines = append(boxLines, indent+" "+left+strings.Repeat(" ", hintGap)+right)

	return strings.Join(boxLines, "\n")
}

// syncDiffCursorToViewport updates diffCursor to match the viewport scroll
// position within the current file (e.g. after ctrl+d/f/u/b).
func (m *Model) toggleSidebar(st sidebarType) {
	if m.showSidebar && m.sidebarType == st {
		m.showSidebar = false
	} else {
		m.showSidebar = true
		m.sidebarType = st
		m.rebuildSidebar()
	}
}

var dimStyle = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)

func (m Model) renderWithLeftSidebarFrom(view string) string {
	treeW := m.dv.Tree.Width
	divider := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("│")

	treeLines := components.RenderFileTree(m.dv.Tree.Entries, m.dv.Tree.Files, m.dv.Tree.Cursor, m.dv.CurrentFileIdx, treeW, m.dv.Height, nil, nil)
	mainLines := strings.Split(view, "\n")

	var b strings.Builder
	for i := 0; i < m.dv.Height; i++ {
		tl := ""
		if i < len(treeLines) {
			tl = treeLines[i]
		}
		ml := ""
		if i < len(mainLines) {
			ml = mainLines[i]
		}
		b.WriteString(tl + divider + ml)
		if i < m.dv.Height-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderModal overlays the sidebar content as a centered modal window on top
// of the dimmed background. The modal has a fixed rounded border frame; the
// viewport scrolls inside it.
func (m Model) renderModal(view string) string {
	const pad = 4
	modalW := m.dv.Width - pad*2
	modalH := m.dv.Height - pad*2
	if modalW < 20 {
		modalW = 20
	}
	if modalH < 5 {
		modalH = 5
	}
	contentPad := 2      // padding inside the │ borders
	innerW := modalW - 2 - contentPad*2 // usable content width

	// Build the modal title for the top border.
	var title string
	switch m.sidebarType {
	case sidebarComments:
		title = iconComments + " Comments"
		if len(m.comments) > 0 {
			title += fmt.Sprintf(" (%d)", len(m.comments))
		}
	case sidebarReviews:
		title = iconReview + " Reviews"
	case sidebarChecks:
		title = iconChecks + " Checks"
		if len(m.checkRuns) > 0 {
			title += fmt.Sprintf(" (%d)", len(m.checkRuns))
		}
	}

	titleStr := " " + lipgloss.NewStyle().Bold(true).Render(title) + " "
	titleW := lipgloss.Width(titleStr)
	fillW := modalW - 3 - titleW // ╭─ + title + fill + ╮
	if fillW < 0 {
		fillW = 0
	}
	bc := m.dv.BorderStyle()
	topBorder := bc.Render("╭─") + titleStr + bc.Render(strings.Repeat("─", fillW)+"╮")
	bw := modalW - 2
	if bw < 0 {
		bw = 0
	}
	bottomBorder := bc.Render("╰" + strings.Repeat("─", bw) + "╯")
	sideBorder := bc.Render("│")

	// The viewport content lines (scrolled).
	vpLines := strings.Split(m.sidebarVP.View(), "\n")

	bgLines := strings.Split(view, "\n")

	// 1-cell black bg margin around the modal.
	shadow := "\033[40m" // black bg
	shadowReset := "\033[0m"
	shadowW := modalW + 2 // modal + 1 cell each side
	shadowBlank := shadow + strings.Repeat(" ", shadowW) + shadowReset
	shadowL := shadow + " " + shadowReset
	shadowR := shadow + " " + shadowReset
	spliceOffset := pad - 1 // 1 cell left of modal

	var b strings.Builder
	for i := 0; i < m.dv.Height; i++ {
		bg := ""
		if i < len(bgLines) {
			bg = bgLines[i]
		}

		if i == pad-1 || i == pad+modalH {
			// Shadow row above/below the modal.
			b.WriteString(spliceModal(bg, shadowBlank, spliceOffset, shadowW, m.dv.Width))
		} else if i == pad {
			b.WriteString(spliceModal(bg, shadowL+topBorder+shadowR, spliceOffset, shadowW, m.dv.Width))
		} else if i == pad+modalH-1 {
			b.WriteString(spliceModal(bg, shadowL+bottomBorder+shadowR, spliceOffset, shadowW, m.dv.Width))
		} else if i > pad && i < pad+modalH-1 {
			vpIdx := i - pad - 1
			cl := ""
			if vpIdx >= 0 && vpIdx < len(vpLines) {
				cl = vpLines[vpIdx]
			}
			clW := lipgloss.Width(cl)
			if clW < innerW {
				cl += strings.Repeat(" ", innerW-clW)
			}
			iPad := strings.Repeat(" ", contentPad)
			modalLine := shadowL + sideBorder + iPad + cl + iPad + sideBorder + shadowR
			b.WriteString(spliceModal(bg, modalLine, spliceOffset, shadowW, m.dv.Width))
		} else {
			b.WriteString(bg)
		}
		if i < m.dv.Height-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// spliceModal replaces the middle portion of a background line with modal content,
// preserving the original background on the left and right.
func spliceModal(bg, modal string, leftOffset, modalW, totalW int) string {
	left := xansi.Truncate(bg, leftOffset, "")
	leftW := lipgloss.Width(left)
	if leftW < leftOffset {
		left += strings.Repeat(" ", leftOffset-leftW)
	}

	rightStart := leftOffset + modalW
	bgW := lipgloss.Width(bg)
	right := ""
	if bgW > rightStart {
		right = xansi.Cut(bg, rightStart, bgW)
	}

	return left + "\033[0m" + modal + "\033[0m" + right
}

// --- Overview tab ---

// overviewPad is the left margin for overview content.
const overviewPad = 2

// indent prefixes every line of s with n spaces.
func indent(s string, n int) string {
	prefix := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

// descWidth returns the available width for description/body markdown.
func (m Model) descWidth() int {
	return m.dv.ContentWidth() - overviewPad*2
}

func (m *Model) rebuildContent() {
	m.dv.HelpLine = m.helpLine()
	if m.dv.SearchPattern != nil {
		m.dv.RunSearch()
	}
	m.dv.RebuildContent(m.buildOverviewContent, m.buildFileContent)
}

// helpLine returns the contextual help text for the right-pane footer
// when help mode is enabled.
func (m Model) helpLine() string {
	if !m.ctx.Config.HelpMode {
		return ""
	}
	hint := func(key, desc string) string {
		return styles.StatusBarKey.Render(key) + " " + styles.StatusBarHint.Render(desc)
	}
	sep := "  "

	var parts []string
	if m.dv.Composing {
		parts = append(parts,
			hint("esc", "cancel"),
			hint("enter", "submit local"),
			hint("alt+enter", "submit to GitHub"),
			hint("^g", "$EDITOR"),
		)
		return strings.Join(parts, sep)
	}
	if m.showSidebar {
		parts = append(parts,
			hint("j/k", "scroll"),
			hint("esc", "close"),
		)
		return strings.Join(parts, sep)
	}
	if m.dv.Tree.Focused {
		parts = append(parts,
			hint("j/k", "navigate"),
			hint("enter", "open file"),
			hint("l", "focus diff"),
			hint("^j/k", "next/prev file"),
		)
	} else if m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
		parts = append(parts,
			hint("j/k", "navigate"),
			hint("J/K", "select range"),
			hint("a", "comment"),
			hint("^j/k", "next/prev file"),
			hint("c/r/s", "comments/reviews/checks"),
		)
	} else {
		parts = append(parts,
			hint("j/k", "scroll"),
			hint("c/r/s", "comments/reviews/checks"),
		)
	}
	return strings.Join(parts, sep)
}

func (m Model) buildOverviewContent(w int) string {
	var content strings.Builder

	// Status + metadata line.
	meta := styles.PRStatusBadge(m.pr.State, m.pr.Draft, m.pr.Merged) +
		" " + m.renderMeta()
	content.WriteString("\n" + indent(meta, overviewPad) + "\n")

	// Title.
	prURL := fmt.Sprintf("https://github.com/%s/pull/%d", m.owner + "/" + m.repo, m.pr.Number)
	title := lipgloss.NewStyle().Bold(true).
		UnderlineStyle(lipgloss.UnderlineDotted).
		Hyperlink(prURL).
		Render(fmt.Sprintf("#%d %s", m.pr.Number, m.pr.Title))
	content.WriteString("\n" + indent(title, overviewPad) + "\n")

	// Description body.
	descBody := strings.TrimSpace(m.descContent)
	if descBody == "" {
		descBody = dimStyle.Render("No description provided.")
	}
	content.WriteString("\n" + indent(descBody, overviewPad))
	content.WriteString("\n")

	return content.String()
}

func (m *Model) buildFileContent(w int) string {
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.Files) {
		return ""
	}

	var content strings.Builder
	if m.dv.RenderedFiles[idx] != "" {
		rendered := m.dv.RenderedFiles[idx]
		if m.dv.Composing && m.dv.HasDiffLines() {
			rendered = m.insertCommentBox(rendered, idx)
		}
		content.WriteString(rendered)
	} else {
		// Show spinner while highlighting is in progress.
		content.WriteString(m.dv.SpinnerView())
	}
	// Trailing padding for scrollability.
	content.WriteString("\n" + strings.Repeat("\n", m.dv.Height/2))

	return content.String()
}

// --- Code tab ---

func (m *Model) startFileRendering() tea.Cmd {
	if len(m.dv.Files) == 0 || m.dv.FilesHighlighted > 0 {
		return nil
	}
	m.dv.FilesLoading = true
	m.dv.SpinnerActive = true
	return tea.Batch(m.highlightFileCmd(0), m.dv.Spinner.Tick)
}

// highlightFileCmd returns a tea.Cmd that fetches file content and runs
// syntax highlighting (expensive). Width-dependent formatting happens later.
func (m Model) highlightFileCmd(index int) tea.Cmd {
	f := m.dv.Files[index]
	headRef := m.pr.Head.SHA
	baseRef := m.pr.Base.SHA
	prNumber := m.pr.Number
	client := m.ctx.Client
	chromaStyle := m.ctx.DiffColors.ChromaStyle
	owner := m.owner
	repo := m.repo

	return func() tea.Msg {
		var fileContent, oldFileContent string

		// Get new file content (PR head) for added/modified files.
		if f.Status != "removed" && f.Patch != "" {
			if content, err := client.FetchFileContent(owner, repo, f.Filename, headRef); err == nil {
				fileContent = content
			}
		}

		// Get old file content (PR base) for deleted/modified files.
		if f.Status != "added" && f.Patch != "" {
			if content, err := client.FetchFileContent(owner, repo, f.Filename, baseRef); err == nil {
				oldFileContent = content
			}
		}

		hl := components.HighlightDiffFile(f, fileContent, oldFileContent, chromaStyle)
		return fileHighlightedMsg{highlight: hl, index: index, prNumber: prNumber}
	}
}

// formatFile runs the cheap width-dependent formatting on a cached highlight.
// prCommentSource implements diffviewer.CommentSource by merging GitHub
// review comments with local comments.
type prCommentSource struct {
	reviewComments []github.ReviewComment
	localComments  *comments.CommentStore
}

func (s prCommentSource) CommentsForFile(filename string) []github.ReviewComment {
	var result []github.ReviewComment
	for _, c := range s.reviewComments {
		if c.Path == filename {
			result = append(result, c)
		}
	}
	if s.localComments != nil {
		result = append(result, s.localComments.ForFile(filename)...)
	}
	return result
}

// BlocksForFile implements diffviewer.BlockSource — preserves content blocks
// from local comments with tool calls.
func (s prCommentSource) BlocksForFile(filename string) map[int][]comments.ContentBlock {
	if s.localComments == nil {
		return nil
	}
	locals := s.localComments.ForFileLocal(filename)
	if len(locals) == 0 {
		return nil
	}
	lookup := make(map[int][]comments.ContentBlock)
	for _, lc := range locals {
		blocks := comments.NormalizedBlocks(lc.Blocks, lc.Body)
		if len(blocks) > 0 {
			lookup[comments.IDToInt(lc.ID)] = blocks
		}
	}
	return lookup
}

// syncCommentSource updates the DiffViewer's CommentSource with current data.
// Must be called after reviewComments or localComments change.
func (m *Model) syncCommentSource() {
	m.dv.Comments = prCommentSource{
		reviewComments: m.reviewComments,
		localComments:  m.localComments,
	}
}

func (m *Model) formatFile(index int) {
	m.syncCommentSource()
	m.dv.FormatFile(index)
}

func (m *Model) reformatAllFiles() {
	m.syncCommentSource()
	m.dv.ReformatAllFiles()
}

// commentsForFile returns base comments for a file (no pending copilot).
func (m Model) commentsForFile(filename string) []github.ReviewComment {
	src := prCommentSource{
		reviewComments: m.reviewComments,
		localComments:  m.localComments,
	}
	return src.CommentsForFile(filename)
}

// prefetchFiles kicks off background fetches for the first n files' content,
// warming the cache so Code tab renders are fast.
func (m Model) prefetchFiles(n int) []tea.Cmd {
	limit := n
	if limit > len(m.dv.Files) {
		limit = len(m.dv.Files)
	}
	if limit == 0 {
		return nil
	}

	ref := m.pr.Head.SHA
	client := m.ctx.Client
	var cmds []tea.Cmd
	for i := 0; i < limit; i++ {
		f := m.dv.Files[i]
		if f.Status == "removed" || f.Patch == "" {
			continue
		}
		filename := f.Filename
		cmds = append(cmds, func() tea.Msg {
			client.FetchFileContent(m.owner, m.repo, filename, ref)
			return prefetchDoneMsg{}
		})
	}
	return cmds
}


// --- Meta / User ---

func (m Model) renderMeta() string {
	pr := m.pr
	author := coloredAuthor(pr.User.Login)

	if pr.Merged && pr.MergedBy != nil {
		if pr.MergedBy.Login == pr.User.Login {
			return dimStyle.Render(relativeTime(*pr.MergedAt)+" by ") + author
		}
		merger := coloredAuthor(pr.MergedBy.Login)
		return dimStyle.Render(relativeTime(*pr.MergedAt)+" by ") + merger
	}

	if pr.State == "closed" && pr.ClosedAt != nil {
		return dimStyle.Render(relativeTime(*pr.ClosedAt)+" by ") + author
	}

	return dimStyle.Render(relativeTime(pr.CreatedAt)+" by ") + author
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

