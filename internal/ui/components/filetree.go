package components

import (
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/charmbracelet/x/ansi"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	iconFolder   = "\U000f024b" // 󰉋 nf-md-folder
	iconPointer  = "\U000f0142" // 󰅂 nf-md-chevron_right
	iconPlus     = "\U000f0415" // 󰐕 nf-md-plus
	iconMinus    = "\U000f0374" // 󰍴 nf-md-minus
	iconRename   = "\U000f0453" // 󰑓 nf-md-rename_box
	iconDescription = "\U000f0219" // 󰈙 nf-md-file_document
	iconComment  = "\U000f0188" // 󰆈 nf-md-comment_text_outline
)

var (
	treeDir      = lipgloss.NewStyle().Bold(true)
	treeFile     = lipgloss.NewStyle()
	treeSelected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Magenta)
	treeDim      = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	treeAdd      = lipgloss.NewStyle().Foreground(lipgloss.Green)
	treeDel      = lipgloss.NewStyle().Foreground(lipgloss.Red)
	treeComment  = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	treeUnread   = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
)

// FileTreeEntry is a flat entry in the rendered tree.
type FileTreeEntry struct {
	FileIndex int    // index into the files slice, -1 for directories
	Display   string // the display name (just filename, not full path)
	Depth     int    // nesting depth
	IsDir     bool
}

// BuildFileTree converts a flat list of changed files into a tree structure
// with common directory prefixes collapsed.
func BuildFileTree(files []github.PullRequestFile) []FileTreeEntry {
	type treeNode struct {
		name     string
		children map[string]*treeNode
		order    []string // insertion order of child keys
		files    []struct {
			name      string
			fileIndex int
		}
	}

	root := &treeNode{children: make(map[string]*treeNode)}

	// Build trie from file paths.
	for i, f := range files {
		parts := strings.Split(path.Dir(f.Filename), "/")
		node := root
		if parts[0] == "." {
			parts = nil
		}
		for _, p := range parts {
			if _, ok := node.children[p]; !ok {
				node.children[p] = &treeNode{children: make(map[string]*treeNode)}
				node.order = append(node.order, p)
			}
			node = node.children[p]
		}
		node.files = append(node.files, struct {
			name      string
			fileIndex int
		}{name: path.Base(f.Filename), fileIndex: i})
	}

	// Collapse single-child directory chains: a/ -> b/ -> files becomes a/b/ -> files.
	var collapse func(n *treeNode)
	collapse = func(n *treeNode) {
		for _, key := range n.order {
			child := n.children[key]
			collapse(child)
		}
		// If this node has exactly one child dir and no files, merge into parent.
		for len(n.order) == 1 && len(n.files) == 0 {
			childKey := n.order[0]
			child := n.children[childKey]
			// Merge child into this node under a combined name.
			newKey := childKey
			if len(child.order) == 1 && len(child.files) == 0 {
				// Child is also a single-dir passthrough — combine names.
				grandKey := child.order[0]
				newKey = childKey + "/" + grandKey
				grandchild := child.children[grandKey]
				delete(n.children, childKey)
				n.children[newKey] = grandchild
				n.order = []string{newKey}
			} else {
				// Child has files or multiple subdirs — absorb it.
				delete(n.children, childKey)
				n.children[newKey] = child
				n.order = []string{newKey}
				break
			}
		}
	}
	collapse(root)

	// Flatten trie into entries.
	var entries []FileTreeEntry
	var walk func(n *treeNode, depth int)
	walk = func(n *treeNode, depth int) {
		// Sort child dirs.
		sort.Strings(n.order)
		for _, key := range n.order {
			child := n.children[key]
			entries = append(entries, FileTreeEntry{
				FileIndex: -1,
				Display:   key + "/",
				Depth:     depth,
				IsDir:     true,
			})
			walk(child, depth+1)
		}
		// Files in this directory.
		for _, f := range n.files {
			entries = append(entries, FileTreeEntry{
				FileIndex: f.fileIndex,
				Display:   f.name,
				Depth:     depth,
			})
		}
	}
	walk(root, 0)

	return entries
}

