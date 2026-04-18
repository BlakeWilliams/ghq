package picker

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Item is a single entry in the picker.
type Item struct {
	Label       string   // primary display text
	Description string   // secondary text (dimmed, right-aligned)
	Value       string   // returned on selection
	Keywords    []string // extra fuzzy match terms
}

// ResultMsg is sent when the picker closes (selection or cancel).
type ResultMsg struct {
	Value    string // selected item's Value, or "" if cancelled
	Selected bool   // true if user selected, false if cancelled
}

// Model is a generic fzf-style fuzzy picker.
type Model struct {
	items      []Item
	filtered   []scoredItem
	query      string
	cursor     int // position in filtered list
	width      int
	height     int
	title      string
	maxVisible int
}

type scoredItem struct {
	index          int
	score          int
	matchPositions []int // character indices in Label that matched the query
}

var (
	promptStyle    = lipgloss.NewStyle().Foreground(lipgloss.Magenta).Bold(true)
	queryStyle     = lipgloss.NewStyle()
	descStyle      = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	countStyle     = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	pointerStyle   = lipgloss.NewStyle().Foreground(lipgloss.Yellow).Bold(true)
	selectedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Yellow).Bold(true)
	selectedDesc   = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	matchStyle     = lipgloss.NewStyle().Foreground(lipgloss.Green).Bold(true)
)

// New creates a picker with the given title and items.
func New(title string, items []Item, width, height int) Model {
	maxVis := height - 4 // input + count line + border overhead
	if maxVis < 3 {
		maxVis = 3
	}
	// Don't show more rows than items, and cap at a reasonable max.
	if maxVis > len(items) {
		maxVis = len(items)
	}
	if maxVis > 20 {
		maxVis = 20
	}

	m := Model{
		items:      items,
		title:      title,
		width:      width,
		height:     height,
		maxVisible: maxVis,
	}
	m.filter()
	return m
}

// Title returns the picker's title.
func (m Model) Title() string { return m.title }

// Update handles all input when the picker is active.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m, func() tea.Msg { return ResultMsg{} }

		case "enter":
			if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
				item := m.items[m.filtered[m.cursor].index]
				return m, func() tea.Msg {
					return ResultMsg{Value: item.Value, Selected: true}
				}
			}
			// No matching item — return the raw query (for line numbers, etc.)
			query := m.query
			return m, func() tea.Msg { return ResultMsg{Value: query} }

		case "up", "ctrl+p", "ctrl+k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

		case "down", "ctrl+n", "ctrl+j":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
			}
			return m, nil

		case "backspace":
			if len(m.query) > 0 {
				m.query = m.query[:len(m.query)-1]
				m.filter()
			}
			return m, nil

		case "ctrl+u":
			m.query = ""
			m.filter()
			return m, nil

		default:
			// Printable characters.
			if len(msg.String()) == 1 && msg.String()[0] >= 32 {
				m.query += msg.String()
				m.filter()
				return m, nil
			}
		}
	}
	return m, nil
}

