package localdiff

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blakewilliams/ghq/internal/git"
	"github.com/blakewilliams/ghq/internal/git/watcher"
	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/review/comments"
	"github.com/blakewilliams/ghq/internal/review/copilot"
	"github.com/blakewilliams/ghq/internal/ui/components"
	"github.com/blakewilliams/ghq/internal/ui/diffviewer"
	"github.com/blakewilliams/ghq/internal/ui/picker"
	"github.com/blakewilliams/ghq/internal/ui/styles"
	"github.com/blakewilliams/ghq/internal/ui/uictx"

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
type reviewCommentsRefreshMsg struct {
	Comments []github.ReviewComment
}
type reviewCommentsTimerMsg struct{}

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

// branchData holds state that is tied to the current branch and must be
// reset whenever the user switches branches.
type branchData struct {
	branch         string
	pr             *github.PullRequest   // nil if no PR for this branch
	prLoaded       bool                  // true once checked
	reviewComments []github.ReviewComment // GitHub review comments (refreshed every 2m)

	// Tracks which files have real Chroma highlighting (vs placeholder).
	chromaHighlighted map[int]bool

	// Per-file cursor memory (session only, not persisted).
	fileCursors map[string]int // filename -> diffCursor

	// Fast filename->index lookup (rebuilt on diff load).
	filePathIndex map[string]int
}

func newBranchData(branch string) branchData {
	return branchData{
		branch:            branch,
		chromaHighlighted: make(map[int]bool),
		fileCursors:       make(map[string]int),
		filePathIndex:     make(map[string]int),
	}
}

type Model struct {
	// Embedded diff viewer (shared with prdetail).
	dv  diffviewer.DiffViewer
	ctx *uictx.Context // alias for dv.Ctx — avoids m.dv.Ctx everywhere

	// Git state.
	repoRoot string
	branchData      branchData
	mode     git.DiffMode

	// Mode used for last highlight generation (to invalidate cache on mode change).
	lastHighlightMode git.DiffMode

	// Comments.
	commentStore *comments.CommentStore
	replyToID    string

	// File watcher.
	watcher *watcher.Watcher

	// Restore state from previous session.
	savedFilename string
	savedLineNo   int
	savedSide     string

	// Render cache.
	lastFormattedStreamLen int // length of copilot reply buffer at last formatFile

	// Staging ops counter.
	stagingInFlight int // number of staging ops in progress
}

func New(ctx *uictx.Context, repoRoot string, width, height int) Model {
	branch, _ := git.CurrentBranch(repoRoot)
	w, _ := watcher.New(repoRoot, nil)
	cp, _ := copilot.New(repoRoot)
	active := comments.LoadActiveState(repoRoot, branch)
	vs := comments.LoadViewState(repoRoot, branch, active.Mode)
	cs := comments.LoadComments(repoRoot)
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
		Copilot:         cp,
		CopilotReplyBuf: make(map[string]string),
		RenderBody:      renderMarkdownBody,
	}
	dv.InitSpinner()

	m := Model{
		ctx:           ctx,
		dv:            dv,
		repoRoot:      repoRoot,
		branchData:           newBranchData(branch),
		mode:          active.Mode,
		watcher:       w,
		commentStore:  cs,
		savedFilename: vs.Filename,
		savedLineNo:   vs.LineNo,
		savedSide:     vs.Side,
	}
	m.dv.Comments = commentStoreAdapter{store: cs}
	return m
}

func (m Model) BranchName() string              { return m.branchData.branch }
func (m Model) DiffMode() git.DiffMode          { return m.mode }
func (m Model) PR() *github.PullRequest         { return m.branchData.pr }
func (m Model) Files() []github.PullRequestFile { return m.dv.Files }

// CurrentFilename returns the filename currently being viewed, or "" if on overview.
func (m Model) CurrentFilename() string {
	if m.dv.CurrentFileIdx >= 0 && m.dv.CurrentFileIdx < len(m.dv.Files) {
		return m.dv.Files[m.dv.CurrentFileIdx].Filename
	}
	return ""
}