// RenderFileTree renders the file tree as exactly `height` lines.
// Each line is padded to `width`. The cursor is kept visible.
func RenderFileTree(entries []FileTreeEntry, files []github.PullRequestFile, cursor int, currentFileIdx int, width, height int, commentCounts, unreadCounts []int) []string {
	loading := len(entries) == 0
	skeletonCount := 8
	entryCount := len(entries)
	if loading {
		entryCount = skeletonCount
	}

	totalEntries := entryCount
	lines := make([]string, height)

	// Scroll window: keep cursor visible, centered when possible.
	start := cursor - height/2
	if start < 0 {
		start = 0
	}
	if start+height > totalEntries {
		start = totalEntries - height
	}
	if start < 0 {
		start = 0
	}

	for row := 0; row < height; row++ {
		idx := start + row
		if idx >= totalEntries {
			lines[row] = strings.Repeat(" ", width)
			continue
		}

		if loading {
			if idx >= skeletonCount {
				lines[row] = strings.Repeat(" ", width)
				continue
			}
			skeletonWidths := []int{12, 18, 10, 15, 20, 8, 14, 16}
			sw := skeletonWidths[idx%len(skeletonWidths)]
			line := "  " + treeDim.Render("  "+strings.Repeat("─", sw))
			lines[row] = padTo(line, width)
			continue
		}

		if idx >= len(entries) {
			lines[row] = strings.Repeat(" ", width)
			continue
		}

		e := entries[idx]
		depthPad := strings.Repeat(" ", e.Depth*2)

		var line string
		if e.IsDir {
			line = "  " + depthPad + treeDir.Render(iconFolder+" "+e.Display)
		} else {
			f := files[e.FileIndex]
			isCurrent := e.FileIndex == currentFileIdx
			isCursor := idx == cursor

			name := e.Display
			var stats string
			switch f.Status {
			case "added":
				stats = treeAdd.Render(iconPlus)
			case "removed":
				stats = treeDel.Render(iconMinus)
			case "renamed":
				stats = treeDim.Render(iconRename)
			default:
				stats = treeAdd.Render(iconPlus) + treeDel.Render(iconMinus)
			}

			if isCursor {
				name = treeSelected.Render(iconPointer + " " + name)
			} else if isCurrent {
				name = treeSelected.Render("  " + name)
			} else {
				name = treeFile.Render("  " + name)
			}

			line = depthPad + name + " " + stats

			// Comment badge: right-aligned "󰆈 N"
			cc := 0
			if e.FileIndex < len(commentCounts) {
				cc = commentCounts[e.FileIndex]
			}
			if cc > 0 {
				uc := 0
				if e.FileIndex < len(unreadCounts) {
					uc = unreadCounts[e.FileIndex]
				}
				badge := iconComment + " " + strconv.Itoa(cc)
				style := treeComment
				if uc > 0 {
					style = treeUnread
				}
				badgeRendered := style.Render(badge)
				badgeW := lipgloss.Width(badgeRendered)
				lineW := lipgloss.Width(line)
				gap := width - lineW - badgeW - 1 // 1 col right margin
				if gap >= 0 {
					line = line + strings.Repeat(" ", gap) + badgeRendered
				}
			}
		}

		lines[row] = padTo(line, width)
	}

	return lines
}

