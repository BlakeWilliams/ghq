package localdiff

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/blakewilliams/ghq/internal/review/comments"
	"github.com/blakewilliams/ghq/internal/review/copilot"
	"github.com/blakewilliams/ghq/internal/git"
	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/components"
	"github.com/blakewilliams/ghq/internal/ui/picker"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	"github.com/blakewilliams/ghq/internal/ui/uictx"
	"github.com/blakewilliams/ghq/internal/git/watcher"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/google/uuid"
)

// Messages.
type diffLoadedMsg struct {
	files []github.PullRequestFile
	mode  git.DiffMode
}

type diffErrorMsg struct {
	err error
}

type fileHighlightedMsg struct {
	highlight components.HighlightedDiff
	index     int
}

type watchReadyMsg struct{}
type copilotTickMsg struct{}

// GoToLineMsg is sent from the command bar to jump to a source line number.
type GoToLineMsg struct {
	Line int
}

// SwitchToPRMsg is sent when the user selects the PR view from the mode picker.
type SwitchToPRMsg struct {
	PR github.PullRequest
}

// OpenViewPickerMsg is sent to app.go to open the view mode picker.
type OpenViewPickerMsg struct {
	Items []picker.Item
}

// SwitchModeMsg is sent from app.go back to localdiff to change the diff mode.
type SwitchModeMsg struct {
	Mode git.DiffMode
}

// prDetectedMsg is a localdiff-internal message for PR auto-detection.
// This is separate from uictx.PRLoadedMsg so the app doesn't intercept it.
type prDetectedMsg struct {
	PR github.PullRequest
}

// prDetectFailedMsg means no PR was found for this branch.
type prDetectFailedMsg struct{}

var dimStyle = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)

type Model struct {
	ctx    *uictx.Context
	width  int
	height int

	// Git state.
	repoRoot string
	branch   string
	mode     git.DiffMode

	// Right panel viewport.
	vp      viewport.Model
	vpReady bool

	// Files.
	files            []github.PullRequestFile
	highlightedFiles []components.HighlightedDiff
	renderedFiles    []string
	filesHighlighted int
	filesLoading     bool
	currentFileIdx   int // -1 = overview

	// File tree.
	treeEntries []components.FileTreeEntry
	treeCursor  int
	treeWidth   int
	treeFocused bool

	// Diff cursor.
	diffCursor      int
	selectionAnchor int
	fileDiffs            [][]components.DiffLine
	fileDiffOffsets      [][]int
	fileCommentPositions [][]components.CommentPosition

	// Copilot.
	copilot            *copilot.Client
	copilotReplyBuf    map[string]string // commentID -> accumulated reply content
	copilotPendingFor  string            // commentID that copilot is currently replying to
	copilotPendingPath string            // file path of pending reply
	copilotPendingLine int               // line number of pending reply
	copilotPendingSide string            // side of pending reply
	copilotDots        int               // animation frame (0-3)

	// Comments.
	commentStore     *comments.CommentStore
	composing        bool
	commentInput     textarea.Model
	commentFile      string
	commentLine      int
	commentSide      string
	commentStartLine int
	commentStartSide string
	replyToID        string


	// File watcher.
	watcher *watcher.Watcher

	// Restore state from previous session.
	savedFilename string
	savedLineNo   int
	savedSide     string

	// Per-file cursor memory (session only, not persisted).
	fileCursors map[string]int // filename -> diffCursor

	// Fast filename->index lookup (rebuilt on diff load).
	filePathIndex map[string]int

	// Comment thread navigation: when > 0, cursor is inside a thread.
	// 0 = on the diff line itself, 1 = first comment, 2 = second, etc.
	threadCursor int

	// PR detection.
	pr       *github.PullRequest // nil if no PR for this branch
	prLoaded bool                // true once checked

	// Render cache.
	lastContent          string
	lastFormattedStreamLen int // length of copilot reply buffer at last formatFile

	// Shared.
	filesListLoaded  bool
	waitingG         bool
	stagingInFlight  int // number of staging ops in progress
}

func New(ctx *uictx.Context, repoRoot string, width, height int) Model {
	branch, _ := git.CurrentBranch(repoRoot)
	w, _ := watcher.New(repoRoot, nil)
	cp, _ := copilot.New(repoRoot)
	active := comments.LoadActiveState(repoRoot, branch)
	vs := comments.LoadViewState(repoRoot, branch, active.Mode)
	return Model{
		ctx:              ctx,
		repoRoot:         repoRoot,
		branch:           branch,
		mode:             active.Mode,
		width:            width,
		height:           height,
		currentFileIdx:   -1,
		selectionAnchor:  -1,
		treeWidth:        35,
		treeFocused:      true,
		watcher:          w,
		commentStore:     comments.LoadComments(repoRoot),
		copilot:          cp,
		copilotReplyBuf:  make(map[string]string),
		fileCursors:      make(map[string]int),
		filePathIndex:    make(map[string]int),
		savedFilename:    vs.Filename,
		savedLineNo:      vs.LineNo,
		savedSide:        vs.Side,
	}
}

func (m Model) BranchName() string              { return m.branch }
func (m Model) DiffMode() git.DiffMode          { return m.mode }
func (m Model) PR() *github.PullRequest         { return m.pr }
func (m Model) Files() []github.PullRequestFile { return m.files }

func (m Model) authorName() string {
	if m.ctx.Username != "" {
		return m.ctx.Username
	}
	return "you"
}

// restoreSavedPosition finds the saved file by name and restores cursor
// to the diff line matching the saved source line number.
func (m *Model) restoreSavedPosition() {
	for i, f := range m.files {
		if f.Filename == m.savedFilename {
			m.currentFileIdx = i
			m.diffCursor = m.findDiffLineBySourceLine(i, m.savedLineNo, m.savedSide)
			// Set tree cursor to match the file.
			m.treeCursor = m.treeIndexForFile(i)
			m.treeFocused = false
			break
		}
	}
	// Clear saved state so it only applies once.
	m.savedFilename = ""
}

// findDiffLineBySourceLine finds the diff line index closest to the given
// source line number. This is stable across code changes — if line 42 moves
// to diff index 15 instead of 12, we still land on line 42.
func (m Model) findDiffLineBySourceLine(fileIdx, lineNo int, side string) int {
	if fileIdx >= len(m.fileDiffs) || lineNo == 0 {
		return 0
	}
	lines := m.fileDiffs[fileIdx]
	best := 0
	bestDist := -1
	for i, dl := range lines {
		if dl.Type == components.LineHunk {
			continue
		}
		var srcLine int
		if side == "LEFT" {
			srcLine = dl.OldLineNo
		} else {
			srcLine = dl.NewLineNo
		}
		dist := lineNo - srcLine
		if dist < 0 {
			dist = -dist
		}
		if bestDist < 0 || dist < bestDist {
			best = i
			bestDist = dist
		}
	}
	return best
}

// treeFileIndex returns the file index for the file under the tree cursor, or -1.
func (m Model) treeFileIndex() int {
	if m.treeCursor < 2 {
		return -1 // Overview or separator
	}
	eIdx := m.treeCursor - 2
	if eIdx >= 0 && eIdx < len(m.treeEntries) {
		e := m.treeEntries[eIdx]
		if !e.IsDir && e.FileIndex >= 0 {
			return e.FileIndex
		}
	}
	return -1
}

// treeIndexForFile returns the tree cursor index for a given file index.
func (m Model) treeIndexForFile(fileIdx int) int {
	for i, e := range m.treeEntries {
		if e.FileIndex == fileIdx && !e.IsDir {
			return i + 2 // +2 for Overview + separator
		}
	}
	return 0
}

// saveViewState persists the current position for next session.
// Stores the source line number at the cursor (not diff index) so
// the position survives code changes that shift diff lines.
func (m Model) saveViewState() {
	var filename, side string
	var lineNo int
	if m.currentFileIdx >= 0 && m.currentFileIdx < len(m.files) {
		filename = m.files[m.currentFileIdx].Filename
		if m.currentFileIdx < len(m.fileDiffs) && m.diffCursor < len(m.fileDiffs[m.currentFileIdx]) {
			dl := m.fileDiffs[m.currentFileIdx][m.diffCursor]
			if dl.Type == components.LineDel {
				lineNo = dl.OldLineNo
				side = "LEFT"
			} else {
				lineNo = dl.NewLineNo
				side = "RIGHT"
			}
		}
	}
	comments.SaveViewState(m.repoRoot, m.branch, m.mode, comments.ViewState{
		Filename: filename,
		LineNo:   lineNo,
		Side:     side,
	})
	comments.SaveActiveState(m.repoRoot, m.branch, comments.ActiveState{Mode: m.mode})
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.loadDiff()}
	if m.watcher != nil {
		cmds = append(cmds, m.watcher.WaitCmd())
	}
	if m.copilot != nil {
		cmds = append(cmds, m.copilot.ListenCmd())
	}
	// Auto-detect PR for this branch (uses internal msg type so app doesn't intercept).
	if !m.prLoaded {
		client := m.ctx.Client
		branch := m.branch
		cmds = append(cmds, func() tea.Msg {
			pr, err := client.FetchPRByBranch(m.ctx.Owner, m.ctx.Repo, branch)
			if err != nil {
				return prDetectFailedMsg{}
			}
			return prDetectedMsg{PR: pr}
		})
	}
	return tea.Batch(cmds...)
}

// watchAfterCooldown waits a bit before re-arming the watcher, to avoid
// feedback loops where git-diff touching .git/ files re-triggers immediately.
func (m Model) watchAfterCooldown() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return watchReadyMsg{}
	})
}

func (m Model) loadDiff() tea.Cmd {
	repoRoot := m.repoRoot
	mode := m.mode
	return func() tea.Msg {
		rawDiff, err := git.Diff(repoRoot, mode)
		if err != nil {
			return diffErrorMsg{err: err}
		}
		files := git.ParseDiffToFiles(rawDiff)
		return diffLoadedMsg{files: files, mode: mode}
	}
}