// restoreSavedPosition finds the saved file by name and restores cursor
// to the diff line matching the saved source line number.
func (m *Model) restoreSavedPosition() {
	for i, f := range m.dv.Files {
		if f.Filename == m.savedFilename {
			m.dv.CurrentFileIdx = i
			m.dv.DiffCursor = m.findDiffLineBySourceLine(i, m.savedLineNo, m.savedSide)
			// Set tree cursor to match the file.
			m.dv.Tree.Cursor = m.dv.Tree.IndexForFile(i)
			m.dv.Tree.Focused = false
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
	if fileIdx >= len(m.dv.FileDiffs) || lineNo == 0 {
		return 0
	}
	lines := m.dv.FileDiffs[fileIdx]
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

// saveViewState persists the current position for next session.
// Stores the source line number at the cursor (not diff index) so
// the position survives code changes that shift diff lines.
func (m Model) saveViewState() {
	var filename, side string
	var lineNo int
	if m.dv.CurrentFileIdx >= 0 && m.dv.CurrentFileIdx < len(m.dv.Files) {
		filename = m.dv.Files[m.dv.CurrentFileIdx].Filename
		if m.dv.CurrentFileIdx < len(m.dv.FileDiffs) && m.dv.DiffCursor < len(m.dv.FileDiffs[m.dv.CurrentFileIdx]) {
			dl := m.dv.FileDiffs[m.dv.CurrentFileIdx][m.dv.DiffCursor]
			if dl.Type == components.LineDel {
				lineNo = dl.OldLineNo
				side = "LEFT"
			} else {
				lineNo = dl.NewLineNo
				side = "RIGHT"
			}
		}
	}
	comments.SaveViewState(m.repoRoot, m.branchData.branch, m.mode, comments.ViewState{
		Filename: filename,
		LineNo:   lineNo,
		Side:     side,
	})
	comments.SaveActiveState(m.repoRoot, m.branchData.branch, comments.ActiveState{Mode: m.mode})
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.loadDiff()}
	if m.watcher != nil {
		cmds = append(cmds, m.watcher.WaitCmd())
		cmds = append(cmds, m.watcher.BranchWaitCmd())
	}
	if m.dv.Copilot != nil {
		cmds = append(cmds, m.dv.Copilot.ListenCmd())
	}
	// Auto-detect PR for this branch (uses internal msg type so app doesn't intercept).
	if !m.branchData.prLoaded {
		client := m.ctx.Client
		branch := m.branchData.branch
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
		m.dv.Width = msg.Width
		m.dv.Height = msg.Height
		m.dv.Tree.Height = msg.Height - 2
		// Width changed — layouts need recomputing.
		if m.dv.FilesListLoaded {
			m.dv.ReformatAllFiles()
		}
		m.rebuildContent()
		return m, nil

	case tea.MouseClickMsg:
		if msg.X < m.dv.Tree.Width {
			if idx, ok := m.dv.Tree.EntryIndexAtY(msg.Y); ok {
				if idx >= 0 && idx < len(m.dv.Tree.Entries) {
					e := m.dv.Tree.Entries[idx]
					if !e.IsDir && e.FileIndex >= 0 {
						m.dv.Tree.Cursor = idx
						cmd := m.selectTreeEntry()
						return m, cmd
					}
				}
			}
			return m, nil
		} else if m.dv.CurrentFileIdx >= 0 {
			if cursor := m.dv.DiffCursorFromScreenY(msg.Y); cursor >= 0 {
				m.dv.DiffCursor = cursor
			}
			return m, nil
		}

	case diffLoadedMsg:
		// Remember which file we're viewing by name (not index) so we
		// survive file list reordering when files are added/removed.
		var currentFilename string
		if m.dv.CurrentFileIdx >= 0 && m.dv.CurrentFileIdx < len(m.dv.Files) {
			currentFilename = m.dv.Files[m.dv.CurrentFileIdx].Filename
		}

		// Build index of old patches by filename for incremental updates.
		oldPatches := make(map[string]string, len(m.dv.Files))
		oldHighlights := make(map[string]components.HighlightedDiff)
		oldRendered := make(map[string]string)
		oldOffsets := make(map[string][]int)
		for i, f := range m.dv.Files {
			oldPatches[f.Filename] = f.Patch
			if i < len(m.dv.HighlightedFiles) && m.dv.HighlightedFiles[i].File.Filename != "" {
				oldHighlights[f.Filename] = m.dv.HighlightedFiles[i]
			}
			if i < len(m.dv.RenderedFiles) {
				oldRendered[f.Filename] = m.dv.RenderedFiles[i]
			}
			if i < len(m.dv.FileDiffOffsets) {
				oldOffsets[f.Filename] = m.dv.FileDiffOffsets[i]
			}
		}

		m.dv.Files = msg.files
		m.dv.HighlightedFiles = make([]components.HighlightedDiff, len(msg.files))
		m.dv.RenderedFiles = make([]string, len(msg.files))
		m.dv.FileDiffs = make([][]components.DiffLine, len(msg.files))
		m.dv.FileDiffOffsets = make([][]int, len(msg.files))
		m.dv.FileCommentPositions = make([][]components.CommentPosition, len(msg.files))
		m.dv.FileRenderLists = make([]*components.FileRenderList, len(msg.files))
		m.rebuildFilePathIndex()
		m.dv.FilesListLoaded = true

		// Reuse cached highlights for files whose patch hasn't changed AND mode is the same.
		// Mode change requires re-highlighting because branch mode reads from HEAD commit
		// while working/staged modes read from the working tree.
		modeChanged := m.lastHighlightMode != m.mode
		var needHighlight []int
		for i, f := range msg.files {
			m.dv.FileDiffs[i] = components.ParsePatchLines(f.Patch)
			if !modeChanged {
				if old, ok := oldPatches[f.Filename]; ok && old == f.Patch {
					if hl, ok := oldHighlights[f.Filename]; ok {
						m.dv.HighlightedFiles[i] = hl
						m.dv.RenderedFiles[i] = oldRendered[f.Filename]
						m.dv.FileDiffOffsets[i] = oldOffsets[f.Filename]
						continue
					}
				}
			}
			// Keep stale rendered content so the viewport doesn't flash
			// a skeleton while the new highlight is in progress.
			// But DON'T copy old offsets when mode changed - they're wrong.
			if !modeChanged {
				if rendered, ok := oldRendered[f.Filename]; ok {
					m.dv.RenderedFiles[i] = rendered
					m.dv.FileDiffOffsets[i] = oldOffsets[f.Filename]
				}
			}
			needHighlight = append(needHighlight, i)
		}

		m.dv.FilesHighlighted = len(msg.files) - len(needHighlight)
		m.dv.Tree.SetFiles(m.dv.Files)

		// Update watcher to cover directories with changed files.
		if m.watcher != nil {
			var filenames []string
			for _, f := range msg.files {
				filenames = append(filenames, f.Filename)
			}
			m.watcher.UpdateDirs(watcher.DirsFromFiles(filenames))
		}

		// Restore saved position from previous session.
		savedOffset := m.dv.VP.YOffset()
		if m.savedFilename != "" {
			m.restoreSavedPosition()
		} else if currentFilename != "" {
			// Re-resolve the file index by name after reload.
			if idx, ok := m.branchData.filePathIndex[currentFilename]; ok {
				m.dv.CurrentFileIdx = idx
				m.dv.Tree.Cursor = m.dv.Tree.IndexForFile(idx)
			} else {
				// File was removed from the diff.
				m.dv.CurrentFileIdx = -1
				m.dv.Tree.Cursor = 0
			}
		} else if m.dv.CurrentFileIdx >= len(m.dv.Files) {
			m.dv.CurrentFileIdx = -1
			m.dv.Tree.Cursor = 0
		} else if m.dv.CurrentFileIdx >= 0 {
			// Sync tree cursor with current file after SetFiles reset it.
			m.dv.Tree.Cursor = m.dv.Tree.IndexForFile(m.dv.CurrentFileIdx)
		}

		// Only re-format the current file if it kept its highlights.
		// Other files will be formatted lazily when navigated to.
		if m.dv.CurrentFileIdx >= 0 && m.dv.CurrentFileIdx < len(msg.files) {
			f := msg.files[m.dv.CurrentFileIdx]
			if _, ok := oldHighlights[f.Filename]; ok && oldPatches[f.Filename] == f.Patch {
				m.formatFile(m.dv.CurrentFileIdx)
			}
		}

		m.rebuildContentIfChanged()
		// Preserve scroll position on file-watcher reloads (not initial load).
		if m.savedFilename == "" && savedOffset > 0 {
			m.dv.VP.SetYOffset(savedOffset)
		}

		// Only highlight files that actually changed.
		if len(needHighlight) > 0 {
			// Prioritize the current file if it needs highlighting.
			for i, idx := range needHighlight {
				if idx == m.dv.CurrentFileIdx {
					needHighlight[0], needHighlight[i] = needHighlight[i], needHighlight[0]
					break
				}
			}
			m.lastHighlightMode = m.mode // track mode for cache invalidation
			cmds := []tea.Cmd{m.highlightFileCmd(needHighlight[0])}
			// Show spinner if the current file is being highlighted.
			if needHighlight[0] == m.dv.CurrentFileIdx {
				m.dv.SpinnerActive = true
				cmds = append(cmds, m.dv.Spinner.Tick)
			}
			return m, tea.Batch(cmds...)
		}
		m.lastHighlightMode = m.mode // track mode even if no files needed highlighting
		m.dv.FilesLoading = false
		return m, nil

	case diffErrorMsg:
		return m, nil

	case SwitchModeMsg:
		m.saveViewState()
		m.mode = msg.Mode
		m.dv.FilesListLoaded = false
		m.dv.FilesHighlighted = 0
		m.dv.FilesLoading = true
		m.dv.CurrentFileIdx = -1
		m.dv.Tree.Cursor = 0
		m.branchData.fileCursors = make(map[string]int)
		vs := comments.LoadViewState(m.repoRoot, m.branchData.branch, m.mode)
		m.savedFilename = vs.Filename
		m.savedLineNo = vs.LineNo
		m.savedSide = vs.Side
		return m, m.loadDiff()

	case prDetectedMsg:
		m.branchData.pr = &msg.PR
		m.branchData.prLoaded = true
		return m, tea.Batch(m.fetchReviewComments(), m.reviewCommentsTimer())

	case prDetectFailedMsg:
		m.branchData.prLoaded = true
		return m, nil

	case reviewCommentsRefreshMsg:
		m.branchData.reviewComments = msg.Comments
		m.dv.Comments = commentStoreAdapter{store: m.commentStore, reviewComments: m.branchData.reviewComments}
		// Re-format visible files to include new comments.
		if m.dv.FilesHighlighted > 0 {
			for i := 0; i < len(m.dv.Files); i++ {
				if m.dv.FileRenderLists[i] != nil {
					m.dv.FormatFile(i)
				}
			}
			m.dv.RebuildContentIfChanged(m.buildOverviewContent, m.buildFileContent)
		}
		// Re-arm the timer if we still have a PR.
		if m.branchData.pr != nil {
			return m, m.reviewCommentsTimer()
		}
		return m, nil

	case reviewCommentsTimerMsg:
		return m, m.fetchReviewComments()

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

	case watcher.BranchChangedMsg:
		if msg.Branch != "" && msg.Branch != m.branchData.branch {
			m.branchData = newBranchData(msg.Branch)

			cmds := []tea.Cmd{m.loadDiff()}

			// Re-detect PR for new branch.
			client := m.ctx.Client
			branch := m.branchData.branch
			cmds = append(cmds, func() tea.Msg {
				pr, err := client.FetchPRByBranch(m.ctx.Owner, m.ctx.Repo, branch)
				if err != nil {
					return prDetectFailedMsg{}
				}
				return prDetectedMsg{PR: pr}
			})
			if m.watcher != nil {
				cmds = append(cmds, m.watcher.BranchWaitCmd())
			}
			return m, tea.Batch(cmds...)
		}
		// Same branch or detached HEAD — just re-arm.
		if m.watcher != nil {
			return m, m.watcher.BranchWaitCmd()
		}
		return m, nil

	case spinner.TickMsg:
		if m.dv.SpinnerActive {
			var cmd tea.Cmd
			m.dv.Spinner, cmd = m.dv.Spinner.Update(msg)
			m.rebuildContentIfChanged()
			return m, cmd
		}
		return m, nil

	case fileHighlightedMsg:
		if msg.index >= len(m.dv.HighlightedFiles) {
			return m, nil
		}
		m.dv.HighlightedFiles[msg.index] = msg.highlight
		m.dv.FilesHighlighted++
		m.branchData.chromaHighlighted[msg.index] = true
		// Stop spinner if this is the file we were waiting on.
		if msg.index == m.dv.CurrentFileIdx {
			m.dv.SpinnerActive = false
		}
		// Re-render with real highlights if this is the current file.
		if msg.index == m.dv.CurrentFileIdx {
			m.dv.RenderedFiles[msg.index] = "" // invalidate to force re-render with highlights
			m.formatFile(msg.index)
			m.rebuildContent()
		} else {
			m.dv.RenderedFiles[msg.index] = "" // invalidate so next access uses highlights
		}
		// Find the next file that needs Chroma highlighting.
		for next := msg.index + 1; next < len(m.dv.Files); next++ {
			if !m.branchData.chromaHighlighted[next] {
				return m, m.highlightFileCmd(next)
			}
		}
		m.dv.FilesLoading = false
		return m, nil

	case copilot.ReplyMsg:
		m.dv.CopilotReplyBuf[msg.CommentID] += msg.Content
		if msg.Done {
			body := m.dv.CopilotReplyBuf[msg.CommentID]
			delete(m.dv.CopilotReplyBuf, msg.CommentID)
			pendingPath := m.dv.CopilotPendingPath(msg.CommentID)
			m.dv.ClearCopilotPending(msg.CommentID)
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
			if fileIdx := m.fileIndexForPath(pendingPath); fileIdx >= 0 {
				m.formatFile(fileIdx)
				if fileIdx == m.dv.CurrentFileIdx {
					m.rebuildContent()
				}
			}
		} else if fileIdx := m.fileIndexForPath(m.dv.CopilotPendingPath(msg.CommentID)); fileIdx >= 0 && fileIdx == m.dv.CurrentFileIdx {
			m.formatFile(fileIdx)
			m.rebuildContent()
		}
		cmds := []tea.Cmd{m.dv.Copilot.ListenCmd()}
		if m.dv.HasCopilotPending() {
			cmds = append(cmds, tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
				return copilotTickMsg{}
			}))
		}
		return m, tea.Batch(cmds...)

	case copilot.ErrorMsg:
		pendingPath := m.dv.CopilotPendingPath(msg.CommentID)
		m.dv.ClearCopilotPending(msg.CommentID)
		if fileIdx := m.fileIndexForPath(pendingPath); fileIdx >= 0 {
			m.formatFile(fileIdx)
		}
		m.rebuildContent()
		cmds := []tea.Cmd{}
		if m.dv.Copilot != nil {
			cmds = append(cmds, m.dv.Copilot.ListenCmd())
		}
		return m, tea.Batch(cmds...)

	case copilot.ToolMsg:
		if m.dv.CopilotToolState == nil {
			m.dv.CopilotToolState = make(map[string]string)
		}
		if msg.Done {
			delete(m.dv.CopilotToolState, msg.CommentID)
		} else {
			m.dv.CopilotToolState[msg.CommentID] = msg.Name
		}
		if fileIdx := m.fileIndexForPath(m.dv.CopilotPendingPath(msg.CommentID)); fileIdx >= 0 && fileIdx == m.dv.CurrentFileIdx {
			m.formatFile(fileIdx)
			m.rebuildContent()
		}
		if m.dv.Copilot != nil {
			return m, m.dv.Copilot.ListenCmd()
		}
		return m, nil

	case copilotTickMsg:
		if !m.dv.HasCopilotPending() {
			return m, nil
		}
		m.dv.CopilotDots = (m.dv.CopilotDots + 1) % 4
		// Splice pending threads in the current file (O(thread), not O(n)).
		for commentID, info := range m.dv.CopilotPending {
			if fileIdx := m.fileIndexForPath(info.Path); fileIdx >= 0 && fileIdx == m.dv.CurrentFileIdx {
				m.spliceThreadForComment(fileIdx, commentID)
			}
		}
		m.rebuildContentIfChanged()
		return m, tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
			return copilotTickMsg{}
		})

	case GoToLineMsg:
		if m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
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
	if m.dv.Composing {
		var cmd tea.Cmd
		m.dv.CommentInput, cmd = m.dv.CommentInput.Update(msg)
		return m, cmd
	}

	// Viewport updates.
	if m.dv.VPReady {
		prevOffset := m.dv.VP.YOffset()
		var cmd tea.Cmd
		m.dv.VP, cmd = m.dv.VP.Update(msg)
		if m.dv.VP.YOffset() != prevOffset && m.dv.CurrentFileIdx >= 0 {
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

	// Thread navigation mode.
	if m.dv.ThreadCursor > 0 {
		switch msg.String() {
		case "j", "down":
			// Scroll within a long comment before moving to the next one.
			if m.commentExtendsBelow() {
				m.dv.VP.SetYOffset(m.dv.VP.YOffset() + 1)
				return m, nil, true
			}
			count := m.threadCommentCount()
			if m.dv.ThreadCursor < count {
				m.dv.ThreadCursor++
				m.updateThreadHighlight()
				m.rebuildContent()
				m.scrollToThreadCursor()
			}
			return m, nil, true
		case "k", "up":
			// Scroll within a long comment before moving to the previous one.
			if m.commentExtendsAbove() {
				m.dv.VP.SetYOffset(m.dv.VP.YOffset() - 1)
				return m, nil, true
			}
			if m.dv.ThreadCursor > 1 {
				m.dv.ThreadCursor--
				m.updateThreadHighlight()
				m.rebuildContent()
				m.scrollToThreadCursorBottom()
			} else {
				m.updateThreadHighlight() // remove highlight before exiting
				m.dv.ThreadCursor = 0
				m.rebuildContent()
				m.dv.ScrollToDiffCursor()
			}
			return m, nil, true
		case "ctrl+d":
			m.dv.VP.SetYOffset(m.dv.VP.YOffset() + m.dv.Height/2)
			return m, nil, true
		case "ctrl+u":
			m.dv.VP.SetYOffset(m.dv.VP.YOffset() - m.dv.Height/2)
			return m, nil, true
		case "esc":
			m.updateThreadHighlight() // remove highlight
			m.dv.ThreadCursor = 0
			m.rebuildContent()
			m.dv.ScrollToDiffCursor()
			return m, nil, true
		case "r":
			if cmd := m.replyToThreadAtCursor(); cmd != nil {
				m.dv.ThreadCursor = 0
				return m, cmd, true
			}
			return m, nil, true
		case "x":
			m.toggleResolveAtCursor()
			m.dv.ThreadCursor = 0
			return m, nil, true
		case "enter":
			m.updateThreadHighlight() // remove highlight
			m.dv.ThreadCursor = 0
			m.rebuildContent()
			m.dv.ScrollToDiffCursor()
			return m, nil, true
		}
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
	case "m":
		// Cycle diff mode: Working → Staged → Branch (skip Branch on default branch).
		m.saveViewState()
		defaultBranch, _ := git.DefaultBranch(m.repoRoot)
		if m.branchData.branch == defaultBranch {
			if m.mode == git.DiffWorking {
				m.mode = git.DiffStaged
			} else {
				m.mode = git.DiffWorking
			}
		} else {
			m.mode = (m.mode + 1) % 3
		}
		m.dv.FilesListLoaded = false
		m.dv.FilesHighlighted = 0
		m.dv.FilesLoading = true
		m.dv.CurrentFileIdx = -1
		m.dv.Tree.Cursor = 0
		vs := comments.LoadViewState(m.repoRoot, m.branchData.branch, m.mode)
		m.savedFilename = vs.Filename
		m.savedLineNo = vs.LineNo
		m.savedSide = vs.Side
		return m, m.loadDiff(), true
	case "ctrl+k":
		m.dv.Tree.MoveSelection(-1)
		cmd := m.selectTreeEntry()
		return m, cmd, true
	case "ctrl+j":
		m.dv.Tree.MoveSelection(1)
		cmd := m.selectTreeEntry()
		return m, cmd, true
	case "j", "down", "k", "up", "J", "shift+down", "K", "shift+up":
		if m.dv.HandleNavKey(msg.String()) == diffviewer.KeyHandled {
			return m, nil, true
		}
	case "enter":
		if m.dv.Tree.Focused {
			cmd := m.selectTreeEntry()
			m.dv.Tree.Focused = false
			return m, cmd, true
		}
		// If inside a thread, exit thread mode.
		if m.dv.ThreadCursor > 0 {
			m.updateThreadHighlight() // remove highlight
			m.dv.ThreadCursor = 0
			m.rebuildContent()
			m.dv.ScrollToDiffCursor()
			return m, nil, true
		}
		// If on a line with comments, enter thread navigation.
		if m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() && m.cursorHasThread() {
			m.dv.ThreadCursor = 1
			m.updateThreadHighlight() // add highlight
			m.rebuildContent()
			m.scrollToThreadCursor()
			return m, nil, true
		}
		// Otherwise open comment input.
		if m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
			return m.openCommentInput()
		}
	case "a":
		if m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
			return m.openCommentInput()
		}
	case "r":
		// Reply to comment thread on current line.
		if !m.dv.Tree.Focused && m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
			if cmd := m.replyToThreadAtCursor(); cmd != nil {
				return m, cmd, true
			}
		}
	case "x":
		// Resolve/unresolve comment thread on current line.
		if !m.dv.Tree.Focused && m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
			if m.toggleResolveAtCursor() {
				return m, nil, true
			}
		}
	case "s":
		if m.mode == git.DiffWorking {
			if m.dv.Tree.Focused {
				// Stage the whole file under the tree cursor.
				if fileIdx := m.dv.Tree.FileIndex(); fileIdx >= 0 {
					m.dv.CurrentFileIdx = fileIdx
					return m.stageWholeFile(false)
				}
			} else if m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
				status := m.dv.Files[m.dv.CurrentFileIdx].Status
				if status == "removed" || status == "renamed" {
					return m.stageWholeFile(false)
				}
				return m.stageSelection(false)
			}
		}
	case "u":
		if m.mode == git.DiffStaged {
			if m.dv.Tree.Focused {
				if fileIdx := m.dv.Tree.FileIndex(); fileIdx >= 0 {
					m.dv.CurrentFileIdx = fileIdx
					return m.stageWholeFile(true)
				}
			} else if m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
				return m.stageSelection(true)
			}
		}
	case "S":
		if m.mode == git.DiffWorking {
			if m.dv.Tree.Focused {
				if fileIdx := m.dv.Tree.FileIndex(); fileIdx >= 0 {
					m.dv.CurrentFileIdx = fileIdx
					return m.stageWholeFile(false)
				}
			} else if m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
				status := m.dv.Files[m.dv.CurrentFileIdx].Status
				if status == "removed" || status == "renamed" {
					return m.stageWholeFile(false)
				}
				return m.stageHunk(false)
			}
		}
	case "U":
		if m.mode == git.DiffStaged {
			if m.dv.Tree.Focused {
				if fileIdx := m.dv.Tree.FileIndex(); fileIdx >= 0 {
					m.dv.CurrentFileIdx = fileIdx
					return m.stageWholeFile(true)
				}
			} else if m.dv.CurrentFileIdx >= 0 && m.dv.HasDiffLines() {
				return m.stageHunk(true)
			}
		}
	case "ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b", "G", "g":
		if m.dv.HandleNavKey(msg.String()) == diffviewer.KeyHandled {
			return m, nil, true
		}
	default:
		m.dv.WaitingG = false
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
	if m.dv.Composing {
		left = append(left, styles.StatusBarKey.Render("esc")+" "+styles.StatusBarHint.Render("cancel"))
		right = append(right, styles.StatusBarKey.Render("enter")+" "+styles.StatusBarHint.Render("submit"))
		return
	}
	if m.dv.Tree.Focused {
		left = append(left, styles.StatusBarKey.Render("f")+" "+styles.StatusBarHint.Render("unfocus tree"))
	} else {
		left = append(left, styles.StatusBarKey.Render("f")+" "+styles.StatusBarHint.Render("focus tree"))
	}
	left = append(left, styles.StatusBarKey.Render("h/l")+" "+styles.StatusBarHint.Render("panes"))
	left = append(left, styles.StatusBarKey.Render("ctrl+j/k")+" "+styles.StatusBarHint.Render("files"))
	if !m.dv.Tree.Focused && m.dv.CurrentFileIdx >= 0 {
		left = append(left, styles.StatusBarKey.Render("J/K")+" "+styles.StatusBarHint.Render("select range"))
		if m.dv.ThreadCursor > 0 {
			count := m.threadCommentCount()
			left = append(left, styles.StatusBarHint.Render(fmt.Sprintf("comment %d/%d", m.dv.ThreadCursor, count)))
			left = append(left, styles.StatusBarKey.Render("r")+" "+styles.StatusBarHint.Render("reply"))
			left = append(left, styles.StatusBarKey.Render("x")+" "+styles.StatusBarHint.Render("resolve"))
		} else if m.cursorHasThread() {
			left = append(left, styles.StatusBarKey.Render("r")+" "+styles.StatusBarHint.Render("reply"))
			left = append(left, styles.StatusBarKey.Render("x")+" "+styles.StatusBarHint.Render("resolve"))
		}
	}
	modeStr := m.mode.String()
	if m.branchData.pr != nil {
		modeStr += fmt.Sprintf(" · PR #%d", m.branchData.pr.Number)
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
	if !m.dv.VPReady {
		return ""
	}
	rightView := m.dv.VP.View()
	if m.dv.CurrentFileIdx >= 0 {
		rightView = m.dv.OverlayDiffCursor(rightView)
	}
	return m.renderLayout(rightView)
}

