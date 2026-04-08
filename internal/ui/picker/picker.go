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
	index int
	score int
}

var (
	promptStyle    = lipgloss.NewStyle().Foreground(lipgloss.Magenta).Bold(true)
	queryStyle     = lipgloss.NewStyle()
	descStyle      = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	countStyle     = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	pointerStyle   = lipgloss.NewStyle().Foreground(lipgloss.Yellow).Bold(true)
	selectedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Yellow).Bold(true)
	selectedDesc   = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
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

		if i == m.cursor {
			pointer := pointerStyle.Render("█")
			line := pointer + " " + selectedStyle.Render(item.Label) + "  " + selectedDesc.Render(item.Description)
			b.WriteString(pad(line) + "\n")
		} else {
			line := "  " + item.Label + "  " + descStyle.Render(item.Description)
			b.WriteString(pad(line) + "\n")
		}
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
		queryWords := strings.Fields(queryLower)

		for i, item := range m.items {
			score := scoreItem(item, queryWords, queryLower)
			if score > 0 {
				m.filtered = append(m.filtered, scoredItem{index: i, score: score})
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

// scoreItem returns a fuzzy match score for an item against the query.
// Returns 0 if no match. Uses character-by-character fuzzy matching —
// query chars must appear in order but not contiguously.
func scoreItem(item Item, queryWords []string, queryFull string) int {
	fields := []string{
		strings.ToLower(item.Label),
		strings.ToLower(item.Description),
		strings.ToLower(item.Value),
	}
	for _, kw := range item.Keywords {
		fields = append(fields, strings.ToLower(kw))
	}
	text := strings.Join(fields, " ")

	// Try fuzzy match on the combined text.
	score := fuzzyScore(text, queryFull)
	if score == 0 {
		return 0
	}

	// Bonus for matching the label specifically.
	labelScore := fuzzyScore(strings.ToLower(item.Label), queryFull)
	if labelScore > 0 {
		score += labelScore
	}

	// Bonus for contiguous substring match.
	if strings.Contains(text, queryFull) {
		score += 20
	}

	return score
}

// fuzzyScore checks if all chars in query appear in text in order.
// Returns 0 if no match, higher scores for better matches.
// Rewards: consecutive chars, word-boundary matches, shorter gaps.
func fuzzyScore(text, query string) int {
	if len(query) == 0 {
		return 1
	}

	queryIdx := 0
	score := 0
	lastMatchPos := -1
	consecutive := 0

	for textIdx := 0; textIdx < len(text) && queryIdx < len(query); textIdx++ {
		if text[textIdx] == query[queryIdx] {
			queryIdx++
			pts := 1

			// Consecutive bonus.
			if lastMatchPos == textIdx-1 {
				consecutive++
				pts += consecutive * 3
			} else {
				consecutive = 0
			}

			// Word boundary bonus (start of text or after space/separator).
			if textIdx == 0 || text[textIdx-1] == ' ' || text[textIdx-1] == '_' || text[textIdx-1] == '-' || text[textIdx-1] == '/' {
				pts += 5
			}

			// Prefix bonus.
			if textIdx == queryIdx-1 {
				pts += 3
			}

			score += pts
			lastMatchPos = textIdx
		}
	}

	if queryIdx < len(query) {
		return 0 // not all query chars matched
	}

	return score
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
