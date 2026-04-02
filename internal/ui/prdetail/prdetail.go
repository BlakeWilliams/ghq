package prdetail

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/components"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	"github.com/blakewilliams/ghq/internal/ui/uictx"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
)

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
	pr     github.PullRequest
	ctx    *uictx.Context
	width  int
	height int

	// Right panel viewport (shows overview or file diff)
	vp      viewport.Model
	vpReady bool

	// Content data
	descContent    string
	reviews        []github.Review
	comments       []github.IssueComment
	reviewComments []github.ReviewComment
	checkRuns      []github.CheckRun

	// Files
	files            []github.PullRequestFile
	highlightedFiles []components.HighlightedDiff
	renderedFiles    []string
	filesHighlighted int
	filesLoading     bool
	currentFileIdx   int // -1 = Overview selected

	// File tree (always visible)
	treeEntries []components.FileTreeEntry
	treeCursor  int  // 0 = Overview, 1+ = file entries (offset by 1)
	treeWidth   int
	treeFocused bool // when true, j/k move tree cursor; yellow border

	// Modal (comments/reviews/checks)
	showSidebar bool
	sidebarType sidebarType
	sidebarVP   viewport.Model

	// Line cursor (within current file diff)
	diffCursor      int
	selectionAnchor int // -1 = no selection; otherwise the diff line index where shift-select started
	fileDiffs       [][]components.DiffLine
	fileDiffOffsets [][]int

	// Comment composing
	composing        bool
	commentInput     textarea.Model
	commentFile      string
	commentLine      int
	commentSide      string
	commentStartLine int    // >0 for multi-line comments (the first line of the range)
	commentStartSide string // side of the first line for multi-line comments
	replyToID        *int

	// Shared
	filesListLoaded bool
	waitingG        bool
}

func New(pr github.PullRequest, ctx *uictx.Context, width, height int) Model {
	return Model{
		pr:              pr,
		ctx:             ctx,
		width:           width,
		height:          height,
		currentFileIdx:  -1, // start on Overview
		selectionAnchor: -1, // no selection
		treeWidth:       35,
		treeFocused:     true, // start with tree focused
	}
}

func (m Model) PRNumber() int {
	return m.pr.Number
}

func (m Model) PRTitle() string {
	return m.pr.Title
}

func (m *Model) activeViewport() *viewport.Model {
	if m.showSidebar {
		return &m.sidebarVP
	}
	return &m.vp
}