func (m Model) renderLayout(rightView string) string {
	var rightTitle string
	if m.dv.CurrentFileIdx >= 0 && m.dv.CurrentFileIdx < len(m.dv.Files) {
		rightTitle = m.dv.Files[m.dv.CurrentFileIdx].Filename
	} else {
		rightTitle = "Overview"
	}
	info := diffviewer.LayoutInfo{
		ModeName:   m.mode.String(),
		ModeColor:  styles.ModeColor(m.mode),
		BranchName: m.branchData.branch,
		PR:         m.branchData.pr,
	}
	return m.dv.RenderLayout(rightView, rightTitle, info)
}

// --- Content building ---

func (m *Model) rebuildContent() {
	m.dv.RebuildContent(m.buildOverviewContent, m.buildFileContent)
}

func (m *Model) rebuildContentIfChanged() {
	m.dv.RebuildContentIfChanged(m.buildOverviewContent, m.buildFileContent)
}

func (m Model) buildOverviewContent(w int) string {
	var content strings.Builder

	if len(m.dv.Files) == 0 {
		if m.dv.FilesListLoaded {
			content.WriteString("\n  " + dimStyle.Render("No changes") + "\n")
		} else {
			content.WriteString("\n  " + dimStyle.Render("Loading...") + "\n")
		}
		return content.String()
	}

	// Stats summary with colored +/-.
	adds, dels := git.FilesAddedDeletedStats(m.dv.Files)
	var statParts []string
	statParts = append(statParts, fmt.Sprintf("%d files", len(m.dv.Files)))
	if adds > 0 {
		statParts = append(statParts,
			lipgloss.NewStyle().Foreground(lipgloss.Green).Render(fmt.Sprintf("+%d", adds)))
	}
	if dels > 0 {
		statParts = append(statParts,
			lipgloss.NewStyle().Foreground(lipgloss.Red).Render(fmt.Sprintf("-%d", dels)))
	}
	content.WriteString("\n  " + strings.Join(statParts, "  ") + "\n")

	// File list.
	content.WriteString("\n")
	for _, f := range m.dv.Files {
		var icon string
		switch f.Status {
		case "added":
			icon = lipgloss.NewStyle().Foreground(lipgloss.Green).Render("+")
		case "removed":
			icon = lipgloss.NewStyle().Foreground(lipgloss.Red).Render("-")
		case "renamed":
			icon = lipgloss.NewStyle().Foreground(lipgloss.Yellow).Render("→")
		default:
			icon = lipgloss.NewStyle().Foreground(lipgloss.Blue).Render("~")
		}
		content.WriteString("  " + icon + " " + f.Filename + "\n")
	}
	content.WriteString("\n")

	return content.String()
}