func (m Model) Update(msg tea.Msg) (uictx.View, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.filesListLoaded && m.filesHighlighted > 0 {
			m.reformatAllFiles()
			m.rebuildContent()
		}
		return m, nil

	case tea.MouseClickMsg:
		if msg.X < m.treeWidth {
			if idx, ok := m.treeEntryIndexAtY(msg.Y); ok {
				if idx == 0 {
					m.treeCursor = 0
					m.currentFileIdx = -1
					m.rebuildContent()
					m.vp.GotoTop()
					m.saveViewState()
				} else if idx >= 2 {
					eIdx := idx - 2
					if eIdx >= 0 && eIdx < len(m.treeEntries) {
						e := m.treeEntries[eIdx]
						if !e.IsDir && e.FileIndex >= 0 {
							m.treeCursor = idx
							m.currentFileIdx = e.FileIndex
							m.rebuildContent()
							m.vp.GotoTop()
							m.saveViewState()
						}
					}
				}
			}
			return m, nil
		}

	case diffLoadedMsg:
		// Build index of old patches by filename for incremental updates.
		oldPatches := make(map[string]string, len(m.files))
		oldHighlights := make(map[string]components.HighlightedDiff)
		oldRendered := make(map[string]string)
		for i, f := range m.files {
			oldPatches[f.Filename] = f.Patch
			if i < len(m.highlightedFiles) && m.highlightedFiles[i].File.Filename != "" {
				oldHighlights[f.Filename] = m.highlightedFiles[i]
			}
			if i < len(m.renderedFiles) {
				oldRendered[f.Filename] = m.renderedFiles[i]
			}
		}

		m.files = msg.files
		m.highlightedFiles = make([]components.HighlightedDiff, len(msg.files))
		m.renderedFiles = make([]string, len(msg.files))
		m.fileDiffs = make([][]components.DiffLine, len(msg.files))
		m.fileDiffOffsets = make([][]int, len(msg.files))
		m.fileCommentPositions = make([][]components.CommentPosition, len(msg.files))
		m.rebuildFilePathIndex()
		m.filesListLoaded = true

		// Reuse cached highlights for files whose patch hasn't changed.
		var needHighlight []int
		for i, f := range msg.files {
			m.fileDiffs[i] = components.ParsePatchLines(f.Patch)
			if old, ok := oldPatches[f.Filename]; ok && old == f.Patch {
				if hl, ok := oldHighlights[f.Filename]; ok {
					m.highlightedFiles[i] = hl
					m.renderedFiles[i] = oldRendered[f.Filename]
					continue
				}
			}
			// Keep stale rendered content so the viewport doesn't flash
			// a skeleton while the new highlight is in progress.
			if rendered, ok := oldRendered[f.Filename]; ok {
				m.renderedFiles[i] = rendered
			}
			needHighlight = append(needHighlight, i)
		}

		m.filesHighlighted = len(msg.files) - len(needHighlight)
		m.treeEntries = components.BuildFileTree(m.files)

		// Update watcher to cover directories with changed files.
		if m.watcher != nil {
			var filenames []string
			for _, f := range msg.files {
				filenames = append(filenames, f.Filename)
			}
			m.watcher.UpdateDirs(watcher.DirsFromFiles(filenames))
		}

		// Restore saved position from previous session.
		savedOffset := m.vp.YOffset()
		if m.savedFilename != "" {
			m.restoreSavedPosition()
		} else if m.currentFileIdx >= len(m.files) {
			m.currentFileIdx = -1
			m.treeCursor = 0
		}

		// Only re-format the current file if it kept its highlights.
		// Other files will be formatted lazily when navigated to.
		if m.currentFileIdx >= 0 && m.currentFileIdx < len(msg.files) {
			f := msg.files[m.currentFileIdx]
			if _, ok := oldHighlights[f.Filename]; ok && oldPatches[f.Filename] == f.Patch {
				m.formatFile(m.currentFileIdx)
			}
		}

		m.rebuildContentIfChanged()
		// Preserve scroll position on file-watcher reloads (not initial load).
		if m.savedFilename == "" && savedOffset > 0 {
			m.vp.SetYOffset(savedOffset)
		}

		// Only highlight files that actually changed.
		if len(needHighlight) > 0 {
			// Prioritize the current file if it needs highlighting.
			for i, idx := range needHighlight {
				if idx == m.currentFileIdx {
					needHighlight[0], needHighlight[i] = needHighlight[i], needHighlight[0]
					break
				}
			}
			return m, m.highlightFileCmd(needHighlight[0])
		}
		m.filesLoading = false
		return m, nil

	case diffErrorMsg:
		return m, nil

	case SwitchModeMsg:
		m.saveViewState()
		m.mode = msg.Mode
		m.filesListLoaded = false
		m.filesHighlighted = 0
		m.filesLoading = true
		m.currentFileIdx = -1
		m.treeCursor = 0
		m.fileCursors = make(map[string]int)
		vs := comments.LoadViewState(m.repoRoot, m.branch, m.mode)
		m.savedFilename = vs.Filename
		m.savedLineNo = vs.LineNo
		m.savedSide = vs.Side
		return m, m.loadDiff()

	case prDetectedMsg:
		m.pr = &msg.PR
		m.prLoaded = true
		return m, nil

	case prDetectFailedMsg:
		m.prLoaded = true
		return m, nil

	case stageDoneMsg:
		m.stagingInFlight--
		if m.stagingInFlight < 0 {
			m.stagingInFlight = 0
		}
		// If all staging ops are done, do a single clean reload.
		if m.stagingInFlight == 0 {
			return m, m.loadDiff()
		}
		return m, nil

	case watcher.FileChangedMsg:
		// Suppress reloads while staging is in flight to avoid lock contention.
		if m.stagingInFlight > 0 {
			if m.watcher != nil {
				return m, m.watcher.WaitCmd()
			}
			return m, nil
		}
		cmds := []tea.Cmd{m.loadDiff()}
		if m.watcher != nil {
			cmds = append(cmds, m.watcher.WaitCmd())
		}
		return m, tea.Batch(cmds...)

	case fileHighlightedMsg:
		if msg.index >= len(m.highlightedFiles) {
			return m, nil
		}
		m.highlightedFiles[msg.index] = msg.highlight
		m.formatFile(msg.index)
		// Only rebuild viewport if this is the file we're currently viewing.
		if msg.index == m.currentFileIdx || m.currentFileIdx == -1 {
			m.rebuildContent()
		}
		// Find the next file that needs highlighting.
		for next := msg.index + 1; next < len(m.files); next++ {
			if m.highlightedFiles[next].File.Filename == "" {
				return m, m.highlightFileCmd(next)
			}
		}
		m.filesLoading = false
		return m, nil

	case copilot.ReplyMsg:
		m.copilotReplyBuf[msg.CommentID] += msg.Content
		if msg.Done {
			body := m.copilotReplyBuf[msg.CommentID]
			delete(m.copilotReplyBuf, msg.CommentID)
			pendingPath := m.copilotPendingPath
			m.copilotPendingFor = ""
			if body != "" {
				for _, c := range m.commentStore.Comments {
					if c.ID == msg.CommentID {
						reply := comments.LocalComment{
							ID:          uuid.New().String(),
							Body:        strings.TrimSpace(body),
							Path:        c.Path,
							Line:        c.Line,
							Side:        c.Side,
							InReplyToID: c.ID,
							Author:      "copilot",
							CreatedAt:   time.Now(),
						}
						m.commentStore.Add(reply)
						break
					}
				}
			}
			// Update the affected file's rendered cache.
			if fileIdx := m.fileIndexForPath(pendingPath); fileIdx >= 0 {
				m.formatFile(fileIdx)
				// Only rebuild viewport if we're looking at this file.
				if fileIdx == m.currentFileIdx {
					m.rebuildContent()
				}
			}
		} else if fileIdx := m.fileIndexForPath(m.copilotPendingPath); fileIdx >= 0 && fileIdx == m.currentFileIdx {
			// Streaming delta — only re-render if we're looking at this file.
			m.lastFormattedStreamLen = len(m.copilotReplyBuf[msg.CommentID])
			m.formatFile(fileIdx)
			m.rebuildContentIfChanged()
		}
		cmds := []tea.Cmd{m.copilot.ListenCmd()}
		if m.copilotPendingFor != "" {
			cmds = append(cmds, tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
				return copilotTickMsg{}
			}))
		}
		return m, tea.Batch(cmds...)

	case copilot.ErrorMsg:
		m.copilotPendingFor = ""
		// Only re-render the affected file.
		if fileIdx := m.fileIndexForPath(m.copilotPendingPath); fileIdx >= 0 {
			m.formatFile(fileIdx)
		}
		m.rebuildContent()
		cmds := []tea.Cmd{}
		if m.copilot != nil {
			cmds = append(cmds, m.copilot.ListenCmd())
		}
		return m, tea.Batch(cmds...)

	case copilotTickMsg:
		if m.copilotPendingFor != "" {
			m.copilotDots = (m.copilotDots + 1) % 4
			// Only re-format if new streaming content arrived since last format.
			streamLen := len(m.copilotReplyBuf[m.copilotPendingFor])
			if fileIdx := m.fileIndexForPath(m.copilotPendingPath); fileIdx >= 0 && fileIdx == m.currentFileIdx {
				if streamLen != m.lastFormattedStreamLen {
					m.lastFormattedStreamLen = streamLen
					m.formatFile(fileIdx)
					m.rebuildContentIfChanged()
				}
			}
			return m, tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
				return copilotTickMsg{}
			})
		}
		return m, nil

	case GoToLineMsg:
		if m.currentFileIdx >= 0 && m.hasDiffLines() {
			m.goToSourceLine(msg.Line)
		}
		return m, nil

	case tea.KeyPressMsg:
		var cmd tea.Cmd
		var handled bool
		m, cmd, handled = m.handleKey(msg)
		if handled {
			return m, cmd
		}
	}

	// When composing, delegate non-key messages to textarea.
	if m.composing {
		var cmd tea.Cmd
		m.commentInput, cmd = m.commentInput.Update(msg)
		return m, cmd
	}

	// Viewport updates.
	if m.vpReady {
		prevOffset := m.vp.YOffset()
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		if m.vp.YOffset() != prevOffset && m.currentFileIdx >= 0 {
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

	// Thread navigation mode.
	if m.threadCursor > 0 {
		switch msg.String() {
		case "j", "down":
			// Scroll within a long comment before moving to the next one.
			if m.commentExtendsBelow() {
				m.vp.SetYOffset(m.vp.YOffset() + 1)
				return m, nil, true
			}
			count := m.threadCommentCount()
			if m.threadCursor < count {
				m.threadCursor++
				m.formatFile(m.currentFileIdx)
				m.rebuildContent()
				m.scrollToThreadCursor()
			}
			return m, nil, true
		case "k", "up":
			// Scroll within a long comment before moving to the previous one.
			if m.commentExtendsAbove() {
				m.vp.SetYOffset(m.vp.YOffset() - 1)
				return m, nil, true
			}
			if m.threadCursor > 1 {
				m.threadCursor--
				m.formatFile(m.currentFileIdx)
				m.rebuildContent()
				m.scrollToThreadCursorBottom()
			} else {
				m.threadCursor = 0
				m.formatFile(m.currentFileIdx)
				m.rebuildContent()
				m.scrollToDiffCursor()
			}
			return m, nil, true
		case "ctrl+d":
			m.vp.SetYOffset(m.vp.YOffset() + m.height/2)
			return m, nil, true
		case "ctrl+u":
			m.vp.SetYOffset(m.vp.YOffset() - m.height/2)
			return m, nil, true
		case "esc":
			m.threadCursor = 0
			m.formatFile(m.currentFileIdx)
			m.rebuildContent()
			m.scrollToDiffCursor()
			return m, nil, true
		case "r":
			if cmd := m.replyToThreadAtCursor(); cmd != nil {
				m.threadCursor = 0
				return m, cmd, true
			}
			return m, nil, true
		case "x":
			m.toggleResolveAtCursor()
			m.threadCursor = 0
			return m, nil, true
		case "enter":
			m.threadCursor = 0
			m.formatFile(m.currentFileIdx)
			m.rebuildContent()
			m.scrollToDiffCursor()
			return m, nil, true
		}
		return m, nil, true
	}

	// Clear selection on esc.
	if msg.String() == "esc" && m.selectionAnchor >= 0 {
		m.selectionAnchor = -1
		return m, nil, true
	}

	switch msg.String() {
	case "f":
		m.treeFocused = !m.treeFocused
		return m, nil, true
	case "m":
		// Cycle diff mode: Working → Staged → Branch (skip Branch on default branch).
		m.saveViewState()
		defaultBranch, _ := git.DefaultBranch(m.repoRoot)
		if m.branch == defaultBranch {
			if m.mode == git.DiffWorking {
				m.mode = git.DiffStaged
			} else {
				m.mode = git.DiffWorking
			}
		} else {
			m.mode = (m.mode + 1) % 3
		}
		m.filesListLoaded = false
		m.filesHighlighted = 0
		m.filesLoading = true
		m.currentFileIdx = -1
		m.treeCursor = 0
		vs := comments.LoadViewState(m.repoRoot, m.branch, m.mode)
		m.savedFilename = vs.Filename
		m.savedLineNo = vs.LineNo
		m.savedSide = vs.Side
		return m, m.loadDiff(), true
	case "h", "left":
		m.treeFocused = true
		return m, nil, true
	case "l", "right":
		m.treeFocused = false
		return m, nil, true
	case "ctrl+k":
		m.moveTreeSelection(-1)
		return m, nil, true
	case "ctrl+j":
		m.moveTreeSelection(1)
		return m, nil, true
	case "j", "down":
		if m.treeFocused {
			m.moveTreeCursorBy(1)
			return m, nil, true
		}
		if m.currentFileIdx >= 0 && m.hasDiffLines() {
			m.selectionAnchor = -1
			m.moveDiffCursor(1)
			return m, nil, true
		}
	case "k", "up":
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
		if !m.treeFocused && m.currentFileIdx >= 0 && m.hasDiffLines() {
			if m.selectionAnchor < 0 {
				m.selectionAnchor = m.diffCursor
			}
			m.moveDiffCursor(1)
			return m, nil, true
		}
	case "K", "shift+up":
		if !m.treeFocused && m.currentFileIdx >= 0 && m.hasDiffLines() {
			if m.selectionAnchor < 0 {
				m.selectionAnchor = m.diffCursor
			}
			m.moveDiffCursor(-1)
			return m, nil, true
		}
	case "enter":
		if m.treeFocused {
			m.selectTreeEntry()
			m.treeFocused = false
			return m, nil, true
		}
		// If inside a thread, exit thread mode.
		if m.threadCursor > 0 {
			m.threadCursor = 0
			m.formatFile(m.currentFileIdx)
			m.rebuildContent()
			m.scrollToDiffCursor()
			return m, nil, true
		}
		// If on a line with comments, enter thread navigation.
		if m.currentFileIdx >= 0 && m.hasDiffLines() && m.cursorHasThread() {
			m.threadCursor = 1
			m.formatFile(m.currentFileIdx)
			m.rebuildContent()
			m.scrollToThreadCursor()
			return m, nil, true
		}
		// Otherwise open comment input.
		if m.currentFileIdx >= 0 && m.hasDiffLines() {
			return m.openCommentInput()
		}
	case "a":
		if m.currentFileIdx >= 0 && m.hasDiffLines() {
			return m.openCommentInput()
		}
	case "r":
		// Reply to comment thread on current line.
		if !m.treeFocused && m.currentFileIdx >= 0 && m.hasDiffLines() {
			if cmd := m.replyToThreadAtCursor(); cmd != nil {
				return m, cmd, true
			}
		}
	case "x":
		// Resolve/unresolve comment thread on current line.
		if !m.treeFocused && m.currentFileIdx >= 0 && m.hasDiffLines() {
			if m.toggleResolveAtCursor() {
				return m, nil, true
			}
		}
	case "s":
		if m.mode == git.DiffWorking {
			if m.treeFocused {
				// Stage the whole file under the tree cursor.
				if fileIdx := m.treeFileIndex(); fileIdx >= 0 {
					m.currentFileIdx = fileIdx
					return m.stageWholeFile(false)
				}
			} else if m.currentFileIdx >= 0 && m.hasDiffLines() {
				status := m.files[m.currentFileIdx].Status
				if status == "removed" || status == "renamed" {
					return m.stageWholeFile(false)
				}
				return m.stageSelection(false)
			}
		}
	case "u":
		if m.mode == git.DiffStaged {
			if m.treeFocused {
				if fileIdx := m.treeFileIndex(); fileIdx >= 0 {
					m.currentFileIdx = fileIdx
					return m.stageWholeFile(true)
				}
			} else if m.currentFileIdx >= 0 && m.hasDiffLines() {
				return m.stageSelection(true)
			}
		}
	case "S":
		if m.mode == git.DiffWorking {
			if m.treeFocused {
				if fileIdx := m.treeFileIndex(); fileIdx >= 0 {
					m.currentFileIdx = fileIdx
					return m.stageWholeFile(false)
				}
			} else if m.currentFileIdx >= 0 && m.hasDiffLines() {
				status := m.files[m.currentFileIdx].Status
				if status == "removed" || status == "renamed" {
					return m.stageWholeFile(false)
				}
				return m.stageHunk(false)
			}
		}
	case "U":
		if m.mode == git.DiffStaged {
			if m.treeFocused {
				if fileIdx := m.treeFileIndex(); fileIdx >= 0 {
					m.currentFileIdx = fileIdx
					return m.stageWholeFile(true)
				}
			} else if m.currentFileIdx >= 0 && m.hasDiffLines() {
				return m.stageHunk(true)
			}
		}
	case "ctrl+d":
		m.selectionAnchor = -1
		if m.treeFocused {
			m.moveTreeCursorBy(m.height / 2)
		} else if m.currentFileIdx >= 0 && m.hasDiffLines() {
			m.scrollAndSyncCursor(m.height / 2)
		} else {
			m.vp.SetYOffset(m.vp.YOffset() + m.height/2)
		}
		return m, nil, true
	case "ctrl+u":
		m.selectionAnchor = -1
		if m.treeFocused {
			m.moveTreeCursorBy(-m.height / 2)
		} else if m.currentFileIdx >= 0 && m.hasDiffLines() {
			m.scrollAndSyncCursor(-m.height / 2)
		} else {
			m.vp.SetYOffset(m.vp.YOffset() - m.height/2)
		}
		return m, nil, true
	case "ctrl+f":
		m.selectionAnchor = -1
		if m.treeFocused {
			m.moveTreeCursorBy(m.height)
		} else if m.currentFileIdx >= 0 && m.hasDiffLines() {
			m.scrollAndSyncCursor(m.height)
		} else {
			m.vp.SetYOffset(m.vp.YOffset() + m.height)
		}
		return m, nil, true
	case "ctrl+b":
		m.selectionAnchor = -1
		if m.treeFocused {
			m.moveTreeCursorBy(-m.height)
		} else if m.currentFileIdx >= 0 && m.hasDiffLines() {
			m.scrollAndSyncCursor(-m.height)
		} else {
			m.vp.SetYOffset(m.vp.YOffset() - m.height)
		}
		return m, nil, true
	case "G":
		m.waitingG = false
		if m.treeFocused {
			totalEntries := 2 + len(m.treeEntries)
			m.moveTreeCursorBy(totalEntries)
		} else {
			m.vp.GotoBottom()
			if m.currentFileIdx >= 0 && m.hasDiffLines() {
				m.syncDiffCursorToViewport()
			}
		}
		return m, nil, true
	case "g":
		if m.waitingG {
			m.waitingG = false
			if m.treeFocused {
				m.moveTreeCursorBy(-2 - len(m.treeEntries))
			} else {
				m.vp.GotoTop()
				if m.currentFileIdx >= 0 && m.hasDiffLines() {
					m.syncDiffCursorToViewport()
				}
			}
			return m, nil, true
		}
		m.waitingG = true
		return m, nil, true
	default:
		m.waitingG = false
	}
	return m, nil, false
}

// StatusHints returns left and right hint groups for the status bar.
func (m Model) KeyBindings() []uictx.KeyBinding {
	return []uictx.KeyBinding{
		{Key: "j / k", Description: "Move cursor down / up", Keywords: []string{"navigate"}},
		{Key: "J / K", Description: "Extend selection range"},
		{Key: "h / l", Description: "Focus left / right pane"},
		{Key: "f", Description: "Toggle tree focus"},
		{Key: "ctrl+j / k", Description: "Previous / next file"},
		{Key: "ctrl+d / u", Description: "Scroll half page down / up"},
		{Key: "ctrl+f / b", Description: "Scroll full page down / up"},
		{Key: "g g", Description: "Go to top"},
		{Key: "G", Description: "Go to bottom"},
		{Key: "m", Description: "Cycle diff mode (working/staged/branch)", Keywords: []string{"toggle"}},
		{Key: "a", Description: "Add comment on current line"},
		{Key: "enter", Description: "Select file / enter comment thread"},
		{Key: "r", Description: "Reply to comment thread"},
		{Key: "x", Description: "Resolve / unresolve thread"},
		{Key: "s", Description: "Stage line/selection (Working mode)"},
		{Key: "u", Description: "Unstage line/selection (Staged mode)"},
		{Key: "S", Description: "Stage entire hunk"},
		{Key: "U", Description: "Unstage entire hunk"},
		{Key: ":N", Description: "Jump to line number N"},
		{Key: "esc", Description: "Cancel / exit thread"},
	}
}

func (m Model) StatusHints() (left, right []string) {
	if m.composing {
		left = append(left, styles.StatusBarKey.Render("esc")+" "+styles.StatusBarHint.Render("cancel"))
		right = append(right, styles.StatusBarKey.Render("enter")+" "+styles.StatusBarHint.Render("submit"))
		return
	}
	if m.treeFocused {
		left = append(left, styles.StatusBarKey.Render("f")+" "+styles.StatusBarHint.Render("unfocus tree"))
	} else {
		left = append(left, styles.StatusBarKey.Render("f")+" "+styles.StatusBarHint.Render("focus tree"))
	}
	left = append(left, styles.StatusBarKey.Render("h/l")+" "+styles.StatusBarHint.Render("panes"))
	left = append(left, styles.StatusBarKey.Render("ctrl+j/k")+" "+styles.StatusBarHint.Render("files"))
	if !m.treeFocused && m.currentFileIdx >= 0 {
		left = append(left, styles.StatusBarKey.Render("J/K")+" "+styles.StatusBarHint.Render("select range"))
		if m.threadCursor > 0 {
			count := m.threadCommentCount()
			left = append(left, styles.StatusBarHint.Render(fmt.Sprintf("comment %d/%d", m.threadCursor, count)))
			left = append(left, styles.StatusBarKey.Render("r")+" "+styles.StatusBarHint.Render("reply"))
			left = append(left, styles.StatusBarKey.Render("x")+" "+styles.StatusBarHint.Render("resolve"))
		} else if m.cursorHasThread() {
			left = append(left, styles.StatusBarKey.Render("r")+" "+styles.StatusBarHint.Render("reply"))
			left = append(left, styles.StatusBarKey.Render("x")+" "+styles.StatusBarHint.Render("resolve"))
		}
	}
	modeStr := m.mode.String()
	if m.pr != nil {
		modeStr += fmt.Sprintf(" · PR #%d", m.pr.Number)
	}
	right = append(right, styles.StatusBarKey.Render("m")+" "+styles.StatusBarHint.Render(modeStr))
	return
}

// cursorHasThread returns true if the cursor is on a line with a comment thread.
func (m Model) cursorHasThread() bool {
	path, line, side, ok := m.cursorThreadInfo()
	if !ok {
		return false
	}
	return m.commentStore.FindThreadRoot(path, line, side) != ""
}

// --- View ---

func (m Model) View() string {
	if !m.vpReady {
		return ""
	}

	rightView := m.vp.View()
	if m.currentFileIdx >= 0 {
		rightView = m.overlayDiffCursor(rightView)
	}

	return m.renderLayout(rightView)
}

func (m Model) renderLayout(rightView string) string {
	treeW := m.treeWidth
	innerTreeW := treeW - 2
	innerTreeH := m.height - 2

	bc := m.borderStyle()
	var treeBorderStyle lipgloss.Style
	if m.treeFocused {
		treeBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	} else {
		treeBorderStyle = bc
	}

	// Tree border.
	titleStr := " " + lipgloss.NewStyle().Bold(true).Render("Files") + " "
	titleW := lipgloss.Width(titleStr)
	fillW := treeW - 3 - titleW
	if fillW < 0 {
		fillW = 0
	}
	topBorder := treeBorderStyle.Render("╭─") + titleStr + treeBorderStyle.Render(strings.Repeat("─", fillW)+"╮")
	bw := treeW - 2
	if bw < 0 {
		bw = 0
	}
	bottomBorder := treeBorderStyle.Render("╰" + strings.Repeat("─", bw) + "╯")
	sideBorderL := treeBorderStyle.Render("│")
	sideBorderR := treeBorderStyle.Render("│")

	treeContentLines := components.RenderFileTree(m.treeEntries, m.files, m.treeCursor, m.currentFileIdx, innerTreeW, innerTreeH)
	rightLines := strings.Split(rightView, "\n")

	// Right panel border.
	rightW := m.rightPanelWidth()
	innerRightW := rightW - 2
	var rightBorderStyle lipgloss.Style
	if !m.treeFocused {
		rightBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	} else {
		rightBorderStyle = bc
	}

	// Right panel title.
	var rightTitle string
	if m.currentFileIdx >= 0 && m.currentFileIdx < len(m.files) {
		rightTitle = " " + lipgloss.NewStyle().Bold(true).Render(m.files[m.currentFileIdx].Filename) + " "
	} else {
		rightTitle = " " + lipgloss.NewStyle().Bold(true).Render("Overview") + " "
	}
	rtW := lipgloss.Width(rightTitle)
	rtFill := rightW - 3 - rtW
	if rtFill < 0 {
		rtFill = 0
	}
	rightTop := rightBorderStyle.Render("╭─") + rightTitle + rightBorderStyle.Render(strings.Repeat("─", rtFill)+"╮")
	rbw := rightW - 2
	if rbw < 0 {
		rbw = 0
	}
	rightBottom := rightBorderStyle.Render("╰" + strings.Repeat("─", rbw) + "╯")
	rightSideL := rightBorderStyle.Render("│")
	rightSideR := rightBorderStyle.Render("│")

	var b strings.Builder
	for i := 0; i < m.height; i++ {
		var treeLine string
		if i == 0 {
			treeLine = topBorder
		} else if i == m.height-1 {
			treeLine = bottomBorder
		} else {
			tIdx := i - 1
			cl := ""
			if tIdx < len(treeContentLines) {
				cl = treeContentLines[tIdx]
			}
			treeLine = sideBorderL + cl + sideBorderR
		}

		var rightLine string
		if i == 0 {
			rightLine = rightTop
		} else if i == m.height-1 {
			rightLine = rightBottom
		} else {
			rIdx := i - 1
			rl := ""
			if rIdx < len(rightLines) {
				rl = rightLines[rIdx]
			}
			rlW := lipgloss.Width(rl)
			if rlW < innerRightW {
				rl += strings.Repeat(" ", innerRightW-rlW)
			}
			rightLine = rightSideL + rl + rightSideR
		}

		b.WriteString(treeLine + rightLine)
		if i < m.height-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// --- Content building ---

func (m *Model) rebuildContent() {
	innerW := m.rightPanelInnerWidth()
	innerH := m.height - 2

	if !m.vpReady {
		m.vp = viewport.New()
		m.vpReady = true
	}
	m.vp.SetWidth(innerW)
	m.vp.SetHeight(innerH)

	var newContent string
	if m.currentFileIdx == -1 {
		newContent = m.buildOverviewContent(innerW)
	} else {
		newContent = m.buildFileContent(innerW)
	}
	m.vp.SetContent(newContent)
}

// rebuildContentIfChanged only updates the viewport if the content actually changed.
// Use this for paths where the content might not have changed (timer ticks, etc.)
func (m *Model) rebuildContentIfChanged() {
	innerW := m.rightPanelInnerWidth()
	innerH := m.height - 2

	if !m.vpReady {
		m.vp = viewport.New()
		m.vpReady = true
	}
	m.vp.SetWidth(innerW)
	m.vp.SetHeight(innerH)

	var newContent string
	if m.currentFileIdx == -1 {
		newContent = m.buildOverviewContent(innerW)
	} else {
		newContent = m.buildFileContent(innerW)
	}
	if newContent != m.lastContent {
		m.lastContent = newContent
		m.vp.SetContent(newContent)
	}
}

func (m Model) buildOverviewContent(w int) string {
	var content strings.Builder

	// Branch + mode info.
	branchStr := lipgloss.NewStyle().Bold(true).Render(m.branch)
	modeStr := dimStyle.Render("(" + m.mode.String() + ")")
	content.WriteString("\n  " + branchStr + " " + modeStr + "\n")

	if len(m.files) == 0 {
		if m.filesListLoaded {
			content.WriteString("\n  " + dimStyle.Render("No changes") + "\n")
		} else {
			content.WriteString("\n  " + dimStyle.Render("Loading...") + "\n")
		}
		return content.String()
	}

	// Stats summary.
	adds, dels := git.FilesAddedDeletedStats(m.files)
	statsStr := fmt.Sprintf("%d files changed", len(m.files))
	if adds > 0 {
		statsStr += fmt.Sprintf(", %d insertions(+)", adds)
	}
	if dels > 0 {
		statsStr += fmt.Sprintf(", %d deletions(-)", dels)
	}
	content.WriteString("\n  " + dimStyle.Render(statsStr) + "\n")

	// File list.
	content.WriteString("\n")
	for _, f := range m.files {
		icon := "≈"
		switch f.Status {
		case "added":
			icon = lipgloss.NewStyle().Foreground(lipgloss.Green).Render("+")
		case "removed":
			icon = lipgloss.NewStyle().Foreground(lipgloss.Red).Render("-")
		case "renamed":
			icon = lipgloss.NewStyle().Foreground(lipgloss.Yellow).Render("→")
		default:
			icon = lipgloss.NewStyle().Foreground(lipgloss.Blue).Render("≈")
		}
		content.WriteString("  " + icon + " " + f.Filename + "\n")
	}

	content.WriteString("\n  " + dimStyle.Render("Press m to toggle diff mode") + "\n")
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
		// Skeleton.
		for i := 0; i < 20; i++ {
			gutter := dimStyle.Render(strings.Repeat("─", components.TotalGutterWidth(components.DefaultGutterColWidth)))
			lineW := 15 + (i*7)%25
			if lineW > w-12 {
				lineW = w - 12
			}
			code := dimStyle.Render(strings.Repeat("─", lineW))
			content.WriteString(gutter + " " + code + "\n")
		}
	}
	content.WriteString("\n" + strings.Repeat("\n", m.height/2))
	return content.String()
}

// --- File rendering pipeline ---

func (m Model) highlightFileCmd(index int) tea.Cmd {
	f := m.files[index]
	repoRoot := m.repoRoot
	chromaStyle := m.ctx.DiffColors.ChromaStyle

	return func() tea.Msg {
		var fileContent string
		if f.Status != "removed" && f.Patch != "" {
			if content, err := git.FileContent(repoRoot, f.Filename); err == nil {
				fileContent = content
			}
		}
		hl := components.HighlightDiffFile(f, fileContent, chromaStyle)
		return fileHighlightedMsg{highlight: hl, index: index}
	}
}

func (m *Model) formatFile(index int) {
	if index >= len(m.highlightedFiles) {
		return
	}
	hl := m.highlightedFiles[index]
	width := m.contentWidth()
	colors := m.ctx.DiffColors
	fileComments := m.commentsForFile(index)

	// Highlight the comment thread under the cursor.
	var opts components.DiffFormatOptions
	if index == m.currentFileIdx && m.hasDiffLines() &&
		m.diffCursor < len(m.fileDiffs[index]) {
		dl := m.fileDiffs[index][m.diffCursor]
		if dl.Type == components.LineDel {
			opts.HighlightThreadLine = dl.OldLineNo
			opts.HighlightThreadSide = "LEFT"
		} else if dl.Type != components.LineHunk {
			opts.HighlightThreadLine = dl.NewLineNo
			opts.HighlightThreadSide = "RIGHT"
		}
		// Only highlight when inside a thread (threadCursor > 0).
		// threadCursor=0 means cursor is on the diff line, no comment highlighted.
		if m.threadCursor > 0 {
			opts.HighlightCommentIndex = m.threadCursor
		} else {
			// Not in thread mode — don't highlight any comments.
			opts.HighlightThreadLine = 0
			opts.HighlightThreadSide = ""
		}
	}
	// Render copilot bodies as markdown with the correct diff background.
	opts.RenderBody = func(body string, width int, bg string) string {
		return renderMarkdownBody(body, width, bg)
	}

	result := components.FormatDiffFile(hl, width, colors, fileComments, opts)
	m.renderedFiles[index] = result.Content
	if index < len(m.fileDiffOffsets) {
		m.fileDiffOffsets[index] = result.DiffLineOffsets
	}
	if index < len(m.fileCommentPositions) {
		m.fileCommentPositions[index] = result.CommentPositions
	}
}

func (m Model) commentsForFile(fileIdx int) []github.ReviewComment {
	if m.commentStore == nil || fileIdx < 0 || fileIdx >= len(m.files) {
		return nil
	}
	filename := m.files[fileIdx].Filename
	fileComments := m.commentStore.ForFile(filename)

	// Wrap width for comment bodies inside the thread box.
	var gutterW int
	if fileIdx < len(m.fileDiffs) {
		gutterW = components.TotalGutterWidth(components.GutterColWidth(m.fileDiffs[fileIdx]))
	} else {
		gutterW = components.TotalGutterWidth(components.DefaultGutterColWidth)
	}
	wrapW := m.contentWidth() - gutterW - 4 // gutter + "│ " + " │"
	if wrapW < 20 {
		wrapW = 20
	}

	// Don't pre-render markdown here — it's done in commentsForFileWithBg
	// at render time when we know the diff line's background color.

	// Add pending copilot reply as a temporary comment so it renders inline.
	if m.copilotPendingFor != "" && m.copilotPendingPath == filename {
		dots := strings.Repeat(".", m.copilotDots+1)
		body := m.copilotReplyBuf[m.copilotPendingFor]
		if body == "" {
			body = "Thinking" + dots
		} else {
			body = body + dots
		}
		line := m.copilotPendingLine
		replyToInt := comments.IDToInt(m.copilotPendingFor)
		pending := github.ReviewComment{
			ID:           0,
			Body:         body,
			Path:         filename,
			Line:         &line,
			OriginalLine: &line,
			Side:         m.copilotPendingSide,
			InReplyToID:  &replyToInt,
			User:         github.User{Login: "copilot"},
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		fileComments = append(fileComments, pending)
	}

	return fileComments
}

// renderMarkdownBody does lightweight inline markdown rendering suitable
// for comment thread boxes. Wraps text to width, applies bold, italic,
// code, and code block formatting. Uses reset+bg instead of bare \033[0m
// so the diff background color survives through formatting resets.
func renderMarkdownBody(body string, width int, bg string) string {
	reset := "\033[0m" + bg

	var out strings.Builder
	inCodeBlock := false

	for _, line := range strings.Split(body, "\n") {
		// Fenced code blocks — don't wrap, just indent.
		if strings.HasPrefix(line, "```") {
			inCodeBlock = !inCodeBlock
			if inCodeBlock {
				out.WriteString("\033[90m" + bg)
			} else {
				out.WriteString(reset)
			}
			continue
		}
		if inCodeBlock {
			out.WriteString("  " + line + "\n")
			continue
		}

		for _, wrapped := range wordWrap(line, width) {
			out.WriteString(renderInlineMarkdown(wrapped, reset) + "\n")
		}
	}

	if inCodeBlock {
		out.WriteString(reset)
	}

	return strings.TrimRight(out.String(), "\n")
}

// wordWrap splits a line into multiple lines at word boundaries to fit width.
// Uses visible width (not byte length) so ANSI codes don't break wrapping.
func wordWrap(line string, width int) []string {
	if width <= 0 || lipgloss.Width(line) <= width {
		return []string{line}
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		// Use len here since we're working with plain text before ANSI is applied.
		if len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
		} else {
			cur += " " + w
		}
	}
	lines = append(lines, cur)
	return lines
}

// renderInlineMarkdown handles bold, italic, and inline code.
// reset should be "\033[0m" + bg to preserve the diff background.
func renderInlineMarkdown(line string, reset string) string {
	// Inline code: `code`
	for {
		start := strings.Index(line, "`")
		if start < 0 {
			break
		}
		end := strings.Index(line[start+1:], "`")
		if end < 0 {
			break
		}
		end += start + 1
		code := line[start+1 : end]
		line = line[:start] + "\033[36m" + code + reset + line[end+1:]
	}

	// Bold: **text** or __text__
	line = replaceMarkdownPair(line, "**", "\033[1m", reset)
	line = replaceMarkdownPair(line, "__", "\033[1m", reset)

	// Italic: *text* or _text_
	line = replaceMarkdownPair(line, "*", "\033[3m", reset)

	return line
}

func replaceMarkdownPair(s, delim, open, close string) string {
	for {
		start := strings.Index(s, delim)
		if start < 0 {
			break
		}
		end := strings.Index(s[start+len(delim):], delim)
		if end < 0 {
			break
		}
		end += start + len(delim)
		inner := s[start+len(delim) : end]
		s = s[:start] + open + inner + close + s[end+len(delim):]
	}
	return s
}

func (m *Model) reformatAllFiles() {
	for i := 0; i < len(m.files); i++ {
		if i < len(m.highlightedFiles) && m.highlightedFiles[i].File.Filename != "" {
			m.formatFile(i)
		}
	}
}

// --- Layout helpers ---

func (m Model) rightPanelWidth() int {
	return m.width - m.treeWidth
}

func (m Model) rightPanelInnerWidth() int {
	return m.rightPanelWidth() - 2
}

func (m Model) contentWidth() int {
	return m.rightPanelInnerWidth()
}

// viewportHeight returns the actual visible height of the viewport (inside borders).
func (m Model) viewportHeight() int {
	return m.height - 2
}

func (m Model) borderStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(m.ctx.DiffColors.BorderColor)
}

// --- Tree navigation ---

func (m *Model) moveTreeSelection(delta int) {
	totalEntries := 2 + len(m.treeEntries)
	newCursor := m.treeCursor + delta

	for newCursor >= 0 && newCursor < totalEntries {
		if newCursor == 0 {
			break
		}
		if newCursor == 1 {
			newCursor += delta
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

func (m *Model) selectTreeEntry() {
	m.selectionAnchor = -1
	// Save cursor position for the file we're leaving.
	if m.currentFileIdx >= 0 && m.currentFileIdx < len(m.files) {
		m.fileCursors[m.files[m.currentFileIdx].Filename] = m.diffCursor
	}
	m.threadCursor = 0
	if m.treeCursor == 0 {
		m.currentFileIdx = -1
		m.rebuildContent()
		m.vp.GotoTop()
		m.saveViewState()
		return
	}
	eIdx := m.treeCursor - 2
	if eIdx >= 0 && eIdx < len(m.treeEntries) {
		e := m.treeEntries[eIdx]
		if !e.IsDir && e.FileIndex >= 0 && e.FileIndex < len(m.files) && e.FileIndex < len(m.fileDiffs) {
			m.currentFileIdx = e.FileIndex
			// Restore cursor if we've been here before, otherwise start at top.
			if saved, ok := m.fileCursors[m.files[e.FileIndex].Filename]; ok && saved < len(m.fileDiffs[e.FileIndex]) {
				m.diffCursor = saved
			} else {
				m.diffCursor = m.firstNonHunkLine(e.FileIndex)
			}
			m.formatFile(e.FileIndex)
			m.rebuildContent()
			m.scrollToDiffCursor()
			m.saveViewState()
		}
	}
}

func (m Model) treeEntryIndexAtY(y int) (int, bool) {
	// y == 0 is the top border, so content starts at y == 1.
	idx := y - 1 // into content lines
	if idx < 0 {
		return 0, false
	}
	totalEntries := 2 + len(m.treeEntries)
	if idx >= totalEntries {
		return 0, false
	}
	return idx, true
}

// --- Diff cursor ---

func (m Model) hasDiffLines() bool {
	idx := m.currentFileIdx
	return idx >= 0 && idx < len(m.fileDiffs) && len(m.fileDiffs[idx]) > 0
}

func (m *Model) moveDiffCursor(delta int) {
	if m.currentFileIdx < 0 || m.currentFileIdx >= len(m.fileDiffs) {
		return
	}
	m.threadCursor = 0
	lines := m.fileDiffs[m.currentFileIdx]
	oldPos := m.diffCursor
	newPos := m.diffCursor + delta

	for newPos >= 0 && newPos < len(lines) && lines[newPos].Type == components.LineHunk {
		newPos += delta
	}

	if newPos < 0 || newPos >= len(lines) {
		return
	}
	m.diffCursor = newPos

	// Only re-format if moving to/from a line with comments (for highlight update).
	if m.lineHasComments(oldPos) || m.lineHasComments(newPos) {
		m.formatFile(m.currentFileIdx)
		m.rebuildContent()
	}
	m.scrollToDiffCursor()
}

// lineHasComments returns true if the diff line at the given index has a comment thread.
func (m Model) lineHasComments(diffIdx int) bool {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileDiffs) || diffIdx < 0 || diffIdx >= len(m.fileDiffs[idx]) {
		return false
	}
	dl := m.fileDiffs[idx][diffIdx]
	if dl.Type == components.LineHunk {
		return false
	}
	path := m.files[idx].Filename
	var line int
	var side string
	if dl.Type == components.LineDel {
		line = dl.OldLineNo
		side = "LEFT"
	} else {
		line = dl.NewLineNo
		side = "RIGHT"
	}
	return m.commentStore != nil && m.commentStore.FindThreadRoot(path, line, side) != ""
}

func (m *Model) moveDiffCursorBy(delta int) {
	if m.currentFileIdx < 0 || m.currentFileIdx >= len(m.fileDiffs) {
		return
	}
	m.threadCursor = 0
	lines := m.fileDiffs[m.currentFileIdx]
	newPos := m.diffCursor + delta

	if newPos < 0 {
		newPos = 0
	}
	if newPos >= len(lines) {
		newPos = len(lines) - 1
	}

	if newPos >= 0 && newPos < len(lines) && lines[newPos].Type == components.LineHunk {
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

	oldPos := m.diffCursor
	m.diffCursor = newPos
	if m.lineHasComments(oldPos) || m.lineHasComments(newPos) {
		m.formatFile(m.currentFileIdx)
		m.rebuildContent()
	}
	m.scrollToDiffCursor()
}

// goToSourceLine jumps the diff cursor to the line closest to the given
// source line number (new side preferred, falls back to old side).
func (m *Model) goToSourceLine(lineNo int) {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileDiffs) {
		return
	}
	best := m.findDiffLineBySourceLine(idx, lineNo, "RIGHT")
	m.diffCursor = best
	m.selectionAnchor = -1
	m.formatFile(idx)
	m.rebuildContent()
	m.scrollToDiffCursor()
}

// threadCommentCount returns the number of comments in the thread on the
// current cursor line, consistent with what's actually rendered.
func (m Model) threadCommentCount() int {
	path, line, side, ok := m.cursorThreadInfo()
	if !ok {
		return 0
	}
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileCommentPositions) {
		return 0
	}
	// Count from the rendered comment positions — this is the source of truth.
	count := 0
	for _, cp := range m.fileCommentPositions[idx] {
		if cp.Line == line && cp.Side == side {
			count++
		}
	}
	_ = path
	return count
}

// scrollToThreadCursor scrolls the viewport to show the selected comment
// using the exact rendered positions tracked by CommentPositions.
func (m *Model) scrollToThreadCursor() {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileCommentPositions) {
		return
	}

	// Find the comment position matching the current cursor line and threadCursor.
	path, line, side, ok := m.cursorThreadInfo()
	if !ok {
		return
	}

	targetLine := -1
	for _, cp := range m.fileCommentPositions[idx] {
		if cp.Line == line && cp.Side == side && cp.Idx == m.threadCursor-1 {
			targetLine = cp.Offset
			break
		}
	}
	if targetLine < 0 {
		_ = path // suppress unused
		return
	}

	vpH := m.viewportHeight()
	top := m.vp.YOffset()
	bottom := top + vpH - 1

	if targetLine < top+scrollMargin {
		target := targetLine - scrollMargin
		if target < 0 {
			target = 0
		}
		m.vp.SetYOffset(target)
	} else if targetLine > bottom-scrollMargin {
		m.vp.SetYOffset(targetLine - vpH + scrollMargin + 1)
	}
}

// currentCommentRange returns the start and end rendered line offsets for
// the currently selected comment (threadCursor). Returns (-1,-1) if unknown.
func (m Model) currentCommentRange() (start, end int) {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileCommentPositions) {
		return -1, -1
	}
	_, line, side, ok := m.cursorThreadInfo()
	if !ok {
		return -1, -1
	}

	// Find all positions for this thread.
	var threadPositions []components.CommentPosition
	for _, cp := range m.fileCommentPositions[idx] {
		if cp.Line == line && cp.Side == side {
			threadPositions = append(threadPositions, cp)
		}
	}

	ci := m.threadCursor - 1
	if ci < 0 || ci >= len(threadPositions) {
		return -1, -1
	}

	start = threadPositions[ci].Offset
	// End is the next comment's start, or estimate from the next diff line.
	if ci+1 < len(threadPositions) {
		end = threadPositions[ci+1].Offset - 1
	} else {
		// Last comment in thread — find where the thread ends.
		// Use the next diff line's offset as the boundary.
		if m.diffCursor+1 < len(m.fileDiffOffsets[idx]) {
			end = m.fileDiffOffsets[idx][m.diffCursor+1] - 1
		} else {
			// Last diff line — estimate generously.
			end = start + 50
		}
	}
	return start, end
}