// StatusHints returns left and right hint groups for the status bar.
// Entries are pre-rendered.
func (m Model) StatusHints() (left, right []string) {
	if m.treeFocused {
		left = append(left, styles.StatusBarKey.Render("f")+" "+styles.StatusBarHint.Render("unfocus tree"))
	} else {
		left = append(left, styles.StatusBarKey.Render("f")+" "+styles.StatusBarHint.Render("focus tree"))
	}
	left = append(left, styles.StatusBarKey.Render("h/l")+" "+styles.StatusBarHint.Render("panes"))
	left = append(left, styles.StatusBarKey.Render("p/n")+" "+styles.StatusBarHint.Render("files"))
	if !m.treeFocused && m.currentFileIdx >= 0 {
		left = append(left, styles.StatusBarKey.Render("J/K")+" "+styles.StatusBarHint.Render("select range"))
	}
	right = append(right, highlightHint("comments", "c"))
	right = append(right, highlightHint("reviews", "r"))
	right = append(right, highlightHint("checks", "s"))
	if len(m.files) > 0 && m.currentFileIdx >= 0 {
		right = append(right, fmt.Sprintf("%s %d/%d", iconFile, m.currentFileIdx+1, len(m.files)))
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
	return tea.Batch(
		func() tea.Msg {
			rendered := renderMarkdown(body, width)
			return descRenderedMsg{content: rendered, prNumber: prNumber}
		},
		m.ctx.Client.GetPullRequestFiles(m.pr.Number),
		m.ctx.Client.GetReviews(m.pr.Number),
		m.ctx.Client.GetIssueComments(m.pr.Number),
		m.ctx.Client.GetReviewComments(m.pr.Number),
		m.ctx.Client.GetCheckRuns(m.pr.Head.SHA),
	)
}

func (m Model) Update(msg tea.Msg) (uictx.View, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.vp.SetWidth(m.width)
		m.vp.SetHeight(m.height)
		body := m.pr.Body
		width := m.descWidth()
		prNumber := m.pr.Number
		cmds := []tea.Cmd{func() tea.Msg {
			rendered := renderMarkdown(body, width)
			return descRenderedMsg{content: rendered, prNumber: prNumber}
		}}
		// Re-format diff files at the new width (cheap, no Chroma re-run).
		if m.filesListLoaded && m.filesHighlighted > 0 {
			m.reformatAllFiles()
			m.rebuildContent()
		}
		return m, tea.Batch(cmds...)

	case tea.MouseClickMsg:
		if msg.X < m.treeWidth {
			if idx, ok := m.treeEntryIndexAtY(msg.Y); ok {
				if idx == 0 {
					// Overview clicked.
					m.treeCursor = 0
					m.currentFileIdx = -1
					m.rebuildContent()
					m.vp.GotoTop()
				} else if idx >= 2 {
					eIdx := idx - 2 // -2 for Overview + separator
					if eIdx >= 0 && eIdx < len(m.treeEntries) {
						e := m.treeEntries[eIdx]
						if !e.IsDir && e.FileIndex >= 0 {
							m.treeCursor = idx
							m.currentFileIdx = e.FileIndex
							m.rebuildContent()
							m.vp.GotoTop()
						}
					}
				}
			}
			return m, nil
		}

	case tea.KeyPressMsg:
		var cmd tea.Cmd
		var handled bool
		m, cmd, handled = m.handleKey(msg)
		if handled {
			return m, cmd
		}

	case descRenderedMsg:
		if msg.prNumber == m.pr.Number {
			m.descContent = msg.content
			m.rebuildContent()
		}
		return m, nil

	case github.ReviewsLoadedMsg:
		if msg.Number == m.pr.Number {
			m.reviews = msg.Reviews
			m.rebuildSidebar()
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
			m.rebuildSidebar()
		}
		return m, nil

	case github.ReviewCommentsLoadedMsg:
		if msg.Number == m.pr.Number {
			m.reviewComments = msg.Comments
			// Re-format files to include comments (cheap, highlights cached).
			if m.filesHighlighted > 0 {
				m.reformatAllFiles()
				m.rebuildContent()
			}
		}
		return m, nil

	case github.CheckRunsLoadedMsg:
		if msg.Ref == m.pr.Head.SHA {
			m.checkRuns = msg.CheckRuns
			m.rebuildSidebar()
		}
		return m, nil

	case github.PRFilesLoadedMsg:
		m.files = msg.Files
		m.highlightedFiles = make([]components.HighlightedDiff, len(msg.Files))
		m.renderedFiles = make([]string, len(msg.Files))
		m.filesListLoaded = true
		// Parse diff lines for each file (for cursor navigation).
		m.fileDiffs = make([][]components.DiffLine, len(msg.Files))
		m.fileDiffOffsets = make([][]int, len(msg.Files))
		for i, f := range msg.Files {
			m.fileDiffs[i] = components.ParsePatchLines(f.Patch)
		}
		m.treeEntries = components.BuildFileTree(m.files)
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

	case fileHighlightedMsg:
		if msg.prNumber != m.pr.Number || msg.index >= len(m.highlightedFiles) {
			return m, nil
		}
		m.highlightedFiles[msg.index] = msg.highlight
		m.filesHighlighted = msg.index + 1
		// Format at current width (cheap).
		m.formatFile(msg.index)
		if m.filesHighlighted >= len(m.files) {
			m.filesLoading = false
		}
		m.rebuildContent()
		if m.filesHighlighted < len(m.files) {
			return m, m.highlightFileCmd(m.filesHighlighted)
		}
		return m, nil

	case github.CommentCreatedMsg:
		if msg.Number == m.pr.Number {
			m.composing = false
			m.reviewComments = append(m.reviewComments, msg.Comment)
			// Re-format to include the new comment (cheap).
			m.reformatAllFiles()
			m.rebuildContent()
		}
		return m, nil

	case github.CommentErrorMsg:
		// TODO: show error to user
		return m, nil

	case github.QueryErrMsg:
		return m, nil

	case editorFinishedMsg:
		if msg.err == nil && msg.content != "" {
			m.commentInput.SetValue(msg.content)
		}
		m.rebuildContent()
		return m, nil
	}

	// When composing a comment, delegate all input to the textarea.
	if m.composing {
		var cmd tea.Cmd
		m.commentInput, cmd = m.commentInput.Update(msg)
		return m, cmd
	}

	if m.vpReady {
		vp := m.activeViewport()
		prevOffset := vp.YOffset()
		var cmd tea.Cmd
		*vp, cmd = vp.Update(msg)
		// If the main viewport scrolled, sync the diff cursor within the current file.
		if vp == &m.vp && m.vp.YOffset() != prevOffset && m.currentFileIdx >= 0 {
			m.syncDiffCursorToViewport()
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
	if m.composing {
		return m.handleCommentKey(msg)
	}

	// Close modal on esc.
	if msg.String() == "esc" && m.showSidebar {
		m.showSidebar = false
		return m, nil, true
	}

	// Clear selection on esc.
	if msg.String() == "esc" && m.selectionAnchor >= 0 {
		m.selectionAnchor = -1
		return m, nil, true
	}

	switch msg.String() {
	case "f":
		// Toggle tree focus.
		m.treeFocused = !m.treeFocused
		return m, nil, true
	case "c":
		m.toggleSidebar(sidebarComments)
		return m, nil, true
	case "r":
		m.toggleSidebar(sidebarReviews)
		return m, nil, true
	case "s":
		m.toggleSidebar(sidebarChecks)
		return m, nil, true
	case "h", "left":
		// Focus tree pane.
		m.treeFocused = true
		return m, nil, true
	case "l", "right":
		// Focus right pane.
		m.treeFocused = false
		return m, nil, true
	case "p":
		m.moveTreeSelection(-1)
		return m, nil, true
	case "n":
		m.moveTreeSelection(1)
		return m, nil, true
	case "j", "down":
		if m.showSidebar {
			return m, nil, false // let viewport scroll
		}
		if m.treeFocused {
			m.moveTreeCursorBy(1)
			return m, nil, true
		}
		// Diff line cursor — clear selection on normal move.
		if m.currentFileIdx >= 0 && m.hasDiffLines() {
			m.selectionAnchor = -1
			m.moveDiffCursor(1)
			return m, nil, true
		}
	case "k", "up":
		if m.showSidebar {
			return m, nil, false
		}
		if m.treeFocused {
			m.moveTreeCursorBy(-1)
			return m, nil, true
		}
		if m.currentFileIdx >= 0 && m.hasDiffLines() {
			m.selectionAnchor = -1
			m.moveDiffCursor(-1)
			return m, nil, true
		}
	case "J", "shift+down":
		// Extend selection downward.
		if !m.showSidebar && !m.treeFocused && m.currentFileIdx >= 0 && m.hasDiffLines() {
			if m.selectionAnchor < 0 {
				m.selectionAnchor = m.diffCursor
			}
			m.moveDiffCursor(1)
			return m, nil, true
		}
	case "K", "shift+up":
		// Extend selection upward.
		if !m.showSidebar && !m.treeFocused && m.currentFileIdx >= 0 && m.hasDiffLines() {
			if m.selectionAnchor < 0 {
				m.selectionAnchor = m.diffCursor
			}
			m.moveDiffCursor(-1)
			return m, nil, true
		}
	case "enter":
		if m.treeFocused {
			// Select current tree entry — switch to right panel.
			m.selectTreeEntry()
			m.treeFocused = false
			return m, nil, true
		}
		// Open comment input on diff line.
		if !m.showSidebar && m.currentFileIdx >= 0 && m.hasDiffLines() {
			return m.openCommentInput()
		}
	case "a":
		if !m.showSidebar && m.currentFileIdx >= 0 && m.hasDiffLines() {
			return m.openCommentInput()
		}
	case "ctrl+d":
		m.selectionAnchor = -1
		if m.treeFocused {
			m.moveTreeCursorBy(m.height / 2)
		} else if m.currentFileIdx >= 0 && m.hasDiffLines() {
			m.moveDiffCursorBy(m.height / 2)
		}
		return m, nil, true
	case "ctrl+u":
		m.selectionAnchor = -1
		if m.treeFocused {
			m.moveTreeCursorBy(-m.height / 2)
		} else if m.currentFileIdx >= 0 && m.hasDiffLines() {
			m.moveDiffCursorBy(-m.height / 2)
		}
		return m, nil, true
	case "ctrl+f":
		m.selectionAnchor = -1
		if m.treeFocused {
			m.moveTreeCursorBy(m.height)
		} else if m.currentFileIdx >= 0 && m.hasDiffLines() {
			m.moveDiffCursorBy(m.height)
		}
		return m, nil, true
	case "ctrl+b":
		m.selectionAnchor = -1
		if m.treeFocused {
			m.moveTreeCursorBy(-m.height)
		} else if m.currentFileIdx >= 0 && m.hasDiffLines() {
			m.moveDiffCursorBy(-m.height)
		}
		return m, nil, true
	case "G":
		m.waitingG = false
		m.activeViewport().GotoBottom()
		return m, nil, true
	case "g":
		if m.waitingG {
			m.waitingG = false
			m.activeViewport().GotoTop()
			return m, nil, true
		}
		m.waitingG = true
		return m, nil, true
	default:
		m.waitingG = false
	}
	return m, nil, false
}

// moveTreeSelection moves the tree cursor by delta, skipping directories
// and the separator, and selects the entry (updating the right panel).
func (m *Model) moveTreeSelection(delta int) {
	totalEntries := 2 + len(m.treeEntries) // Overview + separator + file entries
	newCursor := m.treeCursor + delta

	// Skip separator (index 1) and directory entries.
	for newCursor >= 0 && newCursor < totalEntries {
		if newCursor == 0 {
			break // Overview is always selectable
		}
		if newCursor == 1 {
			newCursor += delta // skip separator
			continue
		}
		eIdx := newCursor - 2
		if eIdx >= 0 && eIdx < len(m.treeEntries) && !m.treeEntries[eIdx].IsDir {
			break
		}
		newCursor += delta
	}

	if newCursor < 0 || newCursor >= totalEntries {
		return
	}
	m.treeCursor = newCursor
	m.selectTreeEntry()
}

// moveTreeCursorBy jumps the tree cursor by delta entries, skipping
// separators and directories, clamped to bounds. Does NOT select.
func (m *Model) moveTreeCursorBy(delta int) {
	totalEntries := 2 + len(m.treeEntries)
	newCursor := m.treeCursor + delta

	if newCursor < 0 {
		newCursor = 0
	}
	if newCursor >= totalEntries {
		newCursor = totalEntries - 1
	}

	dir := 1
	if delta < 0 {
		dir = -1
	}
	for newCursor >= 0 && newCursor < totalEntries {
		if newCursor == 0 {
			break
		}
		if newCursor == 1 {
			newCursor += dir
			continue
		}
		eIdx := newCursor - 2
		if eIdx >= 0 && eIdx < len(m.treeEntries) && !m.treeEntries[eIdx].IsDir {
			break
		}
		newCursor += dir
	}
	if newCursor < 0 || newCursor >= totalEntries {
		return
	}

	m.treeCursor = newCursor
}

// selectTreeEntry updates the right panel based on the current tree cursor.
func (m *Model) selectTreeEntry() {
	m.selectionAnchor = -1
	if m.treeCursor == 0 {
		// Overview.
		m.currentFileIdx = -1
		m.rebuildContent()
		m.vp.GotoTop()
		return
	}
	eIdx := m.treeCursor - 2 // -2 for Overview + separator
	if eIdx >= 0 && eIdx < len(m.treeEntries) {
		e := m.treeEntries[eIdx]
		if !e.IsDir && e.FileIndex >= 0 {
			m.currentFileIdx = e.FileIndex
			m.diffCursor = m.firstNonHunkLine(e.FileIndex)
			m.rebuildContent()
			m.vp.GotoTop()
		}
	}
}

func (m Model) hasDiffLines() bool {
	idx := m.currentFileIdx
	return idx >= 0 && idx < len(m.fileDiffs) && len(m.fileDiffs[idx]) > 0
}

func (m *Model) moveDiffCursor(delta int) {
	lines := m.fileDiffs[m.currentFileIdx]
	newPos := m.diffCursor + delta

	// Skip hunk lines.
	for newPos >= 0 && newPos < len(lines) && lines[newPos].Type == components.LineHunk {
		newPos += delta
	}

	if newPos < 0 || newPos >= len(lines) {
		// Don't cross file boundaries — stay at current position.
		return
	} else {
		m.diffCursor = newPos
	}
	// No rebuildContent — cursor highlight is applied at View() time.
	m.scrollToDiffCursor()
}

// moveDiffCursorBy jumps the diff cursor by delta lines, skipping hunks,
// clamped to the current file. Also scrolls the viewport.
func (m *Model) moveDiffCursorBy(delta int) {
	lines := m.fileDiffs[m.currentFileIdx]
	newPos := m.diffCursor + delta

	// Clamp to file bounds.
	if newPos < 0 {
		newPos = 0
	}
	if newPos >= len(lines) {
		newPos = len(lines) - 1
	}

	// If we landed on a hunk, search forward to find the nearest non-hunk.
	// (Searching in the movement direction may go out of bounds at file edges.)
	if newPos >= 0 && newPos < len(lines) && lines[newPos].Type == components.LineHunk {
		// Try preferred direction first, then opposite.
		dir := 1
		if delta < 0 {
			dir = -1
		}
		found := false
		for p := newPos + dir; p >= 0 && p < len(lines); p += dir {
			if lines[p].Type != components.LineHunk {
				newPos = p
				found = true
				break
			}
		}
		if !found {
			// Try opposite direction.
			for p := newPos - dir; p >= 0 && p < len(lines); p -= dir {
				if lines[p].Type != components.LineHunk {
					newPos = p
					found = true
					break
				}
			}
		}
		if !found {
			return
		}
	}

	m.diffCursor = newPos
	m.scrollToDiffCursor()
}

func (m Model) firstNonHunkLine(fileIdx int) int {
	for i, dl := range m.fileDiffs[fileIdx] {
		if dl.Type != components.LineHunk {
			return i
		}
	}
	return 0
}

func (m Model) lastNonHunkLine(fileIdx int) int {
	lines := m.fileDiffs[fileIdx]
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].Type != components.LineHunk {
			return i
		}
	}
	return len(lines) - 1
}

