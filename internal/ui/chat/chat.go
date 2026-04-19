package chat

import (
	"fmt"
	"strings"
	"time"

	"github.com/blakewilliams/gg/internal/git"
	"github.com/blakewilliams/gg/internal/github"
	"github.com/blakewilliams/gg/internal/review/agents"
	"github.com/blakewilliams/gg/internal/review/comments"
	"github.com/blakewilliams/gg/internal/ui/markdown"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// CloseMsg is sent when the user closes the chat.
type CloseMsg struct{}

type spinnerTickMsg struct{}

// DiffContext provides the agent with information about the current changes.
type DiffContext struct {
	Files    []github.PullRequestFile
	RepoRoot string
	Branch   string
	PRNumber int
}

type message struct {
	role          string                 // "you" or "copilot"
	content       string                 // plain-text body (user messages + fallback)
	blocks        []comments.ContentBlock // structured content (copilot messages)
	renderedLines []string               // cached rendered output
	lineCount     int                    // cached len(renderedLines)
}

// Model is the chat modal.
type Model struct {
	messages       []message
	input          textarea.Model
	client         *agents.Client
	diffCtx        DiffContext
	streamBlocks   []comments.ContentBlock // live streaming content blocks
	sending        bool
	spinnerTick    int
	cachedMaxScroll int
	maxScrollDirty  bool
	mdRenderer     *markdown.Renderer
	width          int
	height         int
	scroll         int
	username       string
}

var (
	userMarker = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true) // magenta
	aiMarker   = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true) // green
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	sepStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// New creates a chat model.
func New(client *agents.Client, ctx DiffContext, username string, width, height int) Model {
	ta := textarea.New()
	ta.Prompt = ""
	ta.SetWidth(width - 4)
	ta.SetHeight(2)
	ta.ShowLineNumbers = false
	ta.Focus()
	ta.Placeholder = "Ask about your changes..."

	m := Model{
		messages: []message{
			{role: "copilot", content: "How can I help?"},
		},
		input:          ta,
		client:         client,
		diffCtx:        ctx,
		width:          width,
		height:         height,
		username:       username,
		maxScrollDirty: true,
		mdRenderer:     markdown.NewRenderer(nil),
	}
	m.cacheMessageRender(0)
	return m
}

