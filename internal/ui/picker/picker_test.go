package picker

import (
	"testing"
)

func testItems() []Item {
	return []Item{
		{Label: "Inbox", Description: "Go to PR inbox", Value: "inbox", Keywords: []string{"home", "notifications"}},
		{Label: "Local Diff", Description: "Local changes view", Value: "local", Keywords: []string{"diff", "changes"}},
		{Label: "Open PR", Description: "PR for current branch", Value: "pr", Keywords: []string{"pull request"}},
		{Label: "Refresh", Description: "Reload current view", Value: "refresh"},
		{Label: "Quit", Description: "Exit ghq", Value: "quit", Keywords: []string{"exit"}},
	}
}

func TestFilter_NoQuery(t *testing.T) {
	m := New("Test", testItems(), 60, 20)
	if len(m.filtered) != 5 {
		t.Errorf("expected 5 items with no query, got %d", len(m.filtered))
	}
}

func TestFilter_ExactMatch(t *testing.T) {
	m := New("Test", testItems(), 60, 20)
	m.query = "inbox"
	m.filter()

	if len(m.filtered) == 0 {
		t.Fatal("expected matches for 'inbox'")
	}
	if m.items[m.filtered[0].index].Value != "inbox" {
		t.Errorf("expected 'inbox' as top match, got %s", m.items[m.filtered[0].index].Value)
	}
}

func TestFilter_SubstringMatch(t *testing.T) {
	m := New("Test", testItems(), 60, 20)
	m.query = "lo"
	m.filter()

	if len(m.filtered) == 0 {
		t.Fatal("expected matches for 'lo'")
	}
	// "Local Diff" should match.
	found := false
	for _, si := range m.filtered {
		if m.items[si.index].Value == "local" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'Local Diff' to match 'lo'")
	}
}

func TestFilter_KeywordMatch(t *testing.T) {
	m := New("Test", testItems(), 60, 20)
	m.query = "exit"
	m.filter()

	if len(m.filtered) == 0 {
		t.Fatal("expected matches for 'exit'")
	}
	if m.items[m.filtered[0].index].Value != "quit" {
		t.Errorf("expected 'quit' to match keyword 'exit', got %s", m.items[m.filtered[0].index].Value)
	}
}

func TestFilter_NoMatch(t *testing.T) {
	m := New("Test", testItems(), 60, 20)
	m.query = "zzzzz"
	m.filter()

	if len(m.filtered) != 0 {
		t.Errorf("expected 0 matches for 'zzzzz', got %d", len(m.filtered))
	}
}

func TestFilter_MultiWord(t *testing.T) {
	m := New("Test", testItems(), 60, 20)
	m.query = "local diff"
	m.filter()

	if len(m.filtered) == 0 {
		t.Fatal("expected matches for 'local diff'")
	}
	if m.items[m.filtered[0].index].Value != "local" {
		t.Errorf("expected 'local' as top match, got %s", m.items[m.filtered[0].index].Value)
	}
}

func TestFilter_FuzzyInitials(t *testing.T) {
	m := New("Test", testItems(), 60, 20)
	m.query = "ld"
	m.filter()

	if len(m.filtered) == 0 {
		t.Fatal("expected matches for 'ld' (fuzzy: l→Local, d→Diff)")
	}
	found := false
	for _, si := range m.filtered {
		if m.items[si.index].Value == "local" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'Local Diff' to match 'ld'")
	}
}

func TestCursorClamp(t *testing.T) {
	m := New("Test", testItems(), 60, 20)
	m.cursor = 4

	m.query = "inbox"
	m.filter()

	// Cursor should be clamped to filtered length.
	if m.cursor >= len(m.filtered) {
		t.Errorf("cursor %d should be < filtered length %d", m.cursor, len(m.filtered))
	}
}