const scrollMargin = 5

func (m *Model) scrollToDiffCursor() {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileDiffOffsets) {
		return
	}
	if m.diffCursor >= len(m.fileDiffOffsets[idx]) {
		return
	}
	absLine := m.fileDiffOffsets[idx][m.diffCursor]
	top := m.vp.YOffset()
	bottom := top + m.height - 1

	if absLine < top+scrollMargin {
		// Cursor near top edge — scroll up.
		target := absLine - scrollMargin
		if target < 0 {
			target = 0
		}
		m.vp.SetYOffset(target)
	} else if absLine > bottom-scrollMargin {
		// Cursor near bottom edge — scroll down.
		m.vp.SetYOffset(absLine - m.height + scrollMargin + 1)
	}
	// Otherwise cursor is comfortably visible — don't scroll.
}

// editorFinishedMsg is sent when $EDITOR exits.
type editorFinishedMsg struct {
	content string
	err     error
}

func (m Model) handleCommentKey(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		m.composing = false
		m.selectionAnchor = -1
		m.rebuildContent()
		return m, nil, true
	case "alt+enter":
		// Submit comment.
		body := strings.TrimSpace(m.commentInput.Value())
		if body == "" {
			m.composing = false
			m.selectionAnchor = -1
			m.rebuildContent()
			return m, nil, true
		}
		m.composing = false
		var cmd tea.Cmd
		if m.replyToID != nil {
			cmd = m.ctx.Client.ReplyToReviewComment(m.pr.Number, *m.replyToID, body)
		} else {
			cmd = m.ctx.Client.CreateReviewComment(
				m.pr.Number, body, m.pr.Head.SHA,
				m.commentFile, m.commentLine, m.commentSide,
				m.commentStartLine, m.commentStartSide,
			)
		}
		m.selectionAnchor = -1
		m.rebuildContent()
		return m, cmd, true
	case "alt+s":
		// Insert a suggestion block with the selected code.
		suggestion := m.buildSuggestionBlock()
		if suggestion != "" {
			cur := m.commentInput.Value()
			if cur != "" && !strings.HasSuffix(cur, "\n") {
				cur += "\n"
			}
			m.commentInput.SetValue(cur + suggestion)
			m.rebuildContent()
		}
		return m, nil, true
	case "ctrl+g":
		return m.openEditorForComment()
	}
	// Delegate to textarea.
	var cmd tea.Cmd
	m.commentInput, cmd = m.commentInput.Update(msg)
	m.rebuildContent()
	return m, cmd, true
}