// ResumeCmd returns commands needed to restart animations if the chat
// is reopened while still waiting for a response.
func (m Model) ResumeCmd() tea.Cmd {
	if m.sending {
		return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
	}
	return nil
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return CloseMsg{} }
		case "enter":
			if m.sending {
				return m, nil
			}
			body := strings.TrimSpace(m.input.Value())
			if body == "" {
				return m, nil
			}
			m.messages = append(m.messages, message{role: "you", content: body})
			m.cacheMessageRender(len(m.messages) - 1)
			m.input.Reset()
			m.sending = true
			m.streamBlocks = nil
			m.spinnerTick = 0
			m.scroll = m.maxScroll()
			cmds := []tea.Cmd{
				m.sendToAgent(body),
				tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} }),
			}
			return m, tea.Batch(cmds...)
		case "shift+enter":
			m.input.InsertString("\n")
			return m, nil
		case "ctrl+d":
			m.scroll += m.height / 4
			if m.scroll > m.maxScroll() {
				m.scroll = m.maxScroll()
			}
			return m, nil
		case "ctrl+u":
			m.scroll -= m.height / 4
			if m.scroll < 0 {
				m.scroll = 0
			}
			return m, nil
		}

	case spinnerTickMsg:
		if m.sending {
			m.spinnerTick = (m.spinnerTick + 1) % len(spinnerFrames)

			// Drain all buffered events from the agent.
			if m.client != nil {
				for _, ev := range m.client.Drain() {
					m.handleAgentEvent(ev)
				}
			}

			m.scroll = m.maxScroll()
			return m, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handleAgentEvent processes a single event from the agent's Drain().
func (m *Model) handleAgentEvent(ev agents.AgentEvent) {
	switch ev.Kind {
	case agents.EventDelta:
		if ev.CommentID != "chat" {
			return
		}
		p := ev.Payload.(agents.DeltaPayload)
		blocks := m.streamBlocks
		if n := len(blocks); n > 0 {
			if tb, ok := blocks[n-1].(comments.TextBlock); ok {
				blocks[n-1] = comments.TextBlock{Text: tb.Text + p.Delta}
			} else {
				blocks = append(blocks, comments.TextBlock{Text: p.Delta})
			}
		} else {
			blocks = append(blocks, comments.TextBlock{Text: p.Delta})
		}
		m.streamBlocks = blocks
		m.maxScrollDirty = true

	case agents.EventDone:
		if ev.CommentID != "chat" {
			return
		}
		body := comments.BodyFromBlocks(m.streamBlocks)
		m.messages = append(m.messages, message{
			role:    "copilot",
			content: body,
			blocks:  m.streamBlocks,
		})
		m.cacheMessageRender(len(m.messages) - 1)
		m.streamBlocks = nil
		m.sending = false
		m.scroll = m.maxScroll()

	case agents.EventToolStart:
		if ev.CommentID != "chat" {
			return
		}
		p := ev.Payload.(agents.ToolPayload)
		if p.Name == "report_intent" {
			blocks := m.streamBlocks
			if n := len(blocks); n > 0 {
				if tg, ok := blocks[n-1].(comments.ToolGroupBlock); ok && p.Arguments != "" {
					tg.Label = p.Arguments
					blocks[n-1] = tg
					m.streamBlocks = blocks
				}
			}
			return
		}
		tc := comments.ToolCall{Name: p.Name, CallID: p.CallID, Status: "running", Arguments: p.Arguments}
		blocks := m.streamBlocks
		if n := len(blocks); n > 0 {
			if tg, ok := blocks[n-1].(comments.ToolGroupBlock); ok {
				tg.Tools = append(tg.Tools, tc)
				blocks[n-1] = tg
			} else {
				blocks = append(blocks, comments.ToolGroupBlock{Tools: []comments.ToolCall{tc}})
			}
		} else {
			blocks = append(blocks, comments.ToolGroupBlock{Tools: []comments.ToolCall{tc}})
		}
		m.streamBlocks = blocks
		m.maxScrollDirty = true

	case agents.EventToolComplete:
		if ev.CommentID != "chat" {
			return
		}
		p := ev.Payload.(agents.ToolPayload)
		if p.Name == "report_intent" {
			return
		}
		blocks := m.streamBlocks
		for i := len(blocks) - 1; i >= 0; i-- {
			if tg, ok := blocks[i].(comments.ToolGroupBlock); ok {
				for j := range tg.Tools {
					if tg.Tools[j].Status != "running" {
						continue
					}
					matched := false
					if p.CallID != "" && tg.Tools[j].CallID == p.CallID {
						matched = true
					} else if p.CallID == "" && tg.Tools[j].Name == p.Name {
						matched = true
					}
					if matched {
						tg.Tools[j].Status = "done"
						blocks[i] = tg
						m.streamBlocks = blocks
						return
					}
				}
			}
		}
		m.maxScrollDirty = true

	case agents.EventError:
		if ev.CommentID != "chat" {
			return
		}
		p := ev.Payload.(agents.ErrorPayload)
		m.messages = append(m.messages, message{role: "copilot", content: "Error: " + p.Err.Error()})
		m.cacheMessageRender(len(m.messages) - 1)
		m.streamBlocks = nil
		m.sending = false
	}
}

func (m Model) View() string {
	w := m.width
	msgAreaH := m.height - 5 // input(2) + separator(1) + blank(1) + input label(1)

	// Render all messages (using cached lines).
	var rendered []string
	for i := range m.messages {
		rendered = append(rendered, m.getMessageLines(i)...)
	}

	// Live streaming.
	if len(m.streamBlocks) > 0 || m.sending {
		rendered = append(rendered, m.renderLive(w)...)
	}

	// Scroll.
	totalLines := len(rendered)
	start := m.scroll
	if start > totalLines-msgAreaH {
		start = totalLines - msgAreaH
	}
	if start < 0 {
		start = 0
	}
	end := start + msgAreaH
	if end > totalLines {
		end = totalLines
	}

	var b strings.Builder
	linesWritten := 0
	for i := start; i < end; i++ {
		line := rendered[i]
		lineW := lipgloss.Width(line)
		if lineW < w {
			line += strings.Repeat(" ", w-lineW)
		}
		b.WriteString(line + "\n")
		linesWritten++
	}
	for i := linesWritten; i < msgAreaH; i++ {
		b.WriteString(strings.Repeat(" ", w) + "\n")
	}

	// Separator line.
	b.WriteString(sepStyle.Render(strings.Repeat("─", w)) + "\n")

	// Input with > prompt.
	promptLine := userMarker.Render(">") + " "
	taView := m.input.View()
	for i, line := range strings.Split(taView, "\n") {
		if i == 0 {
			line = promptLine + line
		} else {
			line = "  " + line
		}
		lineW := lipgloss.Width(line)
		if lineW < w {
			line += strings.Repeat(" ", w-lineW)
		}
		b.WriteString(line + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// cacheMessageRender pre-computes and caches a message's rendered lines.
func (m *Model) cacheMessageRender(idx int) {
	msg := &m.messages[idx]
	if msg.renderedLines == nil {
		msg.renderedLines = m.renderMessage(*msg, m.width)
		msg.lineCount = len(msg.renderedLines)
		m.maxScrollDirty = true
	}
}

// getMessageLines returns cached lines for a message.
func (m Model) getMessageLines(idx int) []string {
	if m.messages[idx].renderedLines != nil {
		return m.messages[idx].renderedLines
	}
	return m.renderMessage(m.messages[idx], m.width)
}

func (m Model) renderMessage(msg message, width int) []string {
	bodyW := width - 2
	if bodyW < 20 {
		bodyW = 20
	}

	var lines []string

	if msg.role == "you" {
		lines = append(lines, "")
		bodyLines := strings.Split(msg.content, "\n")
		for i, fl := range bodyLines {
			wrapped := wordWrap(fl, bodyW)
			for j, wl := range wrapped {
				if i == 0 && j == 0 {
					lines = append(lines, dimStyle.Render("❯")+" "+wl)
				} else {
					lines = append(lines, "  "+wl)
				}
			}
		}
	} else {
		lines = append(lines, "")

		blocks := comments.NormalizedBlocks(msg.blocks, msg.content)
		firstText := true
		for _, block := range blocks {
			switch b := block.(type) {
			case comments.ToolGroupBlock:
				toolLines := m.renderChatToolGroup(b, bodyW)
				for _, tl := range toolLines {
					lines = append(lines, "  "+tl)
				}
				if len(toolLines) > 0 {
					lines = append(lines, "")
				}
			case comments.TextBlock:
				if b.Text == "" {
					continue
				}
				rendered := m.mdRenderer.RenderBody(b.Text, bodyW, "")
				for _, ml := range strings.Split(rendered, "\n") {
					if firstText {
						lines = append(lines, aiMarker.Render("●")+" "+ml)
						firstText = false
					} else {
						lines = append(lines, "  "+ml)
					}
				}
			}
		}
	}

	return lines
}

func (m Model) renderLive(width int) []string {
	bodyW := width - 2
	var lines []string
	lines = append(lines, "")

	firstText := true
	for _, block := range m.streamBlocks {
		switch b := block.(type) {
		case comments.ToolGroupBlock:
			toolLines := m.renderChatToolGroup(b, bodyW)
			for _, tl := range toolLines {
				lines = append(lines, "  "+tl)
			}
			if len(toolLines) > 0 {
				lines = append(lines, "")
			}
		case comments.TextBlock:
			if b.Text == "" {
				continue
			}
			rendered := m.mdRenderer.RenderBody(b.Text, bodyW, "")
			for _, ml := range strings.Split(rendered, "\n") {
				if firstText {
					lines = append(lines, aiMarker.Render("●")+" "+ml)
					firstText = false
				} else {
					lines = append(lines, "  "+ml)
				}
			}
		}
	}

	// No content yet — show thinking spinner.
	if firstText {
		spinner := aiMarker.Render(spinnerFrames[m.spinnerTick%len(spinnerFrames)])
		lines = append(lines, spinner+" "+dimStyle.Render("thinking..."))
	}

	return lines
}

// renderChatToolGroup renders a tool group as a bordered sub-box,
// matching the visual style used in diff-view comment threads.
func (m Model) renderChatToolGroup(group comments.ToolGroupBlock, width int) []string {
	if len(group.Tools) == 0 {
		return nil
	}

	var toolBorderFg string
	switch group.ToolGroupStatus() {
	case "running":
		toolBorderFg = "\033[33m" // yellow
	case "failed":
		toolBorderFg = "\033[31m" // red
	default:
		toolBorderFg = "\033[32m" // green
	}
	dimFg := "\033[90m"
	yellowFg := "\033[33m"
	reset := "\033[0m"

	boxW := width
	if boxW < 10 {
		boxW = 10
	}
	innerW := boxW - 4 // "│ " + " │"
	if innerW < 6 {
		innerW = 6
	}

	var lines []string

	// Top border: ╭ Label ───╮  or  ╭──────────╮
	if group.Label != "" {
		label := " " + group.Label + " "
		labelW := lipgloss.Width(label)
		fill := boxW - 2 - labelW
		if fill < 0 {
			fill = 0
		}
		lines = append(lines, toolBorderFg+"╭"+yellowFg+label+toolBorderFg+strings.Repeat("─", fill)+"╮"+reset)
	} else {
		fill := boxW - 2
		if fill < 0 {
			fill = 0
		}
		lines = append(lines, toolBorderFg+"╭"+strings.Repeat("─", fill)+"╮"+reset)
	}

	// Tool rows: │ ● name  args │
	for _, tc := range group.Tools {
		var marker string
		switch tc.Status {
		case "done":
			marker = "\033[32m●" + reset
		case "failed":
			marker = "\033[31m✕" + reset
		default:
			marker = "\033[33m" + spinnerFrames[m.spinnerTick%len(spinnerFrames)] + reset
		}

		name := tc.Name
		nameW := lipgloss.Width(name)
		maxNameW := innerW - 2 // "● " takes 2
		if nameW > maxNameW {
			name = name[:maxNameW-1] + "…"
			nameW = maxNameW
		}

		var rowContent string
		if tc.Arguments != "" {
			argsSpace := innerW - 2 - nameW - 1
			if argsSpace > 3 {
				args := tc.Arguments
				argsW := lipgloss.Width(args)
				if argsW > argsSpace {
					args = args[:argsSpace-1] + "…"
				}
				rowContent = marker + " " + dimFg + name + reset + " " + dimFg + args + reset
			} else {
				rowContent = marker + " " + dimFg + name + reset
			}
		} else {
			rowContent = marker + " " + dimFg + name + reset
		}

		rowVisW := lipgloss.Width(rowContent)
		rowPad := innerW - rowVisW
		if rowPad < 0 {
			rowPad = 0
		}
		lines = append(lines, toolBorderFg+"│"+reset+" "+rowContent+strings.Repeat(" ", rowPad)+" "+toolBorderFg+"│"+reset)
	}

	// Bottom border: ╰───────╯
	fill := boxW - 2
	if fill < 0 {
		fill = 0
	}
	lines = append(lines, toolBorderFg+"╰"+strings.Repeat("─", fill)+"╯"+reset)

	return lines
}

func (m *Model) maxScroll() int {
	if !m.maxScrollDirty {
		return m.cachedMaxScroll
	}

	msgAreaH := m.height - 5
	total := 0
	for _, msg := range m.messages {
		if msg.lineCount > 0 {
			total += msg.lineCount
		} else {
			total += len(m.renderMessage(msg, m.width))
		}
	}
	if len(m.streamBlocks) > 0 || m.sending {
		total += len(m.renderLive(m.width))
	}
	max := total - msgAreaH
	if max < 0 {
		max = 0
	}
	m.cachedMaxScroll = max
	m.maxScrollDirty = false
	return max
}

func (m Model) sendToAgent(body string) tea.Cmd {
	var ctx strings.Builder
	ctx.WriteString("You are helping review code changes. Respond concisely.\n\n")

	if m.diffCtx.Branch != "" {
		ctx.WriteString("Branch: " + m.diffCtx.Branch + "\n")
	}
	if m.diffCtx.PRNumber > 0 {
		ctx.WriteString(fmt.Sprintf("PR #%d\n", m.diffCtx.PRNumber))
	}
	ctx.WriteString("\n")

	for _, f := range m.diffCtx.Files {
		if f.Patch == "" {
			continue
		}
		ctx.WriteString(fmt.Sprintf("File: %s (%s)\n```diff\n%s\n```\n\n", f.Filename, f.Status, f.Patch))
	}

	if m.diffCtx.RepoRoot != "" {
		for _, f := range m.diffCtx.Files {
			if f.Status == "removed" {
				continue
			}
			content, err := git.FileContent(m.diffCtx.RepoRoot, f.Filename)
			if err == nil && len(content) < 50000 {
				ctx.WriteString(fmt.Sprintf("Full file %s:\n```\n%s\n```\n\n", f.Filename, content))
			}
		}
	}

	if len(m.messages) > 1 {
		ctx.WriteString("Previous conversation:\n")
		for _, msg := range m.messages[:len(m.messages)-1] {
			ctx.WriteString(fmt.Sprintf("%s: %s\n", msg.role, msg.content))
		}
		ctx.WriteString("\n")
	}

	prompt := ctx.String() + "User: " + body
	return m.client.SendPrompt("chat", prompt)
}

func wordWrap(line string, width int) []string {
	if width <= 0 || len(line) <= width {
		return []string{line}
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		if len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
		} else {
			cur += " " + w
		}
	}
	lines = append(lines, cur)
	return lines
}