func (m *Model) buildFileContent(w int) string {
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.Files) {
		return ""
	}

	return m.buildVirtualFileContent(idx, w)
}

func (m *Model) buildVirtualFileContent(idx, w int) string {
	rendered := m.dv.RenderedFiles[idx]
	if rendered == "" {
		// If the file hasn't been highlighted yet, show a spinner while
		// the async highlight goroutine finishes.
		if idx < len(m.dv.HighlightedFiles) && m.dv.HighlightedFiles[idx].File.Filename == "" {
			return m.dv.SpinnerView()
		}
		m.dv.FormatFile(idx)
		rendered = m.dv.RenderedFiles[idx]
	}

	if m.dv.Composing && m.dv.HasDiffLines() {
		rendered = m.insertCommentBox(rendered, idx)
	}

	return rendered + "\n" + strings.Repeat("\n", m.dv.Height/2)
}

func stripAnsi(s string) string {
	// Simple ANSI stripper
	result := make([]byte, 0, len(s))
	inEscape := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z') {
				inEscape = false
			}
			continue
		}
		result = append(result, s[i])
	}
	return string(result)
}

// spliceThreadForComment delegates to DiffViewer's splice method.
func (m *Model) spliceThreadForComment(fileIdx int, commentID string) {
	info, ok := m.dv.CopilotPending[commentID]
	if !ok {
		m.dv.FormatFile(fileIdx)
		return
	}
	m.dv.SpliceThreadForComment(fileIdx, info.Side, info.Line)
}

