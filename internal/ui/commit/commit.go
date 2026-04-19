package commit

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blakewilliams/gg/internal/git"
	"github.com/blakewilliams/gg/internal/review/agents"
)

// Action describes what to do.
type Action int

const (
	ActionCommit     Action = iota // commit only
	ActionCommitPush               // commit + push
	ActionPush                     // push only
	ActionOpenPR                   // open PR (no commit/push)
)

func (a Action) String() string {
	switch a {
	case ActionCommit:
		return "Commit"
	case ActionCommitPush:
		return "Commit & Push"
	case ActionPush:
		return "Push"
	case ActionOpenPR:
		return "Open PR"
	}
	return ""
}

// NeedsCommit returns true if this action requires a commit message.
func (a Action) NeedsCommit() bool {
	return a == ActionCommit || a == ActionCommitPush
}

// NeedsPR returns true if this action opens a pull request.
func (a Action) NeedsPR() bool {
	return a == ActionOpenPR
}

// Config holds configurable prompts for the commit flow.
type Config struct {
	CommitPrompt string
	PRPrompt string
}

// DoneMsg is sent when the commit flow completes successfully.
type DoneMsg struct{ Summary string }

// CancelMsg is sent when the user cancels the commit flow.
type CancelMsg struct{}

// ErrorMsg is sent when a git operation fails.
type ErrorMsg struct{ Err error }

// editorDoneMsg is sent when the external editor finishes.
type editorDoneMsg struct {
	content string
	err     error
}

type tickMsg struct{}

// pushDoneMsg signals commit+push completed, PR step next.
type pushDoneMsg struct{}

type execResultMsg struct {
	err error
}

// phase tracks where we are in the commit flow.
type phase int

const (
	phaseGenerating    phase = iota // generating commit message
	phaseEditing                    // editing commit message
	phaseExecuting                  // running commit + push
	phasePRGenerating               // generating PR description
	phasePREditing                  // editing PR title/body
	phasePRExecuting                // creating PR
)

