package agents

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// EventKind identifies the type of agent event.
type EventKind int

const (
	EventDelta        EventKind = iota // Streaming text content
	EventToolStart                     // Tool execution began
	EventToolComplete                  // Tool execution finished
	EventDone                          // Agent finished responding
	EventError                         // Agent encountered an error
)

// EventPayload is a sealed interface for event-specific data.
// Only payload types in this package may implement it.
type EventPayload interface {
	agentPayload()
}

// DeltaPayload carries incremental text content.
type DeltaPayload struct {
	Delta string
}

func (DeltaPayload) agentPayload() {}

// ToolPayload carries the name, arguments, and call ID of a tool invocation.
type ToolPayload struct {
	Name       string
	CallID     string // unique tool call ID for correlating start/complete
	Arguments  string // compact display string (may be empty)
}

func (ToolPayload) agentPayload() {}

// ErrorPayload carries an error from the agent.
type ErrorPayload struct {
	Err error
}

func (ErrorPayload) agentPayload() {}

// AgentEvent is a single event emitted by an agent.
type AgentEvent struct {
	CommentID string
	Kind      EventKind
	Payload   EventPayload
}

// Agent defines the interface for AI-powered code review backends.
// Implementations handle SDK communication and buffer events internally.
type Agent interface {
	// Drain returns all buffered events without blocking.
	// Returns nil when no events are pending.
	Drain() []AgentEvent

	// Send sends a prompt to the agent. It blocks until the prompt
	// is dispatched (streaming results arrive via Drain). Returns an error
	// if the agent cannot accept the request.
	Send(commentID, prompt string) error

	// Stop shuts down the agent and releases resources.
	Stop()
}

// Client wraps an Agent implementation and provides Bubble Tea integration.
// It builds review prompts and converts agent operations into tea.Cmds.
type Client struct {
	agent Agent
}

// New creates a Client that wraps the given Agent implementation.
func New(agent Agent) *Client {
	return &Client{agent: agent}
}

// SendComment builds a review prompt from the given context and sends it
// to the underlying agent. Returns a tea.Cmd that handles the blocking call.
func (c *Client) SendComment(commentID, body, filePath, diffMode, diffHunk string, threadHistory []string) tea.Cmd {
	prompt := buildReviewPrompt(body, filePath, diffMode, diffHunk, threadHistory)
	return c.SendPrompt(commentID, prompt)
}

// SendPrompt sends a pre-built prompt to the underlying agent.
// Returns a tea.Cmd that handles the blocking call.
func (c *Client) SendPrompt(commentID, prompt string) tea.Cmd {
	return func() tea.Msg {
		if err := c.agent.Send(commentID, prompt); err != nil {
			return AgentEvent{CommentID: commentID, Kind: EventError, Payload: ErrorPayload{Err: err}}
		}
		return nil
	}
}

// Send sends a pre-built prompt synchronously. Use this when already inside
// a tea.Cmd goroutine instead of creating a nested Cmd via SendPrompt.
func (c *Client) Send(commentID, prompt string) error {
	return c.agent.Send(commentID, prompt)
}

// Drain returns all buffered events from the underlying agent.
func (c *Client) Drain() []AgentEvent {
	return c.agent.Drain()
}

// Stop shuts down the underlying agent.
func (c *Client) Stop() {
	c.agent.Stop()
}

// buildReviewPrompt constructs a system prompt for an AI code review agent.
func buildReviewPrompt(comment, filePath, diffMode, diffHunk string, threadHistory []string) string {
	var b strings.Builder
	b.WriteString("You are reviewing local code changes. Respond concisely to the developer's comment about this diff.\n\n")
	b.WriteString("File: " + filePath + "\n")

	switch diffMode {
	case "Unstaged":
		b.WriteString("Diff mode: unstaged changes (working tree vs index).\n")
		b.WriteString("To get the full diff: `git diff -- " + filePath + "`\n")
		b.WriteString("To read the working tree version: `cat " + filePath + "`\n")
	case "Staged":
		b.WriteString("Diff mode: staged changes (index vs HEAD).\n")
		b.WriteString("To get the full diff: `git diff --cached -- " + filePath + "`\n")
		b.WriteString("To read the staged version: `git show :0:" + filePath + "`\n")
	case "Branch":
		b.WriteString("Diff mode: branch changes (current branch vs default branch merge-base).\n")
		b.WriteString("To get the full diff: `git diff $(git merge-base origin/HEAD HEAD) -- " + filePath + "`\n")
		b.WriteString("To read the current version: `cat " + filePath + "`\n")
	}
	b.WriteString("\n")

	if diffHunk != "" {
		b.WriteString("Commented section:\n```diff\n")
		b.WriteString(diffHunk)
		b.WriteString("\n```\n\n")
	}

	if len(threadHistory) > 0 {
		b.WriteString("Previous conversation:\n")
		for _, msg := range threadHistory {
			b.WriteString("- " + msg + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("Developer's comment: " + comment + "\n")
	return b.String()
}
