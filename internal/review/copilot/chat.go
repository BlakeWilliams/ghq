package copilot

import (
	"fmt"
	"strings"
	"time"

	"github.com/blakewilliams/ghq/internal/git"
	"github.com/blakewilliams/ghq/internal/github"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// CloseMsg is sent when the user closes the chat.
type CloseMsg struct{}

type spinnerTickMsg struct{}

// DiffContext provides the copilot with information about the current changes.
type DiffContext struct {
	Files    []github.PullRequestFile
	RepoRoot string
	Branch   string
	PRNumber int
}

type message struct {
	role          string // "you" or "copilot"
	content       string
	tools         []toolCall // tool calls shown within this message
	renderedLines []string   // cached rendered output
	lineCount     int        // cached len(renderedLines)
}

type toolCall struct {
	name string
	done bool
}

// Model is the copilot chat modal.
type ChatModel struct {
	messages    []message
	input       textarea.Model
	client      *Client
	diffCtx     DiffContext
	streaming      string
	sending        bool
	spinnerTick    int
	cachedMaxScroll int
	maxScrollDirty  bool
	activeTools []toolCall // tools currently running
	width       int
	height      int
	scroll      int
	username    string
}

var (
	userMarker   = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true) // magenta
	aiMarker     = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true) // green
	toolMarker   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))           // yellow
	toolDone     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))           // green
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	sepStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	codeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))           // cyan
	boldStyle    = lipgloss.NewStyle().Bold(true)
)

var spinnerFrames = []string{"◐", "◓", "◑", "◒"}

// New creates a copilot chat model.
func NewChat(client *Client, ctx DiffContext, username string, width, height int) ChatModel {
	ta := textarea.New()
	ta.Prompt = ""
	ta.SetWidth(width - 4)
	ta.SetHeight(2)
	ta.ShowLineNumbers = false
	ta.Focus()
	ta.Placeholder = "Ask about your changes..."

	m := ChatModel{
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
	}
	m.cacheMessageRender(0)
	return m
}

// ResumeCmd returns commands needed to restart animations if the chat
// is reopened while still waiting for a response.
func (m ChatModel) ResumeCmd() tea.Cmd {
	if m.sending {
		return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
	}
	return nil
}