// buildSuggestionBlock returns a GitHub suggestion fenced code block
// pre-filled with the code from the commented line(s). Returns "" if the
// selection contains deleted lines, since suggestions only apply to the
// new side of the diff.
func (m Model) buildSuggestionBlock() string {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileDiffs) {
		return ""
	}
	lines := m.fileDiffs[idx]

	selStart, selEnd := m.diffCursor, m.diffCursor
	if m.selectionAnchor >= 0 && m.selectionAnchor != m.diffCursor {
		selStart, selEnd = m.selectionAnchor, m.diffCursor
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
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileDiffs) {
		return false
	}
	lines := m.fileDiffs[idx]

	selStart, selEnd := m.diffCursor, m.diffCursor
	if m.selectionAnchor >= 0 && m.selectionAnchor != m.diffCursor {
		selStart, selEnd = m.selectionAnchor, m.diffCursor
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
	if v := m.commentInput.Value(); v != "" {
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
	idx := m.currentFileIdx
	if idx >= len(m.fileDiffs) {
		return m, nil, false
	}
	lines := m.fileDiffs[idx]
	if m.diffCursor >= len(lines) {
		return m, nil, false
	}
	dl := lines[m.diffCursor]

	// Skip hunk headers — can't comment on those.
	if dl.Type == components.LineHunk {
		return m, nil, false
	}

	m.commentFile = m.files[idx].Filename

	// Determine if we have a multi-line selection.
	m.commentStartLine = 0
	m.commentStartSide = ""

	if m.selectionAnchor >= 0 && m.selectionAnchor != m.diffCursor {
		selStart, selEnd := m.selectionAnchor, m.diffCursor
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
			m.commentLine = endDL.OldLineNo
			m.commentSide = "LEFT"
		} else {
			m.commentLine = endDL.NewLineNo
			m.commentSide = "RIGHT"
		}

		if startDL.Type == components.LineDel {
			m.commentStartLine = startDL.OldLineNo
			m.commentStartSide = "LEFT"
		} else {
			m.commentStartLine = startDL.NewLineNo
			m.commentStartSide = "RIGHT"
		}
	} else {
		// Single-line comment.
		if dl.Type == components.LineDel {
			m.commentLine = dl.OldLineNo
			m.commentSide = "LEFT"
		} else {
			m.commentLine = dl.NewLineNo
			m.commentSide = "RIGHT"
		}
	}

	// Check if there's an existing comment thread on this line to reply to.
	// Only for single-line comments — multi-line always creates a new thread.
	if m.commentStartLine > 0 {
		m.replyToID = nil
	} else {
		m.replyToID = m.findThreadRootOnLine(m.commentFile, m.commentLine)
	}

	// Create textarea.
	ta := textarea.New()
	ta.SetWidth(m.contentWidth() - 10 - 6) // gutter + border + padding
	ta.SetHeight(5)
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Focus()
	if m.replyToID != nil {
		ta.Placeholder = "Reply to thread..."
	} else {
		ta.Placeholder = "Add a comment..."
	}
	m.commentInput = ta
	m.composing = true
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
			return &c.ID
		}
	}
	return nil
}