// commentExtendsBelow returns true if the selected comment's body extends
// below the viewport (needs scrolling down to see the rest).
func (m Model) commentExtendsBelow() bool {
	_, end := m.currentCommentRange()
	if end < 0 {
		return false
	}
	vpH := m.viewportHeight()
	bottom := m.vp.YOffset() + vpH - 1
	return end > bottom
}

// commentExtendsAbove returns true if the selected comment's header is above
// the viewport (needs scrolling up to see the top).
func (m Model) commentExtendsAbove() bool {
	start, _ := m.currentCommentRange()
	if start < 0 {
		return false
	}
	return start < m.vp.YOffset()
}

// scrollToThreadCursorBottom scrolls so the bottom of the comment is visible
// (used when navigating up into a long comment).
func (m *Model) scrollToThreadCursorBottom() {
	_, end := m.currentCommentRange()
	if end < 0 {
		m.scrollToThreadCursor()
		return
	}
	vpH := m.viewportHeight()
	bottom := m.vp.YOffset() + vpH - 1
	if end > bottom {
		m.vp.SetYOffset(end - vpH + scrollMargin + 1)
	}
	// Also make sure the header is visible if the comment fits.
	start, _ := m.currentCommentRange()
	if start >= 0 && start < m.vp.YOffset() {
		commentH := end - start + 1
		if commentH <= vpH-scrollMargin*2 {
			m.scrollToThreadCursor()
		}
	}
}

