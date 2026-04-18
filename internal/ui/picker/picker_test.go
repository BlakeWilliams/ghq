package picker

import (
	"testing"
	"unicode/utf8"
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

func fileItems() []Item {
	return []Item{
		{Label: "internal/ui/app.go", Value: "internal/ui/app.go"},
		{Label: "internal/ui/diffviewer/diffviewer.go", Value: "internal/ui/diffviewer/diffviewer.go"},
		{Label: "internal/ui/localdiff/localdiff.go", Value: "internal/ui/localdiff/localdiff.go"},
		{Label: "internal/ui/picker/picker.go", Value: "internal/ui/picker/picker.go"},
		{Label: "internal/ui/prdetail/prdetail.go", Value: "internal/ui/prdetail/prdetail.go"},
		{Label: "internal/ui/uictx/uictx.go", Value: "internal/ui/uictx/uictx.go"},
		{Label: "internal/ui/statusbar.go", Value: "internal/ui/statusbar.go"},
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

// --- fuzzyMatch tests ---

func TestFuzzyMatch_Basic(t *testing.T) {
	score, positions := fuzzyMatch("internal/ui/app.go", "app")
	if score == 0 {
		t.Fatal("expected 'app' to match 'internal/ui/app.go'")
	}
	if len(positions) != 3 {
		t.Fatalf("expected 3 positions, got %d", len(positions))
	}
	// Greedy: 'a' at 6 (internAl), 'p' at 13, 'p' at 14.
	// The key property: positions are in ascending order and valid.
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			t.Errorf("positions not ascending: %v", positions)
			break
		}
	}
}

func TestFuzzyMatch_NoMatch(t *testing.T) {
	score, positions := fuzzyMatch("internal/ui/app.go", "xyz")
	if score != 0 {
		t.Errorf("expected 0 score, got %d", score)
	}
	if positions != nil {
		t.Errorf("expected nil positions, got %v", positions)
	}
}

func TestFuzzyMatch_EmptyQuery(t *testing.T) {
	score, _ := fuzzyMatch("anything", "")
	if score != 1 {
		t.Errorf("expected score 1 for empty query, got %d", score)
	}
}

func TestFuzzyMatch_WordBoundaryBonus(t *testing.T) {
	// 'p' after '/' should score higher than 'p' mid-word.
	scoreBoundary, _ := fuzzyMatch("foo/picker", "p")
	scoreMid, _ := fuzzyMatch("fooopicker", "p")
	if scoreBoundary <= scoreMid {
		t.Errorf("boundary match (%d) should score higher than mid-word (%d)", scoreBoundary, scoreMid)
	}
}

func TestFuzzyMatch_ConsecutiveBonus(t *testing.T) {
	// "app" consecutive should score higher than "a...p...p" spread out.
	scoreConsecutive, _ := fuzzyMatch("app.go", "app")
	scoreSpread, _ := fuzzyMatch("a_x_p_x_p.go", "app")
	if scoreConsecutive <= scoreSpread {
		t.Errorf("consecutive (%d) should score higher than spread (%d)", scoreConsecutive, scoreSpread)
	}
}

func TestFuzzyMatch_DotBoundary(t *testing.T) {
	// 'g' after '.' should get word boundary bonus.
	score, positions := fuzzyMatch("app.go", "go")
	if score == 0 {
		t.Fatal("expected match")
	}
	// g at 4, o at 5
	if len(positions) != 2 || positions[0] != 4 || positions[1] != 5 {
		t.Errorf("expected positions [4,5], got %v", positions)
	}
}

func TestFuzzyMatch_TailBias(t *testing.T) {
	// Matching 'a' near the end of a path should score higher than near the start.
	scoreTail, _ := fuzzyMatch("xxxxxxxxxxxxa", "a")
	scoreHead, _ := fuzzyMatch("axxxxxxxxxxxx", "a")
	if scoreTail <= scoreHead {
		t.Errorf("tail match (%d) should score higher than head match (%d)", scoreTail, scoreHead)
	}
}

func TestFuzzyMatch_FilenameBeatsDirMatch(t *testing.T) {
	// "app" matching in the filename portion should beat "app" in a directory.
	scoreFilename, _ := fuzzyMatch("internal/ui/app.go", "app")
	scoreDir, _ := fuzzyMatch("app/ui/internal.go", "app")
	if scoreFilename <= scoreDir {
		t.Errorf("filename match (%d) should score higher than dir match (%d)", scoreFilename, scoreDir)
	}
}

// --- scoreItem tests ---

func TestScoreItem_LabelOnly(t *testing.T) {
	item := Item{Label: "internal/ui/picker/picker.go", Value: "internal/ui/picker/picker.go"}
	score, positions := scoreItem(item, "pick")
	if score == 0 {
		t.Fatal("expected 'pick' to match")
	}
	if len(positions) == 0 {
		t.Fatal("expected highlight positions for label match")
	}
}

func TestScoreItem_NoDuplicateInflation(t *testing.T) {
	// When Label == Value, the Value should not contribute additional score.
	item := Item{Label: "internal/ui/app.go", Value: "internal/ui/app.go"}
	score, _ := scoreItem(item, "app")

	itemWithDistinctValue := Item{Label: "internal/ui/app.go", Value: "something-else", Keywords: []string{"app"}}
	scoreDistinct, _ := scoreItem(itemWithDistinctValue, "app")

	// The item with distinct Value+Keywords should score higher (more fields match).
	if score > scoreDistinct {
		t.Errorf("duplicate Label==Value (%d) should not score higher than distinct fields (%d)", score, scoreDistinct)
	}
}

func TestScoreItem_KeywordFallback(t *testing.T) {
	// Query matches keyword but not label — should match with no positions.
	item := Item{Label: "Quit", Description: "Exit ghq", Value: "quit", Keywords: []string{"exit"}}
	score, positions := scoreItem(item, "exit")
	if score == 0 {
		t.Fatal("expected keyword 'exit' to match")
	}
	if len(positions) != 0 {
		t.Errorf("expected no label positions for keyword-only match, got %v", positions)
	}
}

func TestScoreItem_LabelMatchRankedAboveKeyword(t *testing.T) {
	items := []Item{
		{Label: "Refresh", Value: "refresh"},
		{Label: "Something", Value: "something", Keywords: []string{"refresh"}},
	}
	score0, _ := scoreItem(items[0], "ref")
	score1, _ := scoreItem(items[1], "ref")
	if score0 <= score1 {
		t.Errorf("label match (%d) should rank above keyword-only match (%d)", score0, score1)
	}
}

// --- File picker direct mode tests ---

func TestFilePicker_MatchPositionsAlwaysFromLabel(t *testing.T) {
	m := New("Files", fileItems(), 80, 30)
	m.query = "pick"
	m.filter()

	if len(m.filtered) == 0 {
		t.Fatal("expected matches for 'pick'")
	}

	for _, si := range m.filtered {
		item := m.items[si.index]
		if si.matchPositions == nil {
			t.Errorf("item %q matched but has nil positions", item.Label)
			continue
		}
		// Every position should be a valid rune index into the label.
		runeCount := utf8.RuneCountInString(item.Label)
		for _, pos := range si.matchPositions {
			if pos < 0 || pos >= runeCount {
				t.Errorf("item %q: position %d out of bounds (rune count=%d)", item.Label, pos, runeCount)
			}
		}
	}
}

func TestFilePicker_PathFuzzy(t *testing.T) {
	m := New("Files", fileItems(), 80, 30)
	m.query = "ipg"
	m.filter()

	// Should match files containing i...p...g in order.
	if len(m.filtered) == 0 {
		t.Fatal("expected matches for 'ipg'")
	}

	// All matched items should have positions.
	for _, si := range m.filtered {
		item := m.items[si.index]
		if si.matchPositions == nil {
			t.Errorf("item %q matched but has nil positions", item.Label)
		}
	}
}

func TestFilePicker_NoFalseMatchFromDuplicateValue(t *testing.T) {
	// Item where Label == Value. A query that can't match the label as a
	// subsequence should NOT match, even though the old combined-text
	// approach would have found a match by reading across duplicated fields.
	item := Item{Label: "internal/ui/localdiff/localdiff.go", Value: "internal/ui/localdiff/localdiff.go"}
	// "ilou": i(0), l(7), o(13)... but the only 'u' is at position 9 (before o).
	// This is NOT a valid subsequence of the label.
	score, _ := scoreItem(item, "ilou")
	if score != 0 {
		t.Errorf("'ilou' should not match %q (not a valid subsequence), but got score %d", item.Label, score)
	}
}

func TestFilePicker_DirectModeRanking(t *testing.T) {
	m := New("Files", fileItems(), 80, 30)
	m.query = "dv"
	m.filter()

	if len(m.filtered) == 0 {
		t.Fatal("expected matches for 'dv'")
	}

	// diffviewer should rank first — 'd' and 'v' are both at word boundaries.
	top := m.items[m.filtered[0].index]
	if top.Value != "internal/ui/diffviewer/diffviewer.go" {
		t.Errorf("expected diffviewer as top match for 'dv', got %q", top.Value)
	}
}

func TestFilePicker_StatusbarMatch(t *testing.T) {
	m := New("Files", fileItems(), 80, 30)
	m.query = "stat"
	m.filter()

	if len(m.filtered) == 0 {
		t.Fatal("expected matches for 'stat'")
	}

	found := false
	for _, si := range m.filtered {
		if m.items[si.index].Value == "internal/ui/statusbar.go" {
			found = true
			if si.matchPositions == nil {
				t.Error("statusbar matched but has nil positions")
			}
			break
		}
	}
	if !found {
		t.Error("expected statusbar.go to match 'stat'")
	}
}

// --- highlightMatches tests ---

func TestHighlightMatches_NoPositions(t *testing.T) {
	result := highlightMatches("hello", nil, false)
	if result != "hello" {
		t.Errorf("expected plain 'hello', got %q", result)
	}
}

func TestHighlightMatches_PositionsPresent(t *testing.T) {
	// Just verify it doesn't panic and produces non-empty output.
	result := highlightMatches("hello", []int{0, 2, 4}, false)
	if result == "" {
		t.Error("expected non-empty result")
	}
	if result == "hello" {
		t.Error("expected styled output, got plain text")
	}
}

// --- Unicode tests ---

func TestFuzzyMatch_Unicode(t *testing.T) {
	// "café" has 4 runes but 5 bytes (é is 2 bytes in UTF-8).
	score, positions := fuzzyMatch("café", "cé")
	if score == 0 {
		t.Fatal("expected 'cé' to match 'café'")
	}
	if len(positions) != 2 {
		t.Fatalf("expected 2 positions, got %d: %v", len(positions), positions)
	}
	// Rune indices: c=0, é=3
	if positions[0] != 0 || positions[1] != 3 {
		t.Errorf("expected rune positions [0, 3], got %v", positions)
	}
}

func TestFuzzyMatch_UnicodeMultibyte(t *testing.T) {
	// "日本語テスト" — all multi-byte runes.
	score, positions := fuzzyMatch("日本語テスト", "日テ")
	if score == 0 {
		t.Fatal("expected '日テ' to match '日本語テスト'")
	}
	if len(positions) != 2 {
		t.Fatalf("expected 2 positions, got %d: %v", len(positions), positions)
	}
	// Rune indices: 日=0, テ=3
	if positions[0] != 0 || positions[1] != 3 {
		t.Errorf("expected rune positions [0, 3], got %v", positions)
	}
}

func TestHighlightMatches_Unicode(t *testing.T) {
	// Ensure highlightMatches doesn't panic and correctly highlights rune-indexed positions.
	result := highlightMatches("café", []int{0, 3}, false)
	if result == "" {
		t.Error("expected non-empty result")
	}
	if result == "café" {
		t.Error("expected styled output, got plain text")
	}
}

func TestScoreItem_UnicodePositions(t *testing.T) {
	item := Item{Label: "Ünïcödé", Value: "unicode"}
	score, positions := scoreItem(item, "ünï")
	if score == 0 {
		t.Fatal("expected match")
	}
	runeCount := utf8.RuneCountInString(item.Label)
	for _, pos := range positions {
		if pos < 0 || pos >= runeCount {
			t.Errorf("position %d out of rune bounds (count=%d)", pos, runeCount)
		}
	}
}
