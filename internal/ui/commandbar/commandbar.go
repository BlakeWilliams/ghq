package commandbar

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// CommandMsg is sent when a command is submitted.
type CommandMsg struct {
	Command string
	Args    []string
}

// CancelledMsg is sent when command mode is cancelled.
type CancelledMsg struct{}

var commands = []string{
	"quit",
	"refresh",
	"back",
}

type Model struct {
	input      textinput.Model
	width      int
	completion string // current ghost completion
}

func New() Model {
	ti := textinput.New()
	ti.Prompt = ":"
	s := ti.Styles()
	promptStyle := lipgloss.NewStyle().Foreground(lipgloss.Magenta).Bold(true)
	s.Focused.Prompt = promptStyle
	s.Blurred.Prompt = promptStyle
	ti.SetStyles(s)
	ti.CharLimit = 128
	return Model{input: ti}
}

func (m *Model) Focus() tea.Cmd {
	return m.input.Focus()
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter":
			raw := strings.TrimSpace(m.input.Value())
			if raw == "" {
				return m, func() tea.Msg { return CancelledMsg{} }
			}
			parts := strings.Fields(raw)
			cmd := CommandMsg{Command: parts[0], Args: parts[1:]}
			m.input.Reset()
			m.completion = ""
			return m, func() tea.Msg { return cmd }
		case "esc":
			m.input.Reset()
			m.completion = ""
			return m, func() tea.Msg { return CancelledMsg{} }
		case "tab":
			if m.completion != "" {
				m.input.SetValue(m.completion)
				m.input.SetCursor(len(m.completion))
				m.completion = ""
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.completion = matchCommand(m.input.Value())
	return m, cmd
}

var ghostStyle = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)

func (m Model) View() string {
	v := m.input.View()
	if m.completion != "" {
		typed := m.input.Value()
		if strings.HasPrefix(m.completion, typed) && m.completion != typed {
			ghost := m.completion[len(typed):]
			v += ghostStyle.Render(ghost)
		}
	}
	return v
}

func (m *Model) SetWidth(w int) {
	m.width = w
	m.input.SetWidth(w - 2)
}

func matchCommand(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	for _, cmd := range commands {
		if strings.HasPrefix(cmd, input) && cmd != input {
			return cmd
		}
	}
	return ""
}