// Model is the commit flow overlay.
type Model struct {
	action   Action
	repoRoot string
	branch   string
	client   *agents.Client
	cfg      Config
	input    textarea.Model
	width    int
	height   int

	// Flow state.
	phase      phase
	message    *strings.Builder
	spinnerIdx int
	genErr     error
	execErr    error
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// New creates a new commit flow model.
func New(client *agents.Client, action Action, repoRoot, branch string, cfg Config, width, height int) Model {
	ta := textarea.New()
	ta.SetWidth(width - 6)
	ta.SetHeight(3)
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Placeholder = "Commit message..."

	return Model{
		action:   action,
		repoRoot: repoRoot,
		branch:   branch,
		client:   client,
		cfg:      cfg,
		input:    ta,
		width:    width,
		height:   height,
		message:  &strings.Builder{},
		phase:    phaseGenerating,
	}
}

// maxTextareaHeight returns the max textarea rows that keep the modal
// within 4 rows of vertical padding on each side.
// Chrome = border top + blank line above textarea + blank line below + help line + border bottom = 5
func (m Model) maxTextareaHeight() int {
	h := m.height - 8 - 5
	if h < 3 {
		h = 3
	}
	return h
}

func (m *Model) resizeTextarea() {
	val := m.input.Value()
	taWidth := m.width - 6
	if taWidth < 1 {
		taWidth = 1
	}
	// Count visual lines: each hard line wraps at textarea width.
	lines := 0
	for _, line := range strings.Split(val, "\n") {
		w := lipgloss.Width(line)
		if w == 0 {
			lines++
		} else {
			lines += (w + taWidth - 1) / taWidth
		}
	}
	if lines < 3 {
		lines = 3
	}
	max := m.maxTextareaHeight()
	if lines > max {
		lines = max
	}
	m.input.SetHeight(lines)
}

// Title returns the overlay title based on the current phase.
func (m Model) Title() string {
	switch m.phase {
	case phasePRGenerating, phasePREditing, phasePRExecuting:
		return "Open PR"
	default:
		return m.action.String()
	}
}

// Init starts the commit flow.
func (m Model) Init() tea.Cmd {
	if m.action == ActionPush {
		m.phase = phaseExecuting
		return tea.Batch(
			m.executeAction(""),
			tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} }),
		)
	}
	if m.action == ActionOpenPR {
		m.phase = phasePRGenerating
		m.input.Placeholder = "PR description..."
		return tea.Batch(
			m.generatePRDescription(),
			tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} }),
		)
	}
	return tea.Batch(
		m.generateMessage(),
		tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} }),
	)
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		modalW := msg.Width / 2
		if modalW < 60 {
			modalW = 60
		}
		if modalW > msg.Width-4 {
			modalW = msg.Width - 4
		}
		m.width = modalW
		m.height = msg.Height
		m.input.SetWidth(m.width - 6)
		m.resizeTextarea()
		return m, nil

	case tea.KeyPressMsg:
		if m.phase == phaseExecuting || m.phase == phaseGenerating || m.phase == phasePRGenerating || m.phase == phasePRExecuting {
			if msg.String() == "esc" {
				return m, func() tea.Msg { return CancelMsg{} }
			}
			return m, nil
		}

		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return CancelMsg{} }
		case "ctrl+s":
			body := strings.TrimSpace(m.input.Value())
			if body == "" {
				return m, nil
			}
			if m.phase == phasePREditing {
				m.phase = phasePRExecuting
				m.execErr = nil
				return m, tea.Batch(
					m.createPR(body),
					tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} }),
				)
			}
			m.phase = phaseExecuting
			m.execErr = nil
			return m, tea.Batch(
				m.executeAction(body),
				tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} }),
			)
		case "ctrl+e":
			return m, m.openEditor()
		}

		m.input, cmd = m.input.Update(msg)
		m.resizeTextarea()
		return m, cmd

	case tickMsg:
		m.spinnerIdx = (m.spinnerIdx + 1) % len(spinnerFrames)

		if m.phase == phaseGenerating || m.phase == phasePRGenerating {
			if m.client != nil {
				for _, ev := range m.client.Drain() {
					m.handleEvent(ev)
				}
			}

			if m.phase == phaseGenerating || m.phase == phasePRGenerating {
				return m, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
			}
			// Generation finished — populate the textarea.
			m.input.SetValue(strings.TrimSpace(m.message.String()))
			m.resizeTextarea()
			return m, m.input.Focus()
		}
		if m.phase == phaseExecuting || m.phase == phasePRExecuting {
			return m, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
		}
		return m, nil

	case editorDoneMsg:
		if msg.err == nil && msg.content != "" {
			m.input.SetValue(msg.content)
		}
		m.resizeTextarea()
		return m, m.input.Focus()

	case execResultMsg:
		if msg.err != nil {
			// Return to the appropriate editing phase on error.
			if m.phase == phasePRExecuting {
				m.phase = phasePREditing
			} else {
				m.phase = phaseEditing
			}
			m.execErr = msg.err
			return m, m.input.Focus()
		}
		return m, func() tea.Msg {
			return DoneMsg{Summary: m.action.String() + " complete"}
		}
	}

	if m.phase == phaseEditing || m.phase == phasePREditing {
		m.input, cmd = m.input.Update(msg)
		m.resizeTextarea()
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
		if m.phase == phasePRGenerating {
			m.phase = phasePREditing
		} else {
			m.phase = phaseEditing
		}
	case agents.EventError:
		if m.phase == phasePRGenerating {
			m.phase = phasePREditing
		} else {
			m.phase = phaseEditing
		}
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

	if len(diff) > 30000 {
		diff = diff[:30000] + "\n... (truncated)"
	}

	var prompt strings.Builder
	prompt.WriteString("Generate a git commit message for the following staged changes.\n")
	prompt.WriteString("Format: a concise title (max 72 chars) on the first line, then a blank line, then a description.\n")
	prompt.WriteString("Use imperative mood. Do not wrap the description. Focus on why, not what.\n")
	prompt.WriteString("Output ONLY the commit message, no explanation or markdown fences.\n\n")
	if m.cfg.CommitPrompt != "" {
		prompt.WriteString("Additional instructions: " + m.cfg.CommitPrompt + "\n\n")
	}
	if m.branch != "" {
		prompt.WriteString("Branch: " + m.branch + "\n\n")
	}
	prompt.WriteString("```diff\n" + diff + "\n```\n")

	return m.client.SendPrompt("commit-msg", prompt.String())
}

func (m Model) generatePRDescription() tea.Cmd {
	diff, err := git.BranchDiff(m.repoRoot)
	if err != nil {
		diff = ""
	}
	log, err := git.BranchLog(m.repoRoot)
	if err != nil {
		log = ""
	}

	if len(diff) > 30000 {
		diff = diff[:30000] + "\n... (truncated)"
	}

	var prompt strings.Builder
	prompt.WriteString("Generate a pull request title and description.\n")
	prompt.WriteString("Format: a concise PR title on the first line, then a blank line, then a markdown description.\n")
	prompt.WriteString("The description should explain what changed and why. Use markdown formatting.\n")
	prompt.WriteString("Output ONLY the title and description, no explanation or outer markdown fences.\n\n")
	if m.cfg.PRPrompt != "" {
		prompt.WriteString("Additional instructions: " + m.cfg.PRPrompt + "\n\n")
	}
	if m.branch != "" {
		prompt.WriteString("Branch: " + m.branch + "\n\n")
	}
	if log != "" {
		prompt.WriteString("Commits:\n```\n" + log + "```\n\n")
	}
	if diff != "" {
		prompt.WriteString("```diff\n" + diff + "\n```\n")
	}

	return m.client.SendPrompt("pr-description", prompt.String())
}

