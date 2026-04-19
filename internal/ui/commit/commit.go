package commit

import (
	"fmt"
	"strings"
	"time"

	"github.com/blakewilliams/gg/internal/git"
	"github.com/blakewilliams/gg/internal/review/agents"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Action describes what to do after the commit.
type Action int

const (
	ActionCommit       Action = iota // commit only
	ActionCommitPush                 // commit + push
	ActionCommitPushPR               // commit + push + open PR
)

func (a Action) String() string {
	switch a {
	case ActionCommit:
		return "Commit"
	case ActionCommitPush:
		return "Commit & Push"
	case ActionCommitPushPR:
		return "Commit, Push & Open PR"
	}
	return ""
}

// DoneMsg is sent when the commit flow completes successfully.
type DoneMsg struct{ Summary string }

// CancelMsg is sent when the user cancels the commit flow.
type CancelMsg struct{}

// ErrorMsg is sent when a git operation fails.
type ErrorMsg struct{ Err error }

type tickMsg struct{}

type execResultMsg struct {
	err error
}

// Model is the commit flow overlay.
type Model struct {
	action    Action
	repoRoot  string
	branch    string
	client    *agents.Client
	input     textarea.Model
	width     int
	height    int

	// Streaming state.
	streaming  bool
	message    strings.Builder
	spinnerIdx int
	done       bool
	genErr     error

	// Execution state.
	executing    bool
	execStep     string
	execErr      error
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// New creates a new commit flow model.
func New(client *agents.Client, action Action, repoRoot, branch string, width, height int) Model {
	ta := textarea.New()
	ta.SetWidth(width - 4)
	ta.SetHeight(height/2 - 2)
	ta.CharLimit = 0
	ta.Placeholder = "Commit message..."

	return Model{
		action:   action,
		repoRoot: repoRoot,
		branch:   branch,
		client:   client,
		input:    ta,
		width:    width,
		height:   height,
	}
}

// Init starts the commit message generation.
func (m Model) Init() tea.Cmd {
	m.streaming = true
	return tea.Batch(
		m.generateMessage(),
		tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} }),
	)
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if m.executing {
			return m, nil
		}

		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return CancelMsg{} }
		case "ctrl+s":
			if m.streaming || m.executing {
				return m, nil
			}
			body := strings.TrimSpace(m.input.Value())
			if body == "" {
				return m, nil
			}
			m.executing = true
			return m, m.executeAction(body)
		}

		if !m.streaming {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		return m, nil

	case tickMsg:
		if m.streaming {
			m.spinnerIdx = (m.spinnerIdx + 1) % len(spinnerFrames)

			if m.client != nil {
				for _, ev := range m.client.Drain() {
					m.handleEvent(ev)
				}
			}

			if m.streaming {
				return m, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
			}
			// Generation finished — populate the textarea.
			m.input.SetValue(m.message.String())
			m.input.Focus()
			return m, m.input.Focus()
		}
		return m, nil

	case execResultMsg:
		if msg.err != nil {
			m.executing = false
			m.execErr = msg.err
			return m, nil
		}
		return m, func() tea.Msg {
			return DoneMsg{Summary: m.action.String() + " complete"}
		}
	}

	if !m.streaming && !m.executing {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleEvent(ev agents.AgentEvent) {
	switch ev.Kind {
	case agents.EventDelta:
		if p, ok := ev.Payload.(agents.DeltaPayload); ok {
			m.message.WriteString(p.Delta)
		}
	case agents.EventDone:
		m.streaming = false
		m.done = true
	case agents.EventError:
		m.streaming = false
		if p, ok := ev.Payload.(agents.ErrorPayload); ok {
			m.genErr = p.Err
		}
	}
}

func (m Model) generateMessage() tea.Cmd {
	diff, err := git.StagedDiff(m.repoRoot)
	if err != nil {
		return func() tea.Msg {
			return ErrorMsg{Err: fmt.Errorf("failed to get staged diff: %w", err)}
		}
	}

	// Truncate very large diffs to avoid overwhelming the model.
	if len(diff) > 30000 {
		diff = diff[:30000] + "\n... (truncated)"
	}

	var prompt strings.Builder
	prompt.WriteString("Generate a git commit message for the following staged changes. ")
	prompt.WriteString("Use conventional commit format. Write a concise title (max 72 chars) on the first line. ")
	prompt.WriteString("If the changes warrant it, add a blank line followed by a brief body. ")
	prompt.WriteString("Output ONLY the commit message, no explanation or markdown fences.\n\n")
	if m.branch != "" {
		prompt.WriteString("Branch: " + m.branch + "\n\n")
	}
	prompt.WriteString("```diff\n" + diff + "\n```\n")

	return m.client.SendPrompt("commit-msg", prompt.String())
}

func (m Model) executeAction(message string) tea.Cmd {
	return func() tea.Msg {
		// Split message into title and body for PR creation.
		title := message
		body := ""
		if idx := strings.Index(message, "\n"); idx >= 0 {
			title = strings.TrimSpace(message[:idx])
			body = strings.TrimSpace(message[idx+1:])
		}

		// Step 1: Commit
		if err := git.Commit(m.repoRoot, message); err != nil {
			return execResultMsg{err: fmt.Errorf("commit failed: %w", err)}
		}

		if m.action == ActionCommit {
			return execResultMsg{}
		}

		// Step 2: Push
		if err := git.Push(m.repoRoot); err != nil {
			return execResultMsg{err: fmt.Errorf("push failed: %w", err)}
		}

		if m.action == ActionCommitPush {
			return execResultMsg{}
		}

		// Step 3: Open PR
		if err := git.CreatePR(m.repoRoot, title, body); err != nil {
			return execResultMsg{err: fmt.Errorf("PR creation failed: %w", err)}
		}

		return execResultMsg{}
	}
}

// View renders the commit overlay.
func (m Model) View() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.BrightWhite)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Red)

	b.WriteString(titleStyle.Render("  "+m.action.String()) + "\n")
	b.WriteString(dimStyle.Render("  "+m.branch) + "\n\n")

	if m.streaming {
		spinner := spinnerFrames[m.spinnerIdx]
		b.WriteString(dimStyle.Render("  "+spinner+" Generating commit message...") + "\n\n")
		// Show what's been generated so far.
		if m.message.Len() > 0 {
			for _, line := range strings.Split(m.message.String(), "\n") {
				b.WriteString("  " + dimStyle.Render(line) + "\n")
			}
		}
	} else if m.genErr != nil {
		b.WriteString(errStyle.Render("  Error: "+m.genErr.Error()) + "\n")
	} else if m.executing {
		spinner := spinnerFrames[m.spinnerIdx]
		b.WriteString(dimStyle.Render("  "+spinner+" Executing...") + "\n")
	} else if m.execErr != nil {
		b.WriteString(m.input.View() + "\n\n")
		b.WriteString(errStyle.Render("  Error: "+m.execErr.Error()) + "\n")
		b.WriteString(dimStyle.Render("  ctrl+s to retry • esc to cancel") + "\n")
	} else {
		b.WriteString(m.input.View() + "\n\n")
		b.WriteString(dimStyle.Render("  ctrl+s to confirm • esc to cancel") + "\n")
	}

	return b.String()
}
