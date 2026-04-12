package review

import (
	"testing"

	"github.com/blakewilliams/ghq/internal/review/comments"
	"github.com/blakewilliams/ghq/internal/github"
	"github.com/blakewilliams/ghq/internal/ui/components"
)

func TestSetFiles_Basic(t *testing.T) {
	s := NewSession()
	s.SetFiles([]github.PullRequestFile{
		{Filename: "a.go", Status: "modified", Patch: "@@ -1,3 +1,4 @@\n ctx\n+added\n ctx"},
		{Filename: "b.go", Status: "added", Patch: "@@ -0,0 +1,2 @@\n+line1\n+line2"},
	})

	if s.FileCount() != 2 {
		t.Fatalf("expected 2 files, got %d", s.FileCount())
	}
	if len(s.FileOrder) != 2 || s.FileOrder[0] != "a.go" || s.FileOrder[1] != "b.go" {
		t.Errorf("unexpected order: %v", s.FileOrder)
	}

	a := s.FileByPath("a.go")
	if a == nil {
		t.Fatal("a.go not found")
	}
	if len(a.DiffLines) == 0 {
		t.Error("a.go should have parsed diff lines")
	}
}

func TestSetFiles_PreservesCache(t *testing.T) {
	s := NewSession()
	s.SetFiles([]github.PullRequestFile{
		{Filename: "a.go", Patch: "@@ -1,2 +1,3 @@\n ctx\n+added\n ctx"},
	})

	// Simulate highlight cache.
	hl := &components.HighlightedDiff{Filename: "a.go"}
	s.Files["a.go"].Highlighted = hl
	s.Files["a.go"].Rendered = "cached render"

	// Reload with same patch — should preserve cache.
	s.SetFiles([]github.PullRequestFile{
		{Filename: "a.go", Patch: "@@ -1,2 +1,3 @@\n ctx\n+added\n ctx"},
	})

	if s.Files["a.go"].Highlighted != hl {
		t.Error("highlight cache should be preserved for unchanged patch")
	}
	if s.Files["a.go"].Rendered != "cached render" {
		t.Error("rendered cache should be preserved for unchanged patch")
	}
}

func TestSetFiles_InvalidatesCacheOnPatchChange(t *testing.T) {
	s := NewSession()
	s.SetFiles([]github.PullRequestFile{
		{Filename: "a.go", Patch: "@@ -1,2 +1,3 @@\n ctx\n+added\n ctx"},
	})
	s.Files["a.go"].Highlighted = &components.HighlightedDiff{Filename: "a.go"}
	s.Files["a.go"].Rendered = "old render"

	// Reload with different patch — should clear cache.
	s.SetFiles([]github.PullRequestFile{
		{Filename: "a.go", Patch: "@@ -1,2 +1,4 @@\n ctx\n+added\n+another\n ctx"},
	})

	if s.Files["a.go"].Highlighted != nil {
		t.Error("highlight cache should be cleared on patch change")
	}
	if s.Files["a.go"].Rendered != "" {
		t.Error("rendered cache should be cleared on patch change")
	}
}

func TestSetFiles_PreservesLocalComments(t *testing.T) {
	s := NewSession()
	s.SetFiles([]github.PullRequestFile{
		{Filename: "a.go", Patch: "@@ -1,2 +1,3 @@\n ctx\n+added\n ctx"},
	})

	s.Files["a.go"].LocalComments = []comments.LocalComment{
		{ID: "c1", Body: "test", Path: "a.go"},
	}

	// Reload.
	s.SetFiles([]github.PullRequestFile{
		{Filename: "a.go", Patch: "@@ -1,2 +1,4 @@\n ctx\n+added\n+new\n ctx"},
	})

	if len(s.Files["a.go"].LocalComments) != 1 {
		t.Error("local comments should be preserved across reloads")
	}
}

func TestSetFiles_ClampsCurrentFile(t *testing.T) {
	s := NewSession()
	s.SetFiles([]github.PullRequestFile{
		{Filename: "a.go", Patch: "@@"},
		{Filename: "b.go", Patch: "@@"},
	})
	s.CurrentFile = "b.go"

	// Reload without b.go.
	s.SetFiles([]github.PullRequestFile{
		{Filename: "a.go", Patch: "@@"},
	})

	if s.CurrentFile != "" {
		t.Errorf("CurrentFile should be cleared when file removed, got %q", s.CurrentFile)
	}
}

func TestRemoveFile(t *testing.T) {
	s := NewSession()
	s.SetFiles([]github.PullRequestFile{
		{Filename: "a.go", Patch: "@@"},
		{Filename: "b.go", Patch: "@@"},
		{Filename: "c.go", Patch: "@@"},
	})
	s.CurrentFile = "b.go"

	s.RemoveFile("b.go")

	if s.FileCount() != 2 {
		t.Errorf("expected 2 files, got %d", s.FileCount())
	}
	if _, ok := s.Files["b.go"]; ok {
		t.Error("b.go should be removed")
	}
	if s.CurrentFile != "" {
		t.Error("CurrentFile should be cleared after removing current file")
	}
}

func TestHasChanges(t *testing.T) {
	f := &File{
		DiffLines: []components.DiffLine{
			{Type: components.LineContext},
			{Type: components.LineAdd},
			{Type: components.LineContext},
		},
	}
	if !f.HasChanges() {
		t.Error("expected HasChanges=true with an add line")
	}

	f2 := &File{
		DiffLines: []components.DiffLine{
			{Type: components.LineContext},
			{Type: components.LineHunk},
			{Type: components.LineContext},
		},
	}
	if f2.HasChanges() {
		t.Error("expected HasChanges=false with only context+hunk")
	}
}

func TestNeedsHighlight(t *testing.T) {
	s := NewSession()
	s.SetFiles([]github.PullRequestFile{
		{Filename: "a.go", Patch: "@@ -1 +1 @@\n+a"},
		{Filename: "b.go", Patch: "@@ -1 +1 @@\n+b"},
		{Filename: "c.go", Patch: ""}, // no patch, binary
	})

	// Simulate a.go already highlighted.
	s.Files["a.go"].Highlighted = &components.HighlightedDiff{}

	needs := s.NeedsHighlight()
	if len(needs) != 1 || needs[0] != "b.go" {
		t.Errorf("expected [b.go], got %v", needs)
	}
}

func TestStats(t *testing.T) {
	s := NewSession()
	s.SetFiles([]github.PullRequestFile{
		{Filename: "a.go", Additions: 10, Deletions: 3},
		{Filename: "b.go", Additions: 5, Deletions: 0},
	})

	adds, dels := s.Stats()
	if adds != 15 || dels != 3 {
		t.Errorf("expected 15/3, got %d/%d", adds, dels)
	}
}