func padTo(s string, width int) string {
	w := lipgloss.Width(s)
	if w > width {
		return ansi.Truncate(s, width, "")
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}
// FileSelectedMsg is produced when the user selects a file (or overview) in the tree.
type FileSelectedMsg struct {
	FileIndex int // -1 for overview
}

// FileTree is a Bubble Tea model for a navigable file tree panel.
type FileTree struct {
	Entries           []FileTreeEntry
	Cursor            int
	Width             int
	Height            int
	Focused           bool
	CurrentFileIdx    int // which file is currently being viewed (-1 = overview)
	Files             []github.PullRequestFile
	ChromeRows        int   // rows above tree (header + separator) for mouse offset
	FileCommentCounts []int // total comment count per file (parallel to Files)
	FileUnreadCounts  []int // unread comment count per file (parallel to Files)
}

// SetFiles rebuilds the tree from a new file list and resets the cursor.
func (t *FileTree) SetFiles(files []github.PullRequestFile) {
	t.Files = files
	t.Entries = BuildFileTree(files)
	t.Cursor = 0
}

// View renders the file tree panel content (no borders — the parent adds those).
func (t FileTree) View() []string {
	return RenderFileTree(t.Entries, t.Files, t.Cursor, t.CurrentFileIdx, t.Width, t.Height, t.FileCommentCounts, t.FileUnreadCounts)
}

// scrollStart computes the first visible entry index for the tree.
func scrollStart(totalEntries, cursor, height int) int {
	start := cursor - height/2
	if start < 0 {
		start = 0
	}
	if start+height > totalEntries {
		start = totalEntries - height
	}
	if start < 0 {
		start = 0
	}
	return start
}

// HandleKey processes a key press. Returns (updated tree, cmd, handled).
// Only handles keys when focused. The parent should check handled and
// fall through to its own key handling if false.
func (t FileTree) HandleKey(msg tea.KeyPressMsg) (FileTree, tea.Cmd, bool) {
	if !t.Focused {
		return t, nil, false
	}
	switch msg.String() {
	case "j", "down":
		t.MoveCursorBy(1)
		return t, nil, true
	case "k", "up":
		t.MoveCursorBy(-1)
		return t, nil, true
	case "ctrl+d":
		t.MoveCursorBy(t.Height / 2)
		return t, nil, true
	case "ctrl+u":
		t.MoveCursorBy(-t.Height / 2)
		return t, nil, true
	case "ctrl+f":
		t.MoveCursorBy(t.Height)
		return t, nil, true
	case "ctrl+b":
		t.MoveCursorBy(-t.Height)
		return t, nil, true
	case "G":
		t.MoveCursorBy(len(t.Entries))
		return t, nil, true
	case "enter":
		return t, t.selectCmd(), true
	}
	return t, nil, false
}

// Select triggers selection of the current cursor entry.
// Returns a cmd that produces FileSelectedMsg.
func (t FileTree) selectCmd() tea.Cmd {
	idx := t.fileIndexAtCursor()
	return func() tea.Msg { return FileSelectedMsg{FileIndex: idx} }
}

// SelectFile moves the cursor to the given file and marks it current.
func (t *FileTree) SelectFile(fileIdx int) {
	t.CurrentFileIdx = fileIdx
	if fileIdx < 0 {
		t.Cursor = 0
		return
	}
	for i, e := range t.Entries {
		if !e.IsDir && e.FileIndex == fileIdx {
			t.Cursor = i
			return
		}
	}
}

// MoveSelection moves to the next/prev selectable file and produces FileSelectedMsg.
func (t *FileTree) MoveSelection(delta int) tea.Cmd {
	totalEntries := len(t.Entries)
	newCursor := t.Cursor + delta

	for newCursor >= 0 && newCursor < totalEntries {
		if newCursor < len(t.Entries) && !t.Entries[newCursor].IsDir {
			break
		}
		newCursor += delta
	}

	if newCursor < 0 || newCursor >= totalEntries {
		return nil
	}
	t.Cursor = newCursor
	return t.selectCmd()
}

// fileIndexAtCursor returns the file index at the current cursor, or -1.
func (t FileTree) fileIndexAtCursor() int {
	if t.Cursor < 0 || t.Cursor >= len(t.Entries) {
		return -1
	}
	e := t.Entries[t.Cursor]
	if !e.IsDir {
		return e.FileIndex
	}
	return -1
}

// FileIndex returns the file index of the currently focused tree entry, or -1.
func (t FileTree) FileIndex() int {
	return t.fileIndexAtCursor()
}

// IndexForFile returns the tree cursor position for a given file index.
func (t FileTree) IndexForFile(fileIdx int) int {
	for i, e := range t.Entries {
		if !e.IsDir && e.FileIndex == fileIdx {
			return i
		}
	}
	return 0
}

// EntryIndexAtY maps a mouse Y coordinate to a tree entry index,
// accounting for chrome rows above the tree and scroll position.
func (t FileTree) EntryIndexAtY(y int) (int, bool) {
	row := y - t.ChromeRows
	if row < 0 || row >= t.Height {
		return 0, false
	}
	start := scrollStart(len(t.Entries), t.Cursor, t.Height)
	idx := start + row
	if idx < 0 || idx >= len(t.Entries) {
		return 0, false
	}
	return idx, true
}

// HandleMouseClick processes a click in the tree area.
// Returns (updated tree, cmd, handled).
func (t FileTree) HandleMouseClick(msg tea.MouseClickMsg) (FileTree, tea.Cmd, bool) {
	if msg.X >= t.Width {
		return t, nil, false
	}
	idx, ok := t.EntryIndexAtY(msg.Y)
	if !ok {
		return t, nil, false
	}
	t.Cursor = idx
	return t, t.selectCmd(), true
}

func (t *FileTree) MoveCursorBy(delta int) {
	totalEntries := len(t.Entries)
	newCursor := t.Cursor + delta

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
		if !t.Entries[newCursor].IsDir {
			break
		}
		newCursor += dir
	}
	if newCursor < 0 || newCursor >= totalEntries {
		return
	}
	t.Cursor = newCursor
}
