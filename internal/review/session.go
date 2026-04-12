// Package review provides a shared data model for diff review across
// local and PR views. Each file owns its own diff, highlighting,
// rendering, and comments — no parallel arrays, no index math.
package review

import (
	"github.com/blakewilliams/ghq/internal/review/comments"
	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/components"
	"github.com/blakewilliams/ghq/internal/ui/styles"
)

// File holds all data for a single file in a review.
type File struct {
	Path             string
	PreviousPath     string // for renames
	Status           string // "added", "modified", "removed", "renamed"
	Patch            string
	Additions        int
	Deletions        int

	// Parsed from patch.
	DiffLines []components.DiffLine

	// Syntax highlighting (expensive, cached across resizes).
	Highlighted *components.HighlightedDiff

	// Rendering (cheap, width-dependent, rebuilt on resize/reformat).
	Rendered         string
	DiffOffsets      []int
	CommentPositions []components.CommentPosition

	// Comments on this file.
	GitHubComments []github.ReviewComment
	LocalComments  []comments.LocalComment
}

// HasChanges returns true if the file has any add/del diff lines.
func (f *File) HasChanges() bool {
	for _, dl := range f.DiffLines {
		if dl.Type == components.LineAdd || dl.Type == components.LineDel {
			return true
		}
	}
	return false
}

// AllComments returns GitHub + local comments merged as ReviewComment values.
func (f *File) AllComments() []github.ReviewComment {
	var result []github.ReviewComment
	result = append(result, f.GitHubComments...)

	// Filter resolved local comments.
	resolved := map[string]bool{}
	for _, c := range f.LocalComments {
		if c.Resolved && c.InReplyToID == "" {
			resolved[c.ID] = true
		}
	}
	for _, c := range f.LocalComments {
		if c.Path != f.Path {
			continue
		}
		if c.Resolved || resolved[c.InReplyToID] {
			continue
		}
		result = append(result, c.ToReviewComment())
	}
	return result
}

// Session holds the complete review state shared between local and PR views.
type Session struct {
	Files     map[string]*File // keyed by file path
	FileOrder []string         // display order (stable)

	// Navigation.
	CurrentFile string // path of the selected file ("" = overview)
	DiffCursor  int
	TreeCursor  int
	TreeEntries []components.FileTreeEntry
}

// NewSession creates an empty session.
func NewSession() *Session {
	return &Session{
		Files: make(map[string]*File),
	}
}

// SetFiles replaces the file list from a new diff result.
// Preserves highlights and comments for files whose patch hasn't changed.
func (s *Session) SetFiles(prf []github.PullRequestFile) {
	oldFiles := s.Files
	s.Files = make(map[string]*File, len(prf))
	s.FileOrder = make([]string, 0, len(prf))

	for _, f := range prf {
		rf := &File{
			Path:         f.Filename,
			PreviousPath: f.PreviousFilename,
			Status:       f.Status,
			Patch:        f.Patch,
			Additions:    f.Additions,
			Deletions:    f.Deletions,
			DiffLines:    components.ParsePatchLines(f.Patch),
		}

		// Reuse cached data if the patch hasn't changed.
		if old, ok := oldFiles[f.Filename]; ok && old.Patch == f.Patch {
			rf.Highlighted = old.Highlighted
			rf.Rendered = old.Rendered
			rf.DiffOffsets = old.DiffOffsets
			rf.CommentPositions = old.CommentPositions
		}

		// Preserve local comments (they're keyed by path).
		if old, ok := oldFiles[f.Filename]; ok {
			rf.LocalComments = old.LocalComments
		}

		s.Files[f.Filename] = rf
		s.FileOrder = append(s.FileOrder, f.Filename)
	}

	// Rebuild tree.
	s.TreeEntries = components.BuildFileTree(s.PRFiles())

	// Clamp current file.
	if s.CurrentFile != "" {
		if _, ok := s.Files[s.CurrentFile]; !ok {
			s.CurrentFile = ""
			s.DiffCursor = 0
		}
	}
}

// CurrentFileData returns the File for the currently selected file, or nil.
func (s *Session) CurrentFileData() *File {
	if s.CurrentFile == "" {
		return nil
	}
	return s.Files[s.CurrentFile]
}

// FileByPath returns the file at the given path, or nil.
func (s *Session) FileByPath(path string) *File {
	return s.Files[path]
}

// PRFiles returns the files as github.PullRequestFile values (for components that need that type).
func (s *Session) PRFiles() []github.PullRequestFile {
	result := make([]github.PullRequestFile, 0, len(s.FileOrder))
	for _, path := range s.FileOrder {
		f := s.Files[path]
		result = append(result, github.PullRequestFile{
			Filename:         f.Path,
			PreviousFilename: f.PreviousPath,
			Status:           f.Status,
			Patch:            f.Patch,
			Additions:        f.Additions,
			Deletions:        f.Deletions,
		})
	}
	return result
}

// FormatFile runs the cheap width-dependent formatting on a file.
func (s *Session) FormatFile(path string, width int, colors styles.DiffColors, opts ...components.DiffFormatOptions) {
	f := s.Files[path]
	if f == nil || f.Highlighted == nil {
		return
	}

	var opt components.DiffFormatOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	result := components.FormatDiffFile(*f.Highlighted, width, colors, f.AllComments(), opt)
	f.Rendered = result.Content
	f.DiffOffsets = result.DiffLineOffsets
	f.CommentPositions = result.CommentPositions
}

// RemoveFile removes a file from the session.
func (s *Session) RemoveFile(path string) {
	delete(s.Files, path)
	order := make([]string, 0, len(s.FileOrder)-1)
	for _, p := range s.FileOrder {
		if p != path {
			order = append(order, p)
		}
	}
	s.FileOrder = order
	s.TreeEntries = components.BuildFileTree(s.PRFiles())

	if s.CurrentFile == path {
		s.CurrentFile = ""
		s.DiffCursor = 0
	}
}

// FileCount returns the number of files.
func (s *Session) FileCount() int {
	return len(s.FileOrder)
}

// Stats returns total additions and deletions.
func (s *Session) Stats() (adds, dels int) {
	for _, f := range s.Files {
		adds += f.Additions
		dels += f.Deletions
	}
	return
}

// NeedsHighlight returns paths of files that don't have cached highlights.
func (s *Session) NeedsHighlight() []string {
	var paths []string
	for _, path := range s.FileOrder {
		f := s.Files[path]
		if f.Highlighted == nil && f.Patch != "" {
			paths = append(paths, path)
		}
	}
	return paths
}