// --- File rendering pipeline ---

func (m Model) highlightFileCmd(index int) tea.Cmd {
	f := m.dv.Files[index]
	repoRoot := m.repoRoot
	chromaStyle := m.ctx.DiffColors.ChromaStyle
	mode := m.mode

	return func() tea.Msg {
		var fileContent, oldFileContent string

		// Get new file content for added/modified files.
		if f.Status != "removed" && f.Patch != "" {
			if mode == git.DiffBranch {
				// Branch mode diff is merge-base..HEAD (committed), so use committed HEAD.
				if content, err := git.FileContentAtRef(repoRoot, f.Filename, "HEAD"); err == nil {
					fileContent = content
				}
			} else {
				// Working/Staged mode: use working tree
				if content, err := git.FileContent(repoRoot, f.Filename); err == nil {
					fileContent = content
				}
			}
		}

		// Get old file content for deleted/modified files.
		if f.Status != "added" && f.Patch != "" {
			var ref string
			switch mode {
			case git.DiffWorking, git.DiffStaged:
				ref = "HEAD"
			case git.DiffBranch:
				// For branch mode, the "old" side is the merge-base commit.
				defaultBranch, _ := git.DefaultBranch(repoRoot)
				if mb, err := git.MergeBase(repoRoot, defaultBranch); err == nil {
					ref = mb
				} else {
					ref = "HEAD"
				}
			default:
				ref = "HEAD"
			}
			if content, err := git.FileContentAtRef(repoRoot, f.Filename, ref); err == nil {
				oldFileContent = content
			}
		}

		hl := components.HighlightDiffFile(f, fileContent, oldFileContent, chromaStyle)
		return fileHighlightedMsg{highlight: hl, index: index}
	}
}

func (m *Model) formatFile(index int) {
	m.dv.FormatFile(index)
}

// updateThreadHighlight re-renders just the thread at the cursor with updated highlighting.
// Much faster than formatFile for large files since it's O(thread) not O(file).
func (m *Model) updateThreadHighlight() {
	_, line, side, ok := m.cursorThreadInfo()
	if !ok {
		return
	}
	m.dv.SpliceThreadWithHighlight(m.dv.CurrentFileIdx, side, line, m.dv.ThreadCursor > 0, m.dv.ThreadCursor)
}

// commentStoreAdapter adapts a CommentStore + GitHub review comments to the CommentSource interface.
type commentStoreAdapter struct {
	store          *comments.CommentStore
	reviewComments []github.ReviewComment
}