// scrollCommentBoxIntoView scrolls the viewport so the comment input box
// (which is inserted after the cursor line) is fully visible.
func (m *Model) scrollCommentBoxIntoView() {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileDiffOffsets) || m.diffCursor >= len(m.fileDiffOffsets[idx]) {
		return
	}
	vpH := m.viewportHeight()
	cursorLine := m.fileDiffOffsets[idx][m.diffCursor]
	// The comment box is ~8 lines (border + textarea + hints) inserted after the cursor line.
	boxBottom := cursorLine + 10
	bottom := m.vp.YOffset() + vpH - 1
	if boxBottom > bottom {
		m.vp.SetYOffset(boxBottom - vpH + 1)
	}
}

func (m Model) firstNonHunkLine(fileIdx int) int {
	for i, dl := range m.fileDiffs[fileIdx] {
		if dl.Type != components.LineHunk {
			return i
		}
	}
	return 0
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
	vpH := m.viewportHeight()
	absLine := m.fileDiffOffsets[idx][m.diffCursor]
	top := m.vp.YOffset()
	bottom := top + vpH - 1

	if absLine < top+scrollMargin {
		target := absLine - scrollMargin
		if target < 0 {
			target = 0
		}
		m.vp.SetYOffset(target)
	} else if absLine > bottom-scrollMargin {
		m.vp.SetYOffset(absLine - vpH + scrollMargin + 1)
	}
}