// View renders the picker content (no border — the caller handles that).
func (m Model) View() string {
	w := m.width

	var b strings.Builder

	pad := func(line string) string {
		return lipgloss.PlaceHorizontal(w, lipgloss.Left, line)
	}

	// Input line.
	prompt := promptStyle.Render("> ")
	query := queryStyle.Render(m.query + "█")
	b.WriteString(pad(prompt+query) + "\n")

	// Match count line.
	count := countStyle.Render("  " + itoa(len(m.filtered)) + "/" + itoa(len(m.items)))
	b.WriteString(pad(count) + "\n")

	// Visible window of filtered items.
	visCount := m.maxVisible
	if visCount > len(m.filtered) {
		visCount = len(m.filtered)
	}

	// Calculate scroll window to keep cursor visible.
	scrollStart := 0
	if m.cursor >= visCount {
		scrollStart = m.cursor - visCount + 1
	}
	if scrollStart+visCount > len(m.filtered) {
		scrollStart = len(m.filtered) - visCount
	}
	if scrollStart < 0 {
		scrollStart = 0
	}

	for i := scrollStart; i < scrollStart+visCount; i++ {
		si := m.filtered[i]
		item := m.items[si.index]

		label := highlightMatches(item.Label, si.matchPositions, i == m.cursor)
		var line string
		if i == m.cursor {
			pointer := pointerStyle.Render("█")
			line = pointer + " " + label
			if item.Description != "" {
				line += "  " + selectedDesc.Render(item.Description)
			}
		} else {
			line = "  " + label
			if item.Description != "" {
				line += "  " + descStyle.Render(item.Description)
			}
		}
		b.WriteString(pad(line) + "\n")
	}

	// Pad remaining lines if fewer items than maxVisible.
	for i := visCount; i < m.maxVisible; i++ {
		b.WriteString(pad("") + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// ModalHeight returns the total height the picker content needs (for sizing the modal).
func (m Model) ModalHeight() int {
	vis := m.maxVisible
	if vis > len(m.filtered) {
		vis = len(m.filtered)
	}
	if vis < m.maxVisible {
		vis = m.maxVisible
	}
	return vis + 2 // input + blank + items
}

// filter scores and sorts items by fuzzy match against query.
func (m *Model) filter() {
	m.filtered = m.filtered[:0]

	if m.query == "" {
		// No query — show all items in original order.
		for i := range m.items {
			m.filtered = append(m.filtered, scoredItem{index: i, score: 0})
		}
	} else {
		queryLower := strings.ToLower(m.query)

		for i, item := range m.items {
			score, positions := scoreItem(item, queryLower)
			if score > 0 {
				m.filtered = append(m.filtered, scoredItem{index: i, score: score, matchPositions: positions})
			}
		}

		sort.SliceStable(m.filtered, func(a, b int) bool {
			return m.filtered[a].score > m.filtered[b].score
		})
	}

	// Clamp cursor.
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// scoreItem scores an item against a lowercased query.
// Returns score and match positions within the Label (for highlighting).
// Matches each field individually to avoid cross-field false matches.
func scoreItem(item Item, query string) (int, []int) {
	labelLower := strings.ToLower(item.Label)

	// Primary: match the Label (what the user sees).
	labelScore, positions := fuzzyMatch(labelLower, query)

	// Secondary: match other distinct fields for keyword/description matches.
	var altScore int
	seen := map[string]bool{labelLower: true}

	for _, field := range itemAltFields(item) {
		fl := strings.ToLower(field)
		if seen[fl] || fl == "" {
			continue
		}
		seen[fl] = true
		s, _ := fuzzyMatch(fl, query)
		if s > altScore {
			altScore = s
		}
	}

	if labelScore == 0 && altScore == 0 {
		return 0, nil
	}

	// Label match is weighted 2x — it's the visible text.
	score := labelScore*2 + altScore

	// Bonus for contiguous substring match in label.
	if strings.Contains(labelLower, query) {
		score += 20
	}

	return score, positions
}

// itemAltFields returns the non-Label fields of an item for secondary matching.
func itemAltFields(item Item) []string {
	fields := make([]string, 0, 2+len(item.Keywords))
	if item.Description != "" {
		fields = append(fields, item.Description)
	}
	if item.Value != "" {
		fields = append(fields, item.Value)
	}
	fields = append(fields, item.Keywords...)
	return fields
}

// fuzzyMatch finds the best fuzzy subsequence match of query in text.
// Tries multiple starting positions and returns the highest-scoring alignment.
// Rewards: consecutive chars, word-boundary matches, tail position (filename > dir).
// Returns rune-based positions suitable for highlighting the original (pre-lowered) text.
func fuzzyMatch(text, query string) (int, []int) {
	if len(query) == 0 {
		return 1, nil
	}

	textRunes := []rune(text)
	queryRunes := []rune(query)

	// Find all rune positions where query[0] occurs — each is a candidate start.
	var starts []int
	for i, r := range textRunes {
		if r == queryRunes[0] {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		return 0, nil
	}

	var bestScore int
	var bestPositions []int

	for _, start := range starts {
		score, positions := fuzzyMatchFrom(textRunes, queryRunes, start)
		if score > bestScore {
			bestScore = score
			bestPositions = positions
		}
	}

	return bestScore, bestPositions
}

// fuzzyMatchFrom runs a greedy match starting at startIdx for query[0].
// text and query are rune slices; returned positions are rune indices.
func fuzzyMatchFrom(text, query []rune, startIdx int) (int, []int) {
	textLen := len(text)
	queryIdx := 0
	score := 0
	lastMatchPos := -1
	consecutive := 0
	positions := make([]int, 0, len(query))

	for textIdx := startIdx; textIdx < textLen && queryIdx < len(query); textIdx++ {
		if text[textIdx] == query[queryIdx] {
			queryIdx++
			positions = append(positions, textIdx)
			pts := 1

			// Consecutive bonus.
			if lastMatchPos == textIdx-1 {
				consecutive++
				pts += consecutive * 3
			} else {
				consecutive = 0
			}

			// Word boundary bonus (start of text or after separator).
			if textIdx == 0 || text[textIdx-1] == ' ' || text[textIdx-1] == '_' || text[textIdx-1] == '-' || text[textIdx-1] == '/' || text[textIdx-1] == '.' {
				pts += 5
			}

			// Prefix bonus.
			if textIdx == startIdx+queryIdx-1 {
				pts += 3
			}

			// Tail bias: matches closer to the end score higher.
			// For file paths this favors the filename over directories.
			if textLen > 1 {
				pts += 10 * textIdx / (textLen - 1)
			}

			score += pts
			lastMatchPos = textIdx
		}
	}

	if queryIdx < len(query) {
		return 0, nil
	}

	return score, positions
}

// highlightMatches renders a label with matched character positions highlighted.
func highlightMatches(label string, positions []int, selected bool) string {
	if len(positions) == 0 {
		if selected {
			return selectedStyle.Render(label)
		}
		return label
	}

	posSet := make(map[int]bool, len(positions))
	for _, p := range positions {
		posSet[p] = true
	}

	var b strings.Builder
	runeIdx := 0
	for _, ch := range label {
		s := string(ch)
		if posSet[runeIdx] {
			b.WriteString(matchStyle.Render(s))
		} else if selected {
			b.WriteString(selectedStyle.Render(s))
		} else {
			b.WriteString(s)
		}
		runeIdx++
	}
	return b.String()
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