func (a commentStoreAdapter) CommentsForFile(filename string) []github.ReviewComment {
	var result []github.ReviewComment
	if a.store != nil {
		result = append(result, a.store.ForFile(filename)...)
	}
	for _, c := range a.reviewComments {
		if c.Path == filename {
			result = append(result, c)
		}
	}
	return result
}

// commentsForFile returns comments for a file by index (convenience).
func (m Model) commentsForFile(fileIdx int) []github.ReviewComment {
	if fileIdx < 0 || fileIdx >= len(m.dv.Files) {
		return nil
	}
	return m.dv.CommentsForFile(m.dv.Files[fileIdx].Filename)
}

// fetchReviewComments fetches GitHub review comments for the detected PR.
func (m Model) fetchReviewComments() tea.Cmd {
	if m.branchData.pr == nil {
		return nil
	}
	client := m.ctx.Client
	owner, repo, number := m.ctx.Owner, m.ctx.Repo, m.branchData.pr.Number
	return func() tea.Msg {
		data, found, refetch := client.GetReviewComments(owner, repo, number)
		if refetch != nil {
			result, err := refetch()
			if err == nil {
				return reviewCommentsRefreshMsg{Comments: result}
			}
		}
		if found {
			return reviewCommentsRefreshMsg{Comments: data}
		}
		return nil
	}
}

// reviewCommentsTimer returns a command that fires after 2 minutes to re-fetch comments.
func (m Model) reviewCommentsTimer() tea.Cmd {
	return tea.Tick(2*time.Minute, func(time.Time) tea.Msg {
		return reviewCommentsTimerMsg{}
	})
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

// needsRenderBufferUpdate returns true if the viewport scrolled outside
// the pre-rendered buffer and needs a fresh render.

func (m *Model) selectTreeEntry() tea.Cmd {
	m.dv.SelectionAnchor = -1
	// Save cursor position for the file we're leaving.
	if m.dv.CurrentFileIdx >= 0 && m.dv.CurrentFileIdx < len(m.dv.Files) {
		m.branchData.fileCursors[m.dv.Files[m.dv.CurrentFileIdx].Filename] = m.dv.DiffCursor
	}
	m.dv.ThreadCursor = 0
	fileIdx := m.dv.Tree.FileIndex()
	if fileIdx < 0 || fileIdx >= len(m.dv.Files) || fileIdx >= len(m.dv.FileDiffs) {
		m.dv.SpinnerActive = false
		m.dv.CurrentFileIdx = -1
		m.rebuildContent()
		m.dv.VP.GotoTop()
		m.saveViewState()
		return nil
	}
	m.dv.CurrentFileIdx = fileIdx
	if saved, ok := m.branchData.fileCursors[m.dv.Files[fileIdx].Filename]; ok && saved < len(m.dv.FileDiffs[fileIdx]) {
		m.dv.DiffCursor = saved
	} else {
		m.dv.DiffCursor = m.dv.FirstNonHunkLine(fileIdx)
	}
	// If the file hasn't been highlighted yet, show a spinner
	// and kick off highlighting for this file directly (the
	// sequential chain may have already passed this index).
	needsHighlight := fileIdx < len(m.dv.HighlightedFiles) && m.dv.HighlightedFiles[fileIdx].File.Filename == ""
	if needsHighlight {
		m.dv.SpinnerActive = true
		m.rebuildContent()
		m.saveViewState()
		return tea.Batch(m.dv.Spinner.Tick, m.highlightFileCmd(fileIdx))
	}
	m.dv.SpinnerActive = false
	if m.dv.RenderedFiles[fileIdx] == "" {
		m.formatFile(fileIdx)
	}
	m.rebuildContent()
	m.dv.ScrollToDiffCursor()
	m.saveViewState()
	return nil
}

// lineHasComments returns true if the diff line at the given index has a comment thread.
func (m Model) lineHasComments(diffIdx int) bool {
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.FileDiffs) || diffIdx < 0 || diffIdx >= len(m.dv.FileDiffs[idx]) {
		return false
	}
	dl := m.dv.FileDiffs[idx][diffIdx]
	if dl.Type == components.LineHunk {
		return false
	}
	path := m.dv.Files[idx].Filename
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

// goToSourceLine jumps the diff cursor to the line closest to the given
// source line number (new side preferred, falls back to old side).
func (m *Model) goToSourceLine(lineNo int) {
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.FileDiffs) {
		return
	}
	best := m.findDiffLineBySourceLine(idx, lineNo, "RIGHT")
	m.dv.DiffCursor = best
	m.dv.SelectionAnchor = -1
	// Only format if not already rendered.
	if idx < len(m.dv.RenderedFiles) && m.dv.RenderedFiles[idx] == "" {
		m.formatFile(idx)
	}
	m.rebuildContent()
	m.dv.ScrollToDiffCursor()
}

// threadCommentCount returns the number of comments in the thread on the
// current cursor line, consistent with what's actually rendered.
func (m Model) threadCommentCount() int {
	path, line, side, ok := m.cursorThreadInfo()
	if !ok {
		return 0
	}
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.FileCommentPositions) {
		return 0
	}
	// Count from the rendered comment positions — this is the source of truth.
	count := 0
	for _, cp := range m.dv.FileCommentPositions[idx] {
		if cp.Line == line && cp.Side == side {
			count++
		}
	}
	_ = path
	return count
}

const scrollMargin = 5

// scrollToThreadCursor scrolls the viewport to show the selected comment
// using the exact rendered positions tracked by CommentPositions.
func (m *Model) scrollToThreadCursor() {
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.FileCommentPositions) {
		return
	}

	// Find the comment position matching the current cursor line and threadCursor.
	path, line, side, ok := m.cursorThreadInfo()
	if !ok {
		return
	}

	targetLine := -1
	for _, cp := range m.dv.FileCommentPositions[idx] {
		if cp.Line == line && cp.Side == side && cp.Idx == m.dv.ThreadCursor-1 {
			targetLine = cp.Offset
			break
		}
	}
	if targetLine < 0 {
		_ = path // suppress unused
		return
	}

	vpH := m.dv.ViewportHeight()
	top := m.dv.VP.YOffset()
	bottom := top + vpH - 1

	if targetLine < top+scrollMargin {
		target := targetLine - scrollMargin
		if target < 0 {
			target = 0
		}
		m.dv.VP.SetYOffset(target)
	} else if targetLine > bottom-scrollMargin {
		m.dv.VP.SetYOffset(targetLine - vpH + scrollMargin + 1)
	}
}

// currentCommentRange returns the start and end rendered line offsets for
// the currently selected comment (threadCursor). Returns (-1,-1) if unknown.
func (m Model) currentCommentRange() (start, end int) {
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.FileCommentPositions) {
		return -1, -1
	}
	_, line, side, ok := m.cursorThreadInfo()
	if !ok {
		return -1, -1
	}

	// Find all positions for this thread.
	var threadPositions []components.CommentPosition
	for _, cp := range m.dv.FileCommentPositions[idx] {
		if cp.Line == line && cp.Side == side {
			threadPositions = append(threadPositions, cp)
		}
	}

	ci := m.dv.ThreadCursor - 1
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
		if m.dv.DiffCursor+1 < len(m.dv.FileDiffOffsets[idx]) {
			end = m.dv.FileDiffOffsets[idx][m.dv.DiffCursor+1] - 1
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
	vpH := m.dv.ViewportHeight()
	bottom := m.dv.VP.YOffset() + vpH - 1
	return end > bottom
}