// scrollAndSyncCursor scrolls the viewport by delta lines, then moves
// the diff cursor to the diff line at the same screen-relative position.
// This keeps the cursor visually stable (vim ctrl+d/u behavior).
func (m *Model) scrollAndSyncCursor(delta int) {
	m.threadCursor = 0
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileDiffOffsets) {
		return
	}

	// Remember cursor's screen-relative position.
	cursorAbs := 0
	if m.diffCursor < len(m.fileDiffOffsets[idx]) {
		cursorAbs = m.fileDiffOffsets[idx][m.diffCursor]
	}
	relPos := cursorAbs - m.vp.YOffset()

	// Scroll viewport.
	m.vp.SetYOffset(m.vp.YOffset() + delta)

	// Find the diff line closest to the same screen position.
	targetAbs := m.vp.YOffset() + relPos
	offsets := m.fileDiffOffsets[idx]
	diffs := m.fileDiffs[idx]
	best := -1
	bestDist := 0
	for i := 0; i < len(offsets); i++ {
		if i < len(diffs) && diffs[i].Type == components.LineHunk {
			continue
		}
		dist := offsets[i] - targetAbs
		if dist < 0 {
			dist = -dist
		}
		if best == -1 || dist < bestDist {
			best = i
			bestDist = dist
		}
	}
	if best >= 0 {
		oldCursor := m.diffCursor
		m.diffCursor = best
		// Only reformat if cursor moved to/from a commented line.
		if m.lineHasComments(oldCursor) || m.lineHasComments(best) {
			m.formatFile(idx)
			m.rebuildContent()
		}
	}
}

