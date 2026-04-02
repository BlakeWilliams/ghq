package components

import (
	"path"
	"sort"
	"strings"

	"github.com/blakewilliams/ghq/internal/github"
	"github.com/charmbracelet/x/ansi"
	"charm.land/lipgloss/v2"
)

const (
	iconFolder   = "\U000f024b" // 󰉋 nf-md-folder
	iconPointer  = "\U000f0142" // 󰅂 nf-md-chevron_right
	iconPlus     = "\U000f0415" // 󰐕 nf-md-plus
	iconMinus    = "\U000f0374" // 󰍴 nf-md-minus
	iconRename   = "\U000f0453" // 󰑓 nf-md-rename_box
	iconConversation = "\U000f0219" // 󰈙 nf-md-file_document
)

var (
	treeDir      = lipgloss.NewStyle().Bold(true)
	treeFile     = lipgloss.NewStyle()
	treeSelected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Magenta)
	treeDim      = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	treeAdd      = lipgloss.NewStyle().Foreground(lipgloss.Green)
	treeDel      = lipgloss.NewStyle().Foreground(lipgloss.Red)
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
// currentFileIdx of -1 means "Conversation" is the selected entry.
// The first entry (index 0 in display) is always "Conversation".
// Tree entries start at display index 1.
func RenderFileTree(entries []FileTreeEntry, files []github.PullRequestFile, cursor int, currentFileIdx int, width, height int) []string {
	// Total display count: 1 (Conversation) + 1 (separator) + len(entries)
	totalEntries := 2 + len(entries)
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

		if idx == 0 {
			// Conversation entry — bold with icon.
			isCursor := cursor == 0
			isCurrent := currentFileIdx == -1
			overviewStyle := lipgloss.NewStyle().Bold(true)
			var line string
			if isCursor {
				line = treeSelected.Render(iconPointer + " " + iconConversation + " Conversation")
			} else if isCurrent {
				line = overviewStyle.Foreground(lipgloss.Magenta).Render("  " + iconConversation + " Conversation")
			} else {
				line = overviewStyle.Render("  " + iconConversation + " Conversation")
			}
			lines[row] = padTo(line, width)
			continue
		}

		if idx == 1 {
			// Separator line between Conversation and files.
			sep := treeDim.Render("  " + strings.Repeat("─", width-4))
			lines[row] = padTo(sep, width)
			continue
		}

		eIdx := idx - 2 // offset by 2 for Conversation + separator
		if eIdx >= len(entries) {
			lines[row] = strings.Repeat(" ", width)
			continue
		}

		e := entries[eIdx]
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