// commentExtendsAbove returns true if the selected comment's header is above
// the viewport (needs scrolling up to see the top).
func (m Model) commentExtendsAbove() bool {
	start, _ := m.currentCommentRange()
	if start < 0 {
		return false
	}
	return start < m.dv.VP.YOffset()
}

// scrollToThreadCursorBottom scrolls so the bottom of the comment is visible
// (used when navigating up into a long comment).
func (m *Model) scrollToThreadCursorBottom() {
	_, end := m.currentCommentRange()
	if end < 0 {
		m.scrollToThreadCursor()
		return
	}
	vpH := m.dv.ViewportHeight()
	bottom := m.dv.VP.YOffset() + vpH - 1
	if end > bottom {
		m.dv.VP.SetYOffset(end - vpH + scrollMargin + 1)
	}
	// Also make sure the header is visible if the comment fits.
	start, _ := m.currentCommentRange()
	if start >= 0 && start < m.dv.VP.YOffset() {
		commentH := end - start + 1
		if commentH <= vpH-scrollMargin*2 {
			m.scrollToThreadCursor()
		}
	}
}

// scrollCommentBoxIntoView scrolls the viewport so the comment input box
// (which is inserted after the cursor line) is fully visible.
func (m *Model) scrollCommentBoxIntoView() {
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.FileDiffOffsets) || m.dv.DiffCursor >= len(m.dv.FileDiffOffsets[idx]) {
		return
	}
	vpH := m.dv.ViewportHeight()
	cursorLine := m.dv.FileDiffOffsets[idx][m.dv.DiffCursor]
	// The comment box is ~8 lines (border + textarea + hints) inserted after the cursor line.
	boxBottom := cursorLine + 10
	bottom := m.dv.VP.YOffset() + vpH - 1
	if boxBottom > bottom {
		m.dv.VP.SetYOffset(boxBottom - vpH + 1)
	}
}

// --- Comment composition ---

func (m Model) handleCommentKey(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		m.dv.Composing = false
		m.dv.SelectionAnchor = -1
		m.rebuildContent()
		return m, nil, true
	case "shift+enter":
		// Insert newline.
		m.dv.CommentInput.InsertString("\n")
		m.rebuildContent()
		return m, nil, true
	case "enter":
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
		if m.replyToID != "" {
			comment.InReplyToID = m.replyToID
		}

		m.commentStore.Add(comment)
		m.dv.SelectionAnchor = -1

		// Set copilot pending state BEFORE rebuild so "Thinking..." shows immediately.
		if m.dv.Copilot != nil {
			m.dv.SetCopilotPending(comment.ID, comment.Path, comment.Line, comment.Side)
			m.dv.CopilotDots = 0
		}

		// Use render list operations to update the cached render.
		fileIdx := m.dv.CurrentFileIdx
		spliced := false

		if m.replyToID != "" {
			// Reply to existing thread — splice the updated thread.
			m.dv.SpliceThreadForComment(fileIdx, comment.Side, comment.Line)
			spliced = true
		} else {
			// New thread — insert into render list.
			diffLineIdx := m.dv.DiffLineIdxForComment(fileIdx, comment.Side, comment.Line)
			if diffLineIdx >= 0 {
				threadComments := m.commentStore.ForFile(comment.Path)
				filtered := components.CommentsForThread(threadComments, comment.Side, comment.Line)
				if m.dv.InsertThread(fileIdx, diffLineIdx, comment.Side, comment.Line, filtered) {
					spliced = true
				}
			}
		}

		if spliced {
			m.rebuildContentIfChanged()
		} else {
			// Fallback to full re-render.
			m.formatFile(fileIdx)
			m.rebuildContent()
		}

		if m.dv.Copilot != nil {
			diffHunk := m.getDiffHunkForComment(comment)
			threadHistory := m.getThreadHistory(comment)
			return m, tea.Batch(
				m.dv.Copilot.SendComment(comment.ID, body, comment.Path, m.mode.String(), diffHunk, threadHistory),
				m.dv.Copilot.ListenCmd(),
				tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return copilotTickMsg{} }),
			), true
		}
		return m, nil, true
	}

	// Delegate to textarea.
	var cmd tea.Cmd
	m.dv.CommentInput, cmd = m.dv.CommentInput.Update(msg)
	m.rebuildContent()
	return m, cmd, true
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
	if dl.Type == components.LineHunk {
		return m, nil, false
	}

	m.dv.CommentFile = m.dv.Files[idx].Filename
	m.dv.CommentStartLine = 0
	m.dv.CommentStartSide = ""

	if m.dv.SelectionAnchor >= 0 && m.dv.SelectionAnchor != m.dv.DiffCursor {
		selStart, selEnd := m.dv.SelectionAnchor, m.dv.DiffCursor
		if selStart > selEnd {
			selStart, selEnd = selEnd, selStart
		}
		startDL := lines[selStart]
		endDL := lines[selEnd]
		if startDL.Type == components.LineHunk || endDL.Type == components.LineHunk {
			return m, nil, false
		}
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
		if dl.Type == components.LineDel {
			m.dv.CommentLine = dl.OldLineNo
			m.dv.CommentSide = "LEFT"
		} else {
			m.dv.CommentLine = dl.NewLineNo
			m.dv.CommentSide = "RIGHT"
		}
	}

	// Check for existing thread to reply to.
	if m.dv.CommentStartLine > 0 {
		m.replyToID = ""
	} else {
		m.replyToID = m.commentStore.FindThreadRoot(m.dv.CommentFile, m.dv.CommentLine, m.dv.CommentSide)
	}

	ta := textarea.New()
	ta.SetWidth(m.dv.ContentWidth() - 10 - 6)
	ta.SetHeight(5)
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Focus()
	if m.replyToID != "" {
		ta.Placeholder = "Reply to thread..."
	} else {
		ta.Placeholder = "Add a comment..."
	}
	m.dv.CommentInput = ta
	m.dv.Composing = true
	m.rebuildContent()
	m.scrollCommentBoxIntoView()
	return m, ta.Focus(), true
}

func (m Model) insertCommentBox(rendered string, fileIdx int) string {
	lines := strings.Split(rendered, "\n")
	cursorRenderedLine := -1
	if fileIdx < len(m.dv.FileDiffOffsets) && m.dv.DiffCursor < len(m.dv.FileDiffOffsets[fileIdx]) {
		cursorRenderedLine = m.dv.FileDiffOffsets[fileIdx][m.dv.DiffCursor]
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
	gutter := components.TotalGutterWidth(components.GutterColWidth(m.dv.FileDiffs[m.dv.CurrentFileIdx]))
	indent := strings.Repeat(" ", gutter)
	boxW := m.dv.ContentWidth() - gutter - 2

	taView := m.dv.CommentInput.View()

	isReply := m.replyToID != ""
	bc := m.dv.BorderStyle()
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
	idx := m.dv.CurrentFileIdx
	if idx >= len(m.dv.FileDiffs) || m.dv.DiffCursor >= len(m.dv.FileDiffs[idx]) {
		return
	}
	dl := m.dv.FileDiffs[idx][m.dv.DiffCursor]
	if dl.Type == components.LineHunk {
		return
	}
	path = m.dv.Files[idx].Filename
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
	m.dv.CommentFile = path
	m.dv.CommentLine = line
	m.dv.CommentSide = side
	m.dv.CommentStartLine = 0
	m.dv.CommentStartSide = ""
	ta := textarea.New()
	ta.SetWidth(m.dv.ContentWidth() - 10 - 6)
	ta.SetHeight(5)
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Focus()
	ta.Placeholder = "Reply to thread..."
	m.dv.CommentInput = ta
	m.dv.Composing = true
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
			// Use splice-based removal for resolved threads (O(content) vs O(n) full re-render).
			if !c.Resolved {
				// Thread is being resolved — remove it from rendered content.
				if m.dv.RemoveThread(m.dv.CurrentFileIdx, side, line) {
					m.rebuildContentIfChanged()
					return true
				}
			}
			break
		}
	}
	// Fallback to full re-render (e.g., unresolving brings thread back).
	m.formatFile(m.dv.CurrentFileIdx)
	m.rebuildContent()
	return true
}