func (m *Model) syncDiffCursorToViewport() {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileDiffOffsets) || len(m.fileDiffOffsets[idx]) == 0 {
		return
	}
	center := m.vp.YOffset() + m.viewportHeight()/2
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

// --- Cursor overlay ---

func (m Model) overlayDiffCursor(view string) string {
	if !m.filesListLoaded || !m.hasDiffLines() {
		return view
	}

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
	if rel < 0 || rel >= m.viewportHeight() {
		return -1
	}
	return rel
}

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

func (m Model) applySelectionHighlight(line string, dl components.DiffLine) string {
	if dl.Type == components.LineHunk {
		return line
	}

	prefix, inner, suffix := splitDiffBorders(line)

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

// --- Comment composition ---

func (m Model) handleCommentKey(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		m.composing = false
		m.selectionAnchor = -1
		m.rebuildContent()
		return m, nil, true
	case "shift+enter":
		// Insert newline.
		m.commentInput.InsertString("\n")
		m.rebuildContent()
		return m, nil, true
	case "enter":
		body := strings.TrimSpace(m.commentInput.Value())
		if body == "" {
			m.composing = false
			m.selectionAnchor = -1
			m.rebuildContent()
			return m, nil, true
		}
		m.composing = false

		comment := comments.LocalComment{
			ID:        uuid.New().String(),
			Body:      body,
			Path:      m.commentFile,
			Line:      m.commentLine,
			Side:      m.commentSide,
			StartLine: m.commentStartLine,
			StartSide: m.commentStartSide,
			Author:    m.authorName(),
			CreatedAt: time.Now(),
		}
		if m.replyToID != "" {
			comment.InReplyToID = m.replyToID
		}

		m.commentStore.Add(comment)
		m.selectionAnchor = -1
		m.reformatAllFiles()
		m.rebuildContent()

		// Send to Copilot for AI review.
		if m.copilot != nil {
			m.copilotPendingFor = comment.ID
			m.copilotPendingPath = comment.Path
			m.copilotPendingLine = comment.Line
			m.copilotPendingSide = comment.Side
			m.copilotDots = 0
			diffHunk := m.getDiffHunkForComment(comment)
			threadHistory := m.getThreadHistory(comment)
			fileContent, _ := git.FileContent(m.repoRoot, comment.Path)
			fullDiff := m.getFullFileDiff(comment.Path)
			return m, tea.Batch(
				m.copilot.SendComment(comment.ID, body, comment.Path, fileContent, fullDiff, diffHunk, threadHistory),
				tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return copilotTickMsg{} }),
			), true
		}
		return m, nil, true
	}

	// Delegate to textarea.
	var cmd tea.Cmd
	m.commentInput, cmd = m.commentInput.Update(msg)
	m.rebuildContent()
	return m, cmd, true
}