func (m ChatModel) Update(msg tea.Msg) (ChatModel, tea.Cmd) {
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
			m.streaming = ""
			m.activeTools = nil
			m.spinnerTick = 0
			m.scroll = m.maxScroll()
			cmds := []tea.Cmd{
				m.sendTocopilot(body),
				tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} }),
			}
			if m.client != nil {
				cmds = append(cmds, m.client.ListenCmd())
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

	case ReplyMsg:
		m.streaming += msg.Content
		m.maxScrollDirty = true
		if msg.Done {
			m.messages = append(m.messages, message{
				role:    "copilot",
				content: m.streaming,
				tools:   m.activeTools,
			})
			m.cacheMessageRender(len(m.messages) - 1)
			m.streaming = ""
			m.sending = false
			m.activeTools = nil
			m.scroll = m.maxScroll()
		} else {
			m.scroll = m.maxScroll()
		}
		cmds := []tea.Cmd{}
		if m.client != nil {
			cmds = append(cmds, m.client.ListenCmd())
		}
		return m, tea.Batch(cmds...)

	case ToolMsg:
		if msg.Done {
			// Mark tool as done.
			for i := range m.activeTools {
				if m.activeTools[i].name == msg.Name && !m.activeTools[i].done {
					m.activeTools[i].done = true
					break
				}
			}
		} else {
			m.activeTools = append(m.activeTools, toolCall{name: msg.Name})
		}
		m.scroll = m.maxScroll()
		cmds := []tea.Cmd{}
		if m.client != nil {
			cmds = append(cmds, m.client.ListenCmd())
		}
		return m, tea.Batch(cmds...)

	case ErrorMsg:
		m.messages = append(m.messages, message{role: "copilot", content: "Error: " + msg.Err.Error()})
		m.cacheMessageRender(len(m.messages) - 1)
		m.streaming = ""
		m.sending = false
		m.activeTools = nil
		cmds := []tea.Cmd{}
		if m.client != nil {
			cmds = append(cmds, m.client.ListenCmd())
		}
		return m, tea.Batch(cmds...)

	case spinnerTickMsg:
		if m.sending {
			m.spinnerTick = (m.spinnerTick + 1) % len(spinnerFrames)
			m.scroll = m.maxScroll()
			return m, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m ChatModel) View() string {
	w := m.width
	msgAreaH := m.height - 5 // input(2) + separator(1) + blank(1) + input label(1)

	// Render all messages (using cached lines).
	var rendered []string
	for i := range m.messages {
		rendered = append(rendered, m.getMessageLines(i)...)
	}

	// Live streaming.
	if m.streaming != "" || m.sending {
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
func (m *ChatModel) cacheMessageRender(idx int) {
	msg := &m.messages[idx]
	if msg.renderedLines == nil {
		msg.renderedLines = m.renderMessage(*msg, m.width)
		msg.lineCount = len(msg.renderedLines)
		m.maxScrollDirty = true
	}
}

// getMessageLines returns cached lines for a message.
func (m ChatModel) getMessageLines(idx int) []string {
	if m.messages[idx].renderedLines != nil {
		return m.messages[idx].renderedLines
	}
	// Fallback: render on the fly (shouldn't happen if caching is done properly).
	return m.renderMessage(m.messages[idx], m.width)
}

func (m ChatModel) renderMessage(msg message, width int) []string {
	bodyW := width - 2
	if bodyW < 20 {
		bodyW = 20
	}

	var lines []string

	if msg.role == "you" {
		// User message: ❯ content
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
		// AI message: tool calls + ● content
		lines = append(lines, "")

		// Tool calls first.
		for _, tc := range msg.tools {
			if tc.done {
				lines = append(lines, "  "+toolDone.Render("●")+" "+dimStyle.Render(tc.name))
			} else {
				lines = append(lines, "  "+toolMarker.Render("○")+" "+dimStyle.Render(tc.name))
			}
		}
		if len(msg.tools) > 0 {
			lines = append(lines, "")
		}

		// Message content with markdown.
		mdLines := renderMarkdown(msg.content, bodyW)
		for i, ml := range mdLines {
			if i == 0 {
				lines = append(lines, aiMarker.Render("●")+" "+ml)
			} else {
				lines = append(lines, "  "+ml)
			}
		}
	}

	return lines
}

func (m ChatModel) renderLive(width int) []string {
	bodyW := width - 2
	var lines []string
	lines = append(lines, "")

	// Active tool calls.
	for _, tc := range m.activeTools {
		if tc.done {
			lines = append(lines, "  "+toolDone.Render("●")+" "+dimStyle.Render(tc.name))
		} else {
			spinner := toolMarker.Render(spinnerFrames[m.spinnerTick])
			lines = append(lines, "  "+spinner+" "+dimStyle.Render(tc.name))
		}
	}

	// Streaming text or thinking.
	if m.streaming != "" {
		if len(m.activeTools) > 0 {
			lines = append(lines, "")
		}
		mdLines := renderMarkdown(m.streaming, bodyW)
		for i, ml := range mdLines {
			if i == 0 {
				lines = append(lines, aiMarker.Render("●")+" "+ml)
			} else {
				lines = append(lines, "  "+ml)
			}
		}
	} else if len(m.activeTools) == 0 {
		spinner := aiMarker.Render(spinnerFrames[m.spinnerTick])
		lines = append(lines, spinner+" "+dimStyle.Render("thinking..."))
	}

	return lines
}

// renderMarkdown does lightweight markdown rendering for chat.
func renderMarkdown(body string, width int) []string {
	var lines []string
	inCodeBlock := false

	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "```") {
			inCodeBlock = !inCodeBlock
			if inCodeBlock {
				lines = append(lines, dimStyle.Render("```"+strings.TrimPrefix(line, "```")))
			} else {
				lines = append(lines, dimStyle.Render("```"))
			}
			continue
		}
		if inCodeBlock {
			lines = append(lines, codeStyle.Render(line))
			continue
		}

		// Headings.
		if strings.HasPrefix(line, "### ") {
			lines = append(lines, boldStyle.Render(strings.TrimPrefix(line, "### ")))
			continue
		}
		if strings.HasPrefix(line, "## ") {
			lines = append(lines, boldStyle.Render(strings.TrimPrefix(line, "## ")))
			continue
		}
		if strings.HasPrefix(line, "# ") {
			lines = append(lines, boldStyle.Render(strings.TrimPrefix(line, "# ")))
			continue
		}

		// List items.
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
			bullet := "• " + line[2:]
			for _, wl := range wordWrap(bullet, width) {
				lines = append(lines, renderInline(wl))
			}
			continue
		}

		// Normal text with wrapping + inline formatting.
		for _, wl := range wordWrap(line, width) {
			lines = append(lines, renderInline(wl))
		}
	}

	return lines
}

// renderInline applies bold, italic, inline code formatting.
func renderInline(line string) string {
	// Inline code: `code`
	for {
		start := strings.Index(line, "`")
		if start < 0 {
			break
		}
		end := strings.Index(line[start+1:], "`")
		if end < 0 {
			break
		}
		end += start + 1
		code := line[start+1 : end]
		line = line[:start] + codeStyle.Render(code) + line[end+1:]
	}
	// Bold: **text**
	line = replacePair(line, "**", "\033[1m", "\033[0m")
	// Italic: *text*
	line = replacePair(line, "*", "\033[3m", "\033[0m")
	return line
}

func replacePair(s, delim, open, close string) string {
	for {
		start := strings.Index(s, delim)
		if start < 0 {
			break
		}
		end := strings.Index(s[start+len(delim):], delim)
		if end < 0 {
			break
		}
		end += start + len(delim)
		inner := s[start+len(delim) : end]
		s = s[:start] + open + inner + close + s[end+len(delim):]
	}
	return s
}

func (m *ChatModel) maxScroll() int {
	if !m.maxScrollDirty {
		return m.cachedMaxScroll
	}

	msgAreaH := m.height - 5
	total := 0
	// Use cached line counts for completed messages — O(N) sum, not O(N²) re-render.
	for _, msg := range m.messages {
		if msg.lineCount > 0 {
			total += msg.lineCount
		} else {
			total += len(m.renderMessage(msg, m.width))
		}
	}
	if m.streaming != "" || m.sending {
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

func (m ChatModel) sendTocopilot(body string) tea.Cmd {
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
	return m.client.SendComment("chat", body, "", "", "", prompt, nil)
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