func (m Model) openEditor() tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}

	tmpFile, err := os.CreateTemp("", "gg-commit-*.md")
	if err != nil {
		return func() tea.Msg { return editorDoneMsg{err: err} }
	}

	if _, err := tmpFile.WriteString(m.input.Value()); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return func() tea.Msg { return editorDoneMsg{err: err} }
	}
	tmpFile.Close()

	tmpPath := tmpFile.Name()
	c := exec.Command(editor, tmpPath)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(tmpPath)
		if err != nil {
			return editorDoneMsg{err: err}
		}
		content, readErr := os.ReadFile(tmpPath)
		if readErr != nil {
			return editorDoneMsg{err: readErr}
		}
		return editorDoneMsg{content: strings.TrimSpace(string(content))}
	})
}

func (m Model) executeAction(message string) tea.Cmd {
	return func() tea.Msg {
		if m.action.NeedsCommit() {
			if err := git.Commit(m.repoRoot, message); err != nil {
				return execResultMsg{err: fmt.Errorf("commit failed: %w", err)}
			}
			if m.action == ActionCommit {
				return execResultMsg{}
			}
		}

		if err := git.Push(m.repoRoot); err != nil {
			return execResultMsg{err: fmt.Errorf("push failed: %w", err)}
		}

		return execResultMsg{}
	}
}

func (m Model) createPR(content string) tea.Cmd {
	return func() tea.Msg {
		title := content
		body := ""
		if idx := strings.Index(content, "\n"); idx >= 0 {
			title = strings.TrimSpace(content[:idx])
			body = strings.TrimSpace(content[idx+1:])
		}

		if err := git.CreatePR(m.repoRoot, title, body); err != nil {
			return execResultMsg{err: fmt.Errorf("PR creation failed: %w", err)}
		}
		return execResultMsg{}
	}
}

// View renders the commit overlay content.
func (m Model) View() string {
	var b strings.Builder

	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Red)

	maxLineW := m.width - 6

	switch m.phase {
	case phaseGenerating:
		spinner := spinnerFrames[m.spinnerIdx]
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  "+spinner+" Generating commit message...") + "\n")
		if m.message.Len() > 0 {
			b.WriteString("\n")
			for _, line := range strings.Split(m.message.String(), "\n") {
				rendered := dimStyle.Render(line)
				if lipgloss.Width(rendered) > maxLineW {
					rendered = lipgloss.NewStyle().MaxWidth(maxLineW).Render(rendered)
				}
				b.WriteString("  " + rendered + "\n")
			}
		}

	case phasePRGenerating:
		spinner := spinnerFrames[m.spinnerIdx]
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  "+spinner+" Generating PR description...") + "\n")
		if m.message.Len() > 0 {
			b.WriteString("\n")
			for _, line := range strings.Split(m.message.String(), "\n") {
				rendered := dimStyle.Render(line)
				if lipgloss.Width(rendered) > maxLineW {
					rendered = lipgloss.NewStyle().MaxWidth(maxLineW).Render(rendered)
				}
				b.WriteString("  " + rendered + "\n")
			}
		}

	case phaseExecuting:
		spinner := spinnerFrames[m.spinnerIdx]
		b.WriteString("\n")
		if m.action.NeedsCommit() {
			b.WriteString(m.input.View() + "\n\n")
		}
		b.WriteString(dimStyle.Render("  "+spinner+" "+m.action.String()+"...") + "\n")

	case phasePRExecuting:
		spinner := spinnerFrames[m.spinnerIdx]
		b.WriteString("\n")
		b.WriteString(m.input.View() + "\n\n")
		b.WriteString(dimStyle.Render("  "+spinner+" Creating PR...") + "\n")

	case phaseEditing, phasePREditing:
		b.WriteString("\n")
		b.WriteString(m.input.View() + "\n\n")
		if m.execErr != nil {
			b.WriteString(errStyle.Render("  "+m.execErr.Error()) + "\n")
		}
		if m.genErr != nil {
			b.WriteString(errStyle.Render("  "+m.genErr.Error()) + "\n")
		}
		btnStyle := lipgloss.NewStyle().Background(lipgloss.Green).Foreground(lipgloss.Black).Bold(true)
		escStyle := lipgloss.NewStyle().Background(lipgloss.White).Foreground(lipgloss.Black)
		btn := btnStyle.Render(" ctrl+s ")
		esc := escStyle.Render(" esc ")
		left := dimStyle.Render("  ctrl+e $EDITOR")
		leftW := lipgloss.Width(left)
		rightBtns := esc + " " + btn
		rightW := lipgloss.Width(rightBtns)
		innerW := m.width - 6
		gap := innerW - leftW - rightW
		if gap < 1 {
			gap = 1
		}
		b.WriteString(left + strings.Repeat(" ", gap) + rightBtns + "\n")
	}

	return b.String()
}