func (m Model) openCommentInput() (Model, tea.Cmd, bool) {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileDiffs) || idx >= len(m.files) {
		return m, nil, false
	}
	lines := m.fileDiffs[idx]
	if m.diffCursor >= len(lines) {
		return m, nil, false
	}
	dl := lines[m.diffCursor]
	if dl.Type == components.LineHunk {
		return m, nil, false
	}

	m.commentFile = m.files[idx].Filename
	m.commentStartLine = 0
	m.commentStartSide = ""

	if m.selectionAnchor >= 0 && m.selectionAnchor != m.diffCursor {
		selStart, selEnd := m.selectionAnchor, m.diffCursor
		if selStart > selEnd {
			selStart, selEnd = selEnd, selStart
		}
		startDL := lines[selStart]
		endDL := lines[selEnd]
		if startDL.Type == components.LineHunk || endDL.Type == components.LineHunk {
			return m, nil, false
		}
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
		if dl.Type == components.LineDel {
			m.commentLine = dl.OldLineNo
			m.commentSide = "LEFT"
		} else {
			m.commentLine = dl.NewLineNo
			m.commentSide = "RIGHT"
		}
	}

	// Check for existing thread to reply to.
	if m.commentStartLine > 0 {
		m.replyToID = ""
	} else {
		m.replyToID = m.commentStore.FindThreadRoot(m.commentFile, m.commentLine, m.commentSide)
	}

	ta := textarea.New()
	ta.SetWidth(m.contentWidth() - 10 - 6)
	ta.SetHeight(5)
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Focus()
	if m.replyToID != "" {
		ta.Placeholder = "Reply to thread..."
	} else {
		ta.Placeholder = "Add a comment..."
	}
	m.commentInput = ta
	m.composing = true
	m.rebuildContent()
	m.scrollCommentBoxIntoView()
	return m, ta.Focus(), true
}

func (m Model) insertCommentBox(rendered string, fileIdx int) string {
	lines := strings.Split(rendered, "\n")
	cursorRenderedLine := -1
	if fileIdx < len(m.fileDiffOffsets) && m.diffCursor < len(m.fileDiffOffsets[fileIdx]) {
		cursorRenderedLine = m.fileDiffOffsets[fileIdx][m.diffCursor]
	}
	if cursorRenderedLine < 0 || cursorRenderedLine >= len(lines) {
		return rendered
	}

	// When replying to a thread, find the end of the existing thread block
	// (the last line with a ╰ border character) so we insert right before
	// the thread's closing border.
	insertAt := cursorRenderedLine
	if m.replyToID != "" {
		// Scan forward from cursor line to find the thread's bottom border (╰).
		for i := cursorRenderedLine + 1; i < len(lines); i++ {
			if strings.Contains(lines[i], "╰") {
				insertAt = i
				break
			}
			// Stop if we hit another diff line (no gutter indent = not a comment).
			if i > cursorRenderedLine+200 {
				break
			}
		}
	}

	inputBox := m.renderCommentBox()
	inputLines := strings.Split(inputBox, "\n")
	after := make([]string, len(lines)-insertAt-1)
	copy(after, lines[insertAt+1:])
	lines = append(lines[:insertAt+1], inputLines...)
	lines = append(lines, after...)
	return strings.Join(lines, "\n")
}

func (m Model) renderCommentBox() string {
	gutter := components.TotalGutterWidth(components.GutterColWidth(m.fileDiffs[m.currentFileIdx]))
	indent := strings.Repeat(" ", gutter)
	boxW := m.contentWidth() - gutter - 2

	taView := m.commentInput.View()

	isReply := m.replyToID != ""
	bc := m.borderStyle()
	// Use highlighted border color for the reply box.
	hlStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.ctx.DiffColors.HighlightBorderFg))
	borderRender := bc.Render
	if isReply {
		borderRender = hlStyle.Render
	}

	var topLeft, bottomLeft string
	if isReply {
		topLeft = "├" // connects to thread above
		bottomLeft = "╰"
	} else {
		topLeft = "╭"
		bottomLeft = "╰"
	}

	topRule := borderRender(topLeft + strings.Repeat("─", boxW) + "╮")
	bottomRule := borderRender(bottomLeft + strings.Repeat("─", boxW) + "╯")
	side := borderRender("│")

	var boxLines []string

	// Label for reply.
	if isReply {
		label := dimStyle.Render(" replying...")
		fillW := boxW - lipgloss.Width(label)
		if fillW < 0 {
			fillW = 0
		}
		boxLines = append(boxLines, indent+topRule)
		boxLines = append(boxLines, indent+side+label+strings.Repeat(" ", fillW)+side)
	} else {
		boxLines = append(boxLines, indent+topRule)
	}

	for _, line := range strings.Split(taView, "\n") {
		visW := lipgloss.Width(line)
		padW := boxW - 2 - visW
		if padW < 0 {
			padW = 0
		}
		boxLines = append(boxLines, indent+side+" "+line+strings.Repeat(" ", padW)+" "+side)
	}
	boxLines = append(boxLines, indent+bottomRule)

	colors := m.ctx.DiffColors
	cancelBtn := lipgloss.NewStyle().
		Foreground(colors.PaletteDim).
		Padding(0, 1).
		Render("Cancel")
	submitBtn := lipgloss.NewStyle().
		Background(colors.PaletteGreen).
		Foreground(colors.PaletteBg).
		Bold(true).
		Padding(0, 1).
		Render("Submit")

	buttons := cancelBtn + " " + submitBtn
	hintGap := boxW - lipgloss.Width(buttons)
	if hintGap < 1 {
		hintGap = 1
	}
	boxLines = append(boxLines, indent+" "+strings.Repeat(" ", hintGap)+buttons)

	return strings.Join(boxLines, "\n")
}

// cursorThreadInfo returns the path/line/side for the comment thread at the cursor.
func (m Model) cursorThreadInfo() (path string, line int, side string, ok bool) {
	idx := m.currentFileIdx
	if idx >= len(m.fileDiffs) || m.diffCursor >= len(m.fileDiffs[idx]) {
		return
	}
	dl := m.fileDiffs[idx][m.diffCursor]
	if dl.Type == components.LineHunk {
		return
	}
	path = m.files[idx].Filename
	if dl.Type == components.LineDel {
		line = dl.OldLineNo
		side = "LEFT"
	} else {
		line = dl.NewLineNo
		side = "RIGHT"
	}
	ok = true
	return
}

// replyToThreadAtCursor opens a reply input for the thread at the cursor.
// Returns a tea.Cmd if a thread was found, nil otherwise.
func (m *Model) replyToThreadAtCursor() tea.Cmd {
	path, line, side, ok := m.cursorThreadInfo()
	if !ok {
		return nil
	}
	rootID := m.commentStore.FindThreadRoot(path, line, side)
	if rootID == "" {
		return nil
	}
	m.replyToID = rootID
	m.commentFile = path
	m.commentLine = line
	m.commentSide = side
	m.commentStartLine = 0
	m.commentStartSide = ""
	ta := textarea.New()
	ta.SetWidth(m.contentWidth() - 10 - 6)
	ta.SetHeight(5)
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Focus()
	ta.Placeholder = "Reply to thread..."
	m.commentInput = ta
	m.composing = true
	m.rebuildContent()
	// Scroll to ensure the comment box is visible.
	m.scrollCommentBoxIntoView()
	return ta.Focus()
}

// toggleResolveAtCursor resolves/unresolves the thread at the cursor.
// Returns true if a thread was found.
func (m *Model) toggleResolveAtCursor() bool {
	path, line, side, ok := m.cursorThreadInfo()
	if !ok {
		return false
	}
	rootID := m.commentStore.FindThreadRoot(path, line, side)
	if rootID == "" {
		return false
	}
	for _, c := range m.commentStore.Comments {
		if c.ID == rootID {
			m.commentStore.Resolve(rootID, !c.Resolved)
			break
		}
	}
	m.reformatAllFiles()
	m.rebuildContent()
	return true
}