// getDiffHunkForComment extracts the diff hunk around the commented line.
func (m Model) getDiffHunkForComment(c comments.LocalComment) string {
	// Find the file index.
	fileIdx := -1
	for i, f := range m.dv.Files {
		if f.Filename == c.Path {
			fileIdx = i
			break
		}
	}
	if fileIdx < 0 || fileIdx >= len(m.dv.FileDiffs) {
		return ""
	}

	lines := m.dv.FileDiffs[fileIdx]
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
	if m.branchData.branch != defaultBranch {
		items = append(items, picker.Item{
			Label:       "Branch Diff",
			Description: "vs " + defaultBranch,
			Value:       "branch",
			Keywords:    []string{"compare", "base"},
		})
	}

	if m.branchData.pr != nil {
		items = append(items, picker.Item{
			Label:       fmt.Sprintf("PR #%d", m.branchData.pr.Number),
			Description: m.branchData.pr.Title,
			Value:       "pr",
			Keywords:    []string{"pull request", "review"},
		})
	}

	return items
}

func (m Model) fileIndexForPath(path string) int {
	if idx, ok := m.branchData.filePathIndex[path]; ok {
		return idx
	}
	return -1
}

func (m *Model) rebuildFilePathIndex() {
	m.branchData.filePathIndex = make(map[string]int, len(m.dv.Files))
	for i, f := range m.dv.Files {
		m.branchData.filePathIndex[f.Filename] = i
	}
}

type stageDoneMsg struct{}

// stageWholeFile stages an entire file using the appropriate git command.
func (m Model) stageWholeFile(unstage bool) (Model, tea.Cmd, bool) {
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.Files) || idx >= len(m.dv.FileDiffs) {
		return m, nil, false
	}
	filename := m.dv.Files[idx].Filename
	fileStatus := m.dv.Files[idx].Status
	repoRoot := m.repoRoot

	// Optimistically remove the file from the view.
	m.removeDiffLines(idx, 0, len(m.dv.FileDiffs[idx])-1)
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
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.FileDiffs) || idx >= len(m.dv.Files) {
		return m, nil, false
	}

	lines := m.dv.FileDiffs[idx]
	filename := m.dv.Files[idx].Filename
	fileStatus := m.dv.Files[idx].Status
	patch := m.dv.Files[idx].Patch
	repoRoot := m.repoRoot

	// Determine selection range.
	selStart, selEnd := m.dv.DiffCursor, m.dv.DiffCursor
	if m.dv.SelectionAnchor >= 0 && m.dv.SelectionAnchor != m.dv.DiffCursor {
		selStart, selEnd = m.dv.SelectionAnchor, m.dv.DiffCursor
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

	m.dv.SelectionAnchor = -1

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
	idx := m.dv.CurrentFileIdx
	if idx < 0 || idx >= len(m.dv.FileDiffs) || idx >= len(m.dv.Files) {
		return m, nil, false
	}

	lines := m.dv.FileDiffs[idx]
	if m.dv.DiffCursor >= len(lines) {
		return m, nil, false
	}

	dl := lines[m.dv.DiffCursor]
	if dl.Type == components.LineHunk {
		return m, nil, false
	}

	filename := m.dv.Files[idx].Filename
	fileStatus := m.dv.Files[idx].Status
	patch := m.dv.Files[idx].Patch
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
	hunkStart, hunkEnd := m.findHunkRange(idx, m.dv.DiffCursor)
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
	if fileIdx >= len(m.dv.FileDiffs) {
		return
	}
	lines := m.dv.FileDiffs[fileIdx]
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
		m.dv.Files = append(m.dv.Files[:fileIdx], m.dv.Files[fileIdx+1:]...)
		m.dv.FileDiffs = append(m.dv.FileDiffs[:fileIdx], m.dv.FileDiffs[fileIdx+1:]...)
		m.dv.HighlightedFiles = append(m.dv.HighlightedFiles[:fileIdx], m.dv.HighlightedFiles[fileIdx+1:]...)
		m.dv.RenderedFiles = append(m.dv.RenderedFiles[:fileIdx], m.dv.RenderedFiles[fileIdx+1:]...)
		m.dv.FileDiffOffsets = append(m.dv.FileDiffOffsets[:fileIdx], m.dv.FileDiffOffsets[fileIdx+1:]...)
		m.dv.FileCommentPositions = append(m.dv.FileCommentPositions[:fileIdx], m.dv.FileCommentPositions[fileIdx+1:]...)
		m.dv.Tree.Files = m.dv.Files
		m.dv.Tree.Entries = components.BuildFileTree(m.dv.Files)
		m.dv.SelectionAnchor = -1
		m.rebuildFilePathIndex()

		// Navigate away from the removed file.
		if len(m.dv.Files) == 0 {
			m.dv.CurrentFileIdx = -1
			m.dv.Tree.Cursor = 0
			m.dv.DiffCursor = 0
		} else if m.dv.CurrentFileIdx >= len(m.dv.Files) {
			m.dv.CurrentFileIdx = len(m.dv.Files) - 1
			m.dv.Tree.Cursor = m.dv.Tree.IndexForFile(m.dv.CurrentFileIdx)
			m.dv.DiffCursor = m.dv.FirstNonHunkLine(m.dv.CurrentFileIdx)
			m.formatFile(m.dv.CurrentFileIdx)
		} else {
			m.dv.Tree.Cursor = m.dv.Tree.IndexForFile(m.dv.CurrentFileIdx)
			m.dv.DiffCursor = m.dv.FirstNonHunkLine(m.dv.CurrentFileIdx)
			m.formatFile(m.dv.CurrentFileIdx)
		}
		m.rebuildContent()
		return
	}

	m.dv.FileDiffs[fileIdx] = newLines

	// Clamp cursor and skip hunk lines.
	if m.dv.DiffCursor >= len(newLines) {
		m.dv.DiffCursor = len(newLines) - 1
	}
	if m.dv.DiffCursor < 0 {
		m.dv.DiffCursor = 0
	}
	if len(newLines) > 0 && newLines[m.dv.DiffCursor].Type == components.LineHunk {
		m.dv.DiffCursor = m.dv.FirstNonHunkLine(fileIdx)
	}

	// Re-format to get correct rendered content and offsets.
	m.formatFile(fileIdx)
	m.rebuildContent()
}

// findHunkRange returns the start and end diff line indices for the hunk
// containing the given line index.
func (m Model) findHunkRange(fileIdx, lineIdx int) (start, end int) {
	lines := m.dv.FileDiffs[fileIdx]

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
	for _, f := range m.dv.Files {
		if f.Filename == path {
			return f.Patch
		}
	}
	return ""
}