// insertCommentBox inserts the comment input textarea into the rendered file
// content at the cursor position. Only called when composing.
func (m Model) insertCommentBox(rendered string, fileIdx int) string {
	lines := strings.Split(rendered, "\n")
	cursorRenderedLine := -1
	if fileIdx < len(m.fileDiffOffsets) && m.diffCursor < len(m.fileDiffOffsets[fileIdx]) {
		cursorRenderedLine = m.fileDiffOffsets[fileIdx][m.diffCursor]
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

// applyCursorHighlight applies the cursor highlight to a single visible line.
// Called at View() time on only the one line that needs it.
func (m Model) applyCursorHighlight(line string) string {
	fileIdx := m.currentFileIdx
	if fileIdx >= len(m.fileDiffs) || m.diffCursor >= len(m.fileDiffs[fileIdx]) {
		return line
	}
	dl := m.fileDiffs[fileIdx][m.diffCursor]
	if dl.Type == components.LineHunk {
		return line
	}

	prefix, inner, suffix := splitDiffBorders(line)

	// Replace the bold +/- gutter marker with >.
	inner = strings.Replace(inner, "\033[1m+\033[0m", "\033[1m>\033[0m", 1)
	inner = strings.Replace(inner, "\033[1m-\033[0m", "\033[1m>\033[0m", 1)

	// Pick the right selection bg based on line type.
	colors := m.ctx.DiffColors
	var selBg string
	switch dl.Type {
	case components.LineAdd:
		selBg = colors.SelectedAddBg
	case components.LineDel:
		selBg = colors.SelectedDelBg
	default:
		selBg = colors.SelectedCtxBg
	}

	if selBg != "" {
		if colors.AddBg != "" {
			inner = strings.ReplaceAll(inner, colors.AddBg, selBg)
		}
		if colors.DelBg != "" {
			inner = strings.ReplaceAll(inner, colors.DelBg, selBg)
		}
		inner = strings.ReplaceAll(inner, "\033[0m", "\033[0m"+selBg)
		inner = strings.ReplaceAll(inner, "\033[m", "\033[m"+selBg)
		inner = selBg + inner + "\033[0m"
	}

	return prefix + inner + suffix
}

// renderCommentBox renders the inline comment textarea with a rounded border
// and hint line below, indented past the diff gutter.
func (m Model) renderCommentBox() string {
	const gutter = 10 // commentGutterWidth: padNum(4)*2 + 2
	indent := strings.Repeat(" ", gutter)
	boxW := m.contentWidth() - gutter - 2

	taView := m.commentInput.View()

	// Draw top/bottom borders manually.
	bc := m.borderStyle()
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

	leftHint := "esc cancel · ctrl+g $EDITOR"
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
func (m *Model) syncDiffCursorToViewport() {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileDiffOffsets) || len(m.fileDiffOffsets[idx]) == 0 {
		return
	}
	center := m.vp.YOffset() + m.height/2
	offsets := m.fileDiffOffsets[idx]
	diffs := m.fileDiffs[idx]
	best := -1
	bestDist := 0
	for i := 0; i < len(offsets); i++ {
		if i < len(diffs) && diffs[i].Type == components.LineHunk {
			continue
		}
		dist := offsets[i] - center
		if dist < 0 {
			dist = -dist
		}
		if best == -1 || dist < bestDist {
			best = i
			bestDist = dist
		}
	}
	if best >= 0 {
		m.diffCursor = best
	}
}

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

func (m Model) View() string {
	if !m.vpReady {
		return ""
	}

	// Right panel content with cursor overlay.
	rightView := m.vp.View()
	if m.currentFileIdx >= 0 {
		rightView = m.overlayDiffCursor(rightView)
	}

	// Compose: tree | divider | right panel.
	view := m.renderLayout(rightView)

	// Modal overlay on top.
	if m.showSidebar {
		view = m.renderModal(view)
	}
	return view
}

// renderLayout renders the tree + divider + right panel.
func (m Model) renderLayout(rightView string) string {
	treeW := m.treeWidth
	dividerChar := "│"
	var dividerStyle lipgloss.Style
	if m.treeFocused {
		dividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	} else {
		dividerStyle = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	}
	divider := dividerStyle.Render(dividerChar)

	treeLines := components.RenderFileTree(m.treeEntries, m.files, m.treeCursor, m.currentFileIdx, treeW, m.height)
	rightLines := strings.Split(rightView, "\n")

	var b strings.Builder
	for i := 0; i < m.height; i++ {
		tl := ""
		if i < len(treeLines) {
			tl = treeLines[i]
		}
		rl := ""
		if i < len(rightLines) {
			rl = rightLines[i]
		}
		b.WriteString(tl + divider + rl)
		if i < m.height-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// cursorViewportLine returns the line index within the visible viewport
// that corresponds to the current diff cursor, or -1 if not visible.
func (m Model) cursorViewportLine() int {
	fileIdx := m.currentFileIdx
	if fileIdx < 0 || fileIdx >= len(m.fileDiffOffsets) {
		return -1
	}
	if m.diffCursor >= len(m.fileDiffOffsets[fileIdx]) {
		return -1
	}
	absLine := m.fileDiffOffsets[fileIdx][m.diffCursor]
	rel := absLine - m.vp.YOffset()
	if rel < 0 || rel >= m.height {
		return -1
	}
	return rel
}

// overlayDiffCursor applies the cursor highlight to the one visible line,
// or highlights all lines in the selection range when shift-selecting.
func (m Model) overlayDiffCursor(view string) string {
	if !m.filesListLoaded || !m.hasDiffLines() {
		return view
	}

	// When there's a multi-line selection, highlight all lines in the range.
	if m.selectionAnchor >= 0 && m.selectionAnchor != m.diffCursor {
		return m.overlaySelectionRange(view)
	}

	vLine := m.cursorViewportLine()
	if vLine < 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	if vLine < len(lines) {
		lines[vLine] = m.applyCursorHighlight(lines[vLine])
	}
	return strings.Join(lines, "\n")
}

// overlaySelectionRange highlights all diff lines in the selection range
// that are visible in the viewport.
func (m Model) overlaySelectionRange(view string) string {
	fileIdx := m.currentFileIdx
	if fileIdx < 0 || fileIdx >= len(m.fileDiffOffsets) {
		return view
	}

	selStart, selEnd := m.selectionAnchor, m.diffCursor
	if selStart > selEnd {
		selStart, selEnd = selEnd, selStart
	}

	offsets := m.fileDiffOffsets[fileIdx]
	diffs := m.fileDiffs[fileIdx]
	vpTop := m.vp.YOffset()

	lines := strings.Split(view, "\n")

	for i := selStart; i <= selEnd; i++ {
		if i >= len(offsets) || i >= len(diffs) {
			continue
		}
		if diffs[i].Type == components.LineHunk {
			continue
		}
		absLine := offsets[i]
		rel := absLine - vpTop
		if rel < 0 || rel >= len(lines) {
			continue
		}
		lines[rel] = m.applySelectionHighlight(lines[rel], diffs[i])
	}

	return strings.Join(lines, "\n")
}

// applySelectionHighlight applies the selected background to a line in the
// selection range. Similar to applyCursorHighlight but takes an explicit DiffLine.
func (m Model) applySelectionHighlight(line string, dl components.DiffLine) string {
	if dl.Type == components.LineHunk {
		return line
	}

	prefix, inner, suffix := splitDiffBorders(line)

	// Replace the bold +/- gutter marker with > to indicate selection.
	inner = strings.Replace(inner, "\033[1m+\033[0m", "\033[1m>\033[0m", 1)
	inner = strings.Replace(inner, "\033[1m-\033[0m", "\033[1m>\033[0m", 1)

	colors := m.ctx.DiffColors
	var selBg string
	switch dl.Type {
	case components.LineAdd:
		selBg = colors.SelectedAddBg
	case components.LineDel:
		selBg = colors.SelectedDelBg
	default:
		selBg = colors.SelectedCtxBg
	}

	if selBg != "" {
		if colors.AddBg != "" {
			inner = strings.ReplaceAll(inner, colors.AddBg, selBg)
		}
		if colors.DelBg != "" {
			inner = strings.ReplaceAll(inner, colors.DelBg, selBg)
		}
		inner = strings.ReplaceAll(inner, "\033[0m", "\033[0m"+selBg)
		inner = strings.ReplaceAll(inner, "\033[m", "\033[m"+selBg)
		inner = selBg + inner + "\033[0m"
	}

	return prefix + inner + suffix
}

// splitDiffBorders splits a rendered diff line of the form
// border + inner + border into its three parts. The border is a styled "│"
// character whose ANSI byte length varies by terminal color profile, so we
// locate the "│" characters instead of assuming a fixed byte offset.
func splitDiffBorders(line string) (prefix, inner, suffix string) {
	const borderChar = "│"

	firstIdx := strings.Index(line, borderChar)
	if firstIdx < 0 {
		return "", line, ""
	}

	lastIdx := strings.LastIndex(line, borderChar)
	if lastIdx == firstIdx {
		return "", line, ""
	}

	// Prefix ends after the first │ and any trailing ANSI reset sequence.
	prefixEnd := firstIdx + len(borderChar)
	if prefixEnd < len(line) && line[prefixEnd] == '\033' {
		if i := strings.IndexByte(line[prefixEnd:], 'm'); i >= 0 {
			prefixEnd += i + 1
		}
	}

	// Suffix starts at the ESC introducing the last │'s foreground sequence.
	suffixStart := lastIdx
	for i := lastIdx - 1; i >= prefixEnd; i-- {
		if line[i] == '\033' {
			suffixStart = i
			break
		}
	}

	return line[:prefixEnd], line[prefixEnd:suffixStart], line[suffixStart:]
}

func (m Model) renderWithLeftSidebarFrom(view string) string {
	treeW := m.treeWidth
	divider := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render("│")

	treeLines := components.RenderFileTree(m.treeEntries, m.files, m.treeCursor, m.currentFileIdx, treeW, m.height)
	mainLines := strings.Split(view, "\n")

	var b strings.Builder
	for i := 0; i < m.height; i++ {
		tl := ""
		if i < len(treeLines) {
			tl = treeLines[i]
		}
		ml := ""
		if i < len(mainLines) {
			ml = mainLines[i]
		}
		b.WriteString(tl + divider + ml)
		if i < m.height-1 {
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
	modalW := m.width - pad*2
	modalH := m.height - pad*2
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
	bc := m.borderStyle()
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
	for i := 0; i < m.height; i++ {
		bg := ""
		if i < len(bgLines) {
			bg = bgLines[i]
		}

		if i == pad-1 || i == pad+modalH {
			// Shadow row above/below the modal.
			b.WriteString(spliceModal(bg, shadowBlank, spliceOffset, shadowW, m.width))
		} else if i == pad {
			b.WriteString(spliceModal(bg, shadowL+topBorder+shadowR, spliceOffset, shadowW, m.width))
		} else if i == pad+modalH-1 {
			b.WriteString(spliceModal(bg, shadowL+bottomBorder+shadowR, spliceOffset, shadowW, m.width))
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
			b.WriteString(spliceModal(bg, modalLine, spliceOffset, shadowW, m.width))
		} else {
			b.WriteString(bg)
		}
		if i < m.height-1 {
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
	return m.contentWidth() - overviewPad*2
}

func (m *Model) rebuildContent() {
	w := m.rightPanelWidth()

	if !m.vpReady {
		m.vp = viewport.New()
		m.vpReady = true
	}
	m.vp.SetWidth(w)
	m.vp.SetHeight(m.height)

	if m.currentFileIdx == -1 {
		// Overview panel.
		m.vp.SetContent(m.buildOverviewContent(w))
	} else {
		// Single file diff panel.
		m.vp.SetContent(m.buildFileContent(w))
	}
}

func (m Model) buildOverviewContent(w int) string {
	var content strings.Builder

	// Status + metadata line.
	meta := styles.PRStatusBadge(m.pr.State, m.pr.Draft, m.pr.Merged) +
		" " + m.renderMeta()
	content.WriteString("\n" + indent(meta, overviewPad) + "\n")

	// Title.
	prURL := fmt.Sprintf("https://github.com/%s/pull/%d", m.ctx.Client.RepoFullName(), m.pr.Number)
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
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.files) {
		return ""
	}

	var content strings.Builder
	if m.renderedFiles[idx] != "" {
		rendered := m.renderedFiles[idx]
		if m.composing && m.hasDiffLines() {
			rendered = m.insertCommentBox(rendered, idx)
		}
		content.WriteString(rendered)
	} else {
		content.WriteString(dimStyle.Render("  " + iconLoading + " Loading " + m.files[idx].Filename + "..."))
	}
	// Trailing padding for scrollability.
	content.WriteString("\n" + strings.Repeat("\n", m.height/2))

	return content.String()
}

func (m Model) rightPanelWidth() int {
	return m.width - m.treeWidth - 1 // -1 for divider
}

func (m Model) contentWidth() int {
	return m.rightPanelWidth()
}

// --- Code tab ---

func (m *Model) startFileRendering() tea.Cmd {
	if len(m.files) == 0 || m.filesHighlighted > 0 {
		return nil
	}
	m.filesLoading = true
	return m.highlightFileCmd(0)
}

// highlightFileCmd returns a tea.Cmd that fetches file content and runs
// syntax highlighting (expensive). Width-dependent formatting happens later.
func (m Model) highlightFileCmd(index int) tea.Cmd {
	f := m.files[index]
	ref := m.pr.Head.SHA
	prNumber := m.pr.Number
	client := m.ctx.Client
	chromaStyle := m.ctx.DiffColors.ChromaStyle

	return func() tea.Msg {
		var fileContent string
		if f.Status != "removed" && f.Patch != "" {
			if content, err := client.FetchFileContent(f.Filename, ref); err == nil {
				fileContent = content
			}
		}
		hl := components.HighlightDiffFile(f, fileContent, chromaStyle)
		return fileHighlightedMsg{highlight: hl, index: index, prNumber: prNumber}
	}
}

// formatFile runs the cheap width-dependent formatting on a cached highlight.
func (m Model) formatFile(index int) {
	if index >= len(m.highlightedFiles) {
		return
	}
	hl := m.highlightedFiles[index]
	width := m.contentWidth()
	colors := m.ctx.DiffColors
	fileComments := m.commentsForFile(m.files[index].Filename)
	result := components.FormatDiffFile(hl, width, colors, fileComments)
	m.renderedFiles[index] = result.Content
	if index < len(m.fileDiffOffsets) {
		m.fileDiffOffsets[index] = result.DiffLineOffsets
	}
}

// reformatAllFiles re-formats all highlighted files at the current width.
// This is cheap (no Chroma) and used on resize.
func (m *Model) reformatAllFiles() {
	for i := 0; i < m.filesHighlighted; i++ {
		m.formatFile(i)
	}
}

// commentsForFile returns review comments that belong to the given file.
func (m Model) commentsForFile(filename string) []github.ReviewComment {
	var result []github.ReviewComment
	for _, c := range m.reviewComments {
		if c.Path == filename {
			result = append(result, c)
		}
	}
	return result
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
	client := m.ctx.Client
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

var authorBadge = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Black).
	Background(lipgloss.Yellow)

func (m Model) borderStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(m.ctx.DiffColors.BorderColor)
}

func (m Model) roundedBorderStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.ctx.DiffColors.BorderColor).
		Padding(0, 1)
}

// coloredAuthor is a convenience alias for the shared component.
var coloredAuthor = components.ColoredAuthor

// --- Reviews / Comments ---

func (m Model) hasReviewContent() bool {
	return len(m.reviews) > 0 || len(m.pr.RequestedReviewers) > 0
}

var (
	reviewApproved  = lipgloss.NewStyle().Foreground(lipgloss.Green).Bold(true)
	reviewChanges   = lipgloss.NewStyle().Foreground(lipgloss.Red).Bold(true)
	reviewCommented = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	reviewPending   = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
)

func reviewStateIcon(state string) string {
	switch state {
	case "APPROVED":
		return reviewApproved.Render(iconCheckCircle + " approved")
	case "CHANGES_REQUESTED":
		return reviewChanges.Render(iconXCircle + " changes requested")
	case "COMMENTED":
		return reviewCommented.Render(iconComment + " commented")
	case "DISMISSED":
		return reviewCommented.Render(iconSlash + " dismissed")
	default:
		return reviewPending.Render(iconClock + " pending")
	}
}

// rebuildSidebar rebuilds the right sidebar viewport content.
func (m *Model) rebuildSidebar() {
	if !m.showSidebar {
		return
	}
	const pad = 4
	modalW := m.width - pad*2
	modalH := m.height - pad*2
	if modalW < 20 {
		modalW = 20
	}
	if modalH < 5 {
		modalH = 5
	}
	contentPad := 2
	innerW := modalW - 2 - contentPad*2 // inside borders + padding
	contentH := modalH - 2              // inside top/bottom borders

	var lines []string
	switch m.sidebarType {
	case sidebarComments:
		lines = m.buildCommentLines(innerW)
		if len(lines) == 0 {
			lines = []string{dimStyle.Render("No comments yet.")}
		}
	case sidebarReviews:
		lines = m.buildReviewLines(innerW)
		if len(lines) == 0 {
			lines = []string{dimStyle.Render("No reviews yet.")}
		}
	case sidebarChecks:
		lines = m.buildCheckLines()
		if len(lines) == 0 {
			lines = []string{dimStyle.Render("No checks yet.")}
		}
	}

	sep := m.borderStyle().Render(strings.Repeat("─", innerW))
	content := strings.Join(lines, "\n"+sep+"\n")

	m.sidebarVP = viewport.New()
	m.sidebarVP.SetWidth(innerW)
	m.sidebarVP.SetHeight(contentH)
	m.sidebarVP.SetContent(content)
}

// buildReviewLines builds the content lines for the reviews section.
func (m Model) buildReviewLines(innerW int) []string {
	// Deduplicate reviews — keep only the latest per user.
	latestByUser := make(map[string]github.Review)
	for _, r := range m.reviews {
		if r.State == "PENDING" {
			continue
		}
		existing, ok := latestByUser[r.User.Login]
		if !ok || r.SubmittedAt.After(existing.SubmittedAt) {
			latestByUser[r.User.Login] = r
		}
	}

	var lines []string
	for _, r := range m.reviews {
		latest, ok := latestByUser[r.User.Login]
		if !ok || latest.ID != r.ID {
			continue
		}
		delete(latestByUser, r.User.Login)

		author := coloredAuthor(r.User.Login)
		line := author + " " + reviewStateIcon(r.State)
		if r.Body != "" {
			body := renderMarkdown(r.Body, innerW)
			line += "\n" + body
		}
		lines = append(lines, line)
	}

	// Requested reviewers (haven't reviewed yet).
	for _, u := range m.pr.RequestedReviewers {
		if _, reviewed := latestByUser[u.Login]; reviewed {
			continue
		}
		alreadyRendered := false
		for _, r := range m.reviews {
			if r.User.Login == u.Login {
				alreadyRendered = true
				break
			}
		}
		if alreadyRendered {
			continue
		}
		author := coloredAuthor(u.Login)
		lines = append(lines, author+" "+reviewPending.Render(iconClock+" awaiting review"))
	}

	return lines
}

// buildCommentLines builds the content lines for the comments section.
func (m Model) buildCommentLines(innerW int) []string {
	var lines []string
	for _, c := range m.comments {
		author := coloredAuthor(c.User.Login)
		if c.User.Login == m.pr.User.Login {
			author += " " + authorBadge.Render(" "+iconAuthor+" Author ")
		}
		age := dimStyle.Render(relativeTime(c.CreatedAt))

		line := author + " " + age
		if c.Body != "" {
			body := renderMarkdown(c.Body, innerW)
			line += "\n" + body
		}
		lines = append(lines, line)
	}
	return lines
}

// buildCheckLines builds the content lines for the checks section.
func (m Model) buildCheckLines() []string {
	var lines []string
	for _, c := range m.checkRuns {
		var icon string
		switch {
		case c.Status != "completed":
			icon = reviewPending.Render(iconClock + " in progress")
		case c.Conclusion != nil:
			switch *c.Conclusion {
			case "success":
				icon = reviewApproved.Render(iconCheckCircle + " passed")
			case "failure":
				icon = reviewChanges.Render(iconXCircle + " failed")
			case "cancelled":
				icon = dimStyle.Render(iconSlash + " cancelled")
			case "skipped":
				icon = dimStyle.Render(iconSlash + " skipped")
			case "neutral":
				icon = dimStyle.Render(iconCheckCircle + " neutral")
			default:
				icon = reviewPending.Render(iconClock + " " + *c.Conclusion)
			}
		default:
			icon = reviewPending.Render(iconClock + " pending")
		}

		name := lipgloss.NewStyle().Bold(true).Render(c.Name)
		lines = append(lines, name+" "+icon)
	}
	return lines
}

// --- File Tree ---

func (m *Model) treeMoveCursor(delta int) {
	if len(m.treeEntries) == 0 {
		return
	}
	// Skip directory entries.
	for {
		m.treeCursor += delta
		if m.treeCursor < 0 {
			m.treeCursor = 0
			return
		}
		if m.treeCursor >= len(m.treeEntries) {
			m.treeCursor = len(m.treeEntries) - 1
			return
		}
		if !m.treeEntries[m.treeCursor].IsDir {
			return
		}
	}
}

// treeScrollStart returns the first visible entry index, matching RenderFileTree's scroll logic.
func (m Model) treeScrollStart() int {
	totalEntries := 2 + len(m.treeEntries) // Overview + separator + files
	start := m.treeCursor - m.height/2
	if start < 0 {
		start = 0
	}
	if start+m.height > totalEntries {
		start = totalEntries - m.height
	}
	if start < 0 {
		start = 0
	}
	return start
}

// treeEntryIndexAtY maps a screen Y coordinate to a tree entry index.
func (m Model) treeEntryIndexAtY(y int) (int, bool) {
	idx := m.treeScrollStart() + y
	if idx < 0 || idx >= len(m.treeEntries) {
		return 0, false
	}
	return idx, true
}

// syncTreeFromFileIdx updates treeCursor to match currentFileIdx.
func (m *Model) syncTreeFromFileIdx() {
	if m.currentFileIdx == -1 {
		m.treeCursor = 0
		return
	}
	for i, e := range m.treeEntries {
		if !e.IsDir && e.FileIndex == m.currentFileIdx {
			m.treeCursor = i + 2 // +2 for Overview + separator
			return
		}
	}
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

// --- Glamour ---

var markdownStyle = ansi.StyleConfig{
	Document: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{},
	},
	Heading: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			BlockSuffix: "\n",
			Color:       stringPtr("5"), // magenta
			Bold:        boolPtr(true),
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
		Color:  stringPtr("8"), // bright black
		Format: "\n────────\n",
	},
	Item: ansi.StylePrimitive{
		BlockPrefix: "• ",
	},
	Enumeration: ansi.StylePrimitive{
		BlockPrefix: ". ",
	},
	Task: ansi.StyleTask{
		Ticked:   "\U000f0132 ", // 󰄲 nf-md-checkbox_marked
		Unticked: "\ue640 ",    // nf-seti-checkbox_unchecked
		StylePrimitive: ansi.StylePrimitive{
			Color: stringPtr("2"), // green
		},
	},
	Link: ansi.StylePrimitive{
		// Hide visible URL — link text already has OSC 8 hyperlink.
		Format: "{{/*hidden*/}}",
	},
	LinkText: ansi.StylePrimitive{
		Color:     stringPtr("4"), // blue
		Bold:      boolPtr(true),
		Underline: boolPtr(true),
	},
	Code: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color:  stringPtr("3"), // yellow
			Prefix: "`",
			Suffix: "`",
		},
	},
	CodeBlock: ansi.StyleCodeBlock{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr("8"), // bright black
			},
			Margin: uintPtr(2),
		},
	},
	BlockQuote: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color:  stringPtr("8"), // bright black
			Italic: boolPtr(true),
		},
		Indent:      uintPtr(1),
		IndentToken: stringPtr("│ "),
	},
	List: ansi.StyleList{
		StyleBlock: ansi.StyleBlock{
			Indent: uintPtr(2),
		},
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

var (
	// reImage matches markdown images: ![alt](url)
	reImage = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	// reHTMLImg matches <img ...> tags
	reHTMLImg = regexp.MustCompile(`(?i)<img[^>]*>`)
	// reHTMLVideo matches <video ...>...</video> and self-closing <video ... />
	reHTMLVideo = regexp.MustCompile(`(?is)<video[^>]*(?:/>|>.*?</video>)`)
	// reHTMLPicture matches <picture>...</picture>
	reHTMLPicture = regexp.MustCompile(`(?is)<picture>.*?</picture>`)
	// reBareAssetURL matches bare GitHub asset URLs on their own line (video/image embeds).
	reBareAssetURL = regexp.MustCompile(`(?m)^\s*(https://github\.com/user-attachments/assets/\S+)\s*$`)
)

func renderMarkdown(body string, width int) string {
	if width <= 0 || body == "" {
		return body
	}

	// Convert markdown images to short links.
	body = reImage.ReplaceAllStringFunc(body, func(match string) string {
		sub := reImage.FindStringSubmatch(match)
		text := sub[1]
		if text == "" {
			text = "image"
		}
		return "[" + text + "](" + sub[2] + ")"
	})
	// Strip HTML media tags.
	body = reHTMLPicture.ReplaceAllString(body, "")
	body = reHTMLVideo.ReplaceAllString(body, "")
	body = reHTMLImg.ReplaceAllString(body, "")
	// Convert bare GitHub asset URLs (video/image embeds) to short links.
	body = reBareAssetURL.ReplaceAllString(body, "[attached media]($1)")

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