// getDiffHunkForComment extracts the diff hunk around the commented line.
func (m Model) getDiffHunkForComment(c comments.LocalComment) string {
	// Find the file index.
	fileIdx := -1
	for i, f := range m.files {
		if f.Filename == c.Path {
			fileIdx = i
			break
		}
	}
	if fileIdx < 0 || fileIdx >= len(m.fileDiffs) {
		return ""
	}

	lines := m.fileDiffs[fileIdx]
	// Find the diff line that matches the comment.
	targetIdx := -1
	for i, dl := range lines {
		if c.Side == "LEFT" && dl.OldLineNo == c.Line {
			targetIdx = i
			break
		}
		if c.Side == "RIGHT" && dl.NewLineNo == c.Line {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return ""
	}

	// Extract surrounding context (up to 10 lines each direction).
	start := targetIdx - 10
	if start < 0 {
		start = 0
	}
	end := targetIdx + 10
	if end >= len(lines) {
		end = len(lines) - 1
	}

	var b strings.Builder
	for i := start; i <= end; i++ {
		dl := lines[i]
		switch dl.Type {
		case components.LineHunk:
			b.WriteString(dl.Content + "\n")
		case components.LineAdd:
			b.WriteString("+" + dl.Content + "\n")
		case components.LineDel:
			b.WriteString("-" + dl.Content + "\n")
		default:
			b.WriteString(" " + dl.Content + "\n")
		}
	}
	return b.String()
}

// getThreadHistory returns the bodies of all comments in a thread for context.
func (m Model) getThreadHistory(c comments.LocalComment) []string {
	if c.InReplyToID == "" {
		return nil
	}
	var history []string
	for _, existing := range m.commentStore.Comments {
		if existing.ID == c.InReplyToID || existing.InReplyToID == c.InReplyToID {
			if existing.ID != c.ID { // don't include the current comment
				prefix := existing.Author + ": "
				history = append(history, prefix+existing.Body)
			}
		}
	}
	return history
}

// getFullFileDiff returns the complete patch for a file.
// fileIndexForPath returns the index of the file with the given path, or -1.
func (m Model) buildViewPickerItems() []picker.Item {
	items := []picker.Item{
		{
			Label:       "Working Tree",
			Description: "Uncommitted changes vs HEAD",
			Value:       "working",
			Keywords:    []string{"local", "unstaged"},
		},
		{
			Label:       "Staged",
			Description: "Staged changes (git add)",
			Value:       "staged",
			Keywords:    []string{"cached", "index"},
		},
	}

	defaultBranch, _ := git.DefaultBranch(m.repoRoot)
	if m.branch != defaultBranch {
		items = append(items, picker.Item{
			Label:       "Branch Diff",
			Description: "vs " + defaultBranch,
			Value:       "branch",
			Keywords:    []string{"compare", "base"},
		})
	}

	if m.pr != nil {
		items = append(items, picker.Item{
			Label:       fmt.Sprintf("PR #%d", m.pr.Number),
			Description: m.pr.Title,
			Value:       "pr",
			Keywords:    []string{"pull request", "review"},
		})
	}

	return items
}

func (m Model) fileIndexForPath(path string) int {
	if idx, ok := m.filePathIndex[path]; ok {
		return idx
	}
	return -1
}

func (m *Model) rebuildFilePathIndex() {
	m.filePathIndex = make(map[string]int, len(m.files))
	for i, f := range m.files {
		m.filePathIndex[f.Filename] = i
	}
}

type stageDoneMsg struct{}

// stageWholeFile stages an entire file using the appropriate git command.
func (m Model) stageWholeFile(unstage bool) (Model, tea.Cmd, bool) {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.files) || idx >= len(m.fileDiffs) {
		return m, nil, false
	}
	filename := m.files[idx].Filename
	fileStatus := m.files[idx].Status
	repoRoot := m.repoRoot

	// Optimistically remove the file from the view.
	m.removeDiffLines(idx, 0, len(m.fileDiffs[idx])-1)
	m.stagingInFlight++

	return m, func() tea.Msg {
		if unstage {
			exec.Command("git", "-C", repoRoot, "reset", "HEAD", "--", filename).Run()
		} else if fileStatus == "removed" {
			// Stage a deletion.
			exec.Command("git", "-C", repoRoot, "rm", "--cached", "--", filename).Run()
		} else {
			exec.Command("git", "-C", repoRoot, "add", "--", filename).Run()
		}
		return stageDoneMsg{}
	}, true
}

// stageSelection stages or unstages the current line or J/K selection range.
func (m Model) stageSelection(unstage bool) (Model, tea.Cmd, bool) {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileDiffs) || idx >= len(m.files) {
		return m, nil, false
	}

	lines := m.fileDiffs[idx]
	filename := m.files[idx].Filename
	fileStatus := m.files[idx].Status
	patch := m.files[idx].Patch
	repoRoot := m.repoRoot

	// Determine selection range.
	selStart, selEnd := m.diffCursor, m.diffCursor
	if m.selectionAnchor >= 0 && m.selectionAnchor != m.diffCursor {
		selStart, selEnd = m.selectionAnchor, m.diffCursor
		if selStart > selEnd {
			selStart, selEnd = selEnd, selStart
		}
	}

	// Collect line numbers to stage.
	var newLineNos, oldLineNos []int
	for i := selStart; i <= selEnd; i++ {
		if i >= len(lines) {
			continue
		}
		dl := lines[i]
		switch dl.Type {
		case components.LineAdd:
			newLineNos = append(newLineNos, dl.NewLineNo)
		case components.LineDel:
			oldLineNos = append(oldLineNos, dl.OldLineNo)
		}
	}

	if len(newLineNos) == 0 && len(oldLineNos) == 0 {
		return m, nil, true
	}

	m.selectionAnchor = -1

	// Optimistically remove staged lines from the current diff view.
	m.removeDiffLines(idx, selStart, selEnd)
	m.stagingInFlight++

	return m, func() tea.Msg {
		git.StageLines(repoRoot, filename, fileStatus, patch, newLineNos, oldLineNos, unstage)
		return stageDoneMsg{}
	}, true
}

// stageHunk stages or unstages the entire hunk the cursor is in.
func (m Model) stageHunk(unstage bool) (Model, tea.Cmd, bool) {
	idx := m.currentFileIdx
	if idx < 0 || idx >= len(m.fileDiffs) || idx >= len(m.files) {
		return m, nil, false
	}

	lines := m.fileDiffs[idx]
	if m.diffCursor >= len(lines) {
		return m, nil, false
	}

	dl := lines[m.diffCursor]
	if dl.Type == components.LineHunk {
		return m, nil, false
	}

	filename := m.files[idx].Filename
	fileStatus := m.files[idx].Status
	patch := m.files[idx].Patch
	repoRoot := m.repoRoot

	var lineNo int
	var side string
	if dl.Type == components.LineDel {
		lineNo = dl.OldLineNo
		side = "LEFT"
	} else {
		lineNo = dl.NewLineNo
		side = "RIGHT"
	}

	// Optimistically remove the entire hunk from the view.
	hunkStart, hunkEnd := m.findHunkRange(idx, m.diffCursor)
	m.removeDiffLines(idx, hunkStart, hunkEnd)
	m.stagingInFlight++

	return m, func() tea.Msg {
		git.StageHunk(repoRoot, filename, fileStatus, patch, lineNo, side, unstage)
		return stageDoneMsg{}
	}, true
}

// removeDiffLines removes diff lines from the view optimistically.
// Additions are removed entirely; deletions become context lines.
// If no diff lines remain, the file is removed from the file list.
func (m *Model) removeDiffLines(fileIdx, start, end int) {
	if fileIdx >= len(m.fileDiffs) {
		return
	}
	lines := m.fileDiffs[fileIdx]
	var newLines []components.DiffLine
	for i, dl := range lines {
		if i >= start && i <= end {
			if dl.Type == components.LineAdd || dl.Type == components.LineDel {
				// Remove staged lines from the view entirely.
				continue
			}
		}
		newLines = append(newLines, dl)
	}

	// Check if any actual changes remain in this file.
	hasChanges := false
	for _, dl := range newLines {
		if dl.Type == components.LineAdd || dl.Type == components.LineDel {
			hasChanges = true
			break
		}
	}

	if !hasChanges {
		// Remove the file entirely from the view.
		m.files = append(m.files[:fileIdx], m.files[fileIdx+1:]...)
		m.fileDiffs = append(m.fileDiffs[:fileIdx], m.fileDiffs[fileIdx+1:]...)
		m.highlightedFiles = append(m.highlightedFiles[:fileIdx], m.highlightedFiles[fileIdx+1:]...)
		m.renderedFiles = append(m.renderedFiles[:fileIdx], m.renderedFiles[fileIdx+1:]...)
		m.fileDiffOffsets = append(m.fileDiffOffsets[:fileIdx], m.fileDiffOffsets[fileIdx+1:]...)
		m.fileCommentPositions = append(m.fileCommentPositions[:fileIdx], m.fileCommentPositions[fileIdx+1:]...)
		m.treeEntries = components.BuildFileTree(m.files)
		m.selectionAnchor = -1
		m.rebuildFilePathIndex()

		// Navigate away from the removed file.
		if len(m.files) == 0 {
			m.currentFileIdx = -1
			m.treeCursor = 0
			m.diffCursor = 0
		} else if m.currentFileIdx >= len(m.files) {
			m.currentFileIdx = len(m.files) - 1
			m.treeCursor = m.treeIndexForFile(m.currentFileIdx)
			m.diffCursor = m.firstNonHunkLine(m.currentFileIdx)
			m.formatFile(m.currentFileIdx)
		} else {
			m.treeCursor = m.treeIndexForFile(m.currentFileIdx)
			m.diffCursor = m.firstNonHunkLine(m.currentFileIdx)
			m.formatFile(m.currentFileIdx)
		}
		m.rebuildContent()
		return
	}

	m.fileDiffs[fileIdx] = newLines

	// Clamp cursor and skip hunk lines.
	if m.diffCursor >= len(newLines) {
		m.diffCursor = len(newLines) - 1
	}
	if m.diffCursor < 0 {
		m.diffCursor = 0
	}
	if len(newLines) > 0 && newLines[m.diffCursor].Type == components.LineHunk {
		m.diffCursor = m.firstNonHunkLine(fileIdx)
	}

	// Re-format to get correct rendered content and offsets.
	m.formatFile(fileIdx)
	m.rebuildContent()
}

// findHunkRange returns the start and end diff line indices for the hunk
// containing the given line index.
func (m Model) findHunkRange(fileIdx, lineIdx int) (start, end int) {
	lines := m.fileDiffs[fileIdx]

	// Find hunk start — scan backward for the @@ header.
	start = lineIdx
	for start > 0 && lines[start].Type != components.LineHunk {
		start--
	}
	// Skip the hunk header itself.
	if lines[start].Type == components.LineHunk {
		start++
	}

	// Find hunk end — scan forward to next @@ or end of file.
	end = lineIdx
	for end < len(lines)-1 {
		if lines[end+1].Type == components.LineHunk {
			break
		}
		end++
	}

	return start, end
}

func (m Model) getFullFileDiff(path string) string {
	for _, f := range m.files {
		if f.Filename == path {
			return f.Patch
		}
	}
	return ""
}

// splitDiffBorders splits a rendered diff line into border|inner|border parts.
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

	prefixEnd := firstIdx + len(borderChar)
	if prefixEnd < len(line) && line[prefixEnd] == '\033' {
		if i := strings.IndexByte(line[prefixEnd:], 'm'); i >= 0 {
			prefixEnd += i + 1
		}
	}

	suffixStart := lastIdx
	for i := lastIdx - 1; i >= prefixEnd; i-- {
		if line[i] == '\033' {
			suffixStart = i
			break
		}
	}

	return line[:prefixEnd], line[prefixEnd:suffixStart], line[suffixStart:]
}
