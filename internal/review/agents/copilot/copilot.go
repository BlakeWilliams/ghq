package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	sdk "github.com/github/copilot-sdk/go"

	"github.com/blakewilliams/gg/internal/review/agents"
)

// Client wraps the Copilot SDK for code review conversations.
// It implements agents.Agent.
type Client struct {
	sdk      *sdk.Client
	ctx      context.Context
	cancel   context.CancelFunc
	ready    chan struct{}
	startErr error
	events   chan agents.AgentEvent
	log      *log.Logger
	logFile  *os.File
	repoRoot string
}

// New creates a Copilot client without starting the SDK.
// Call Start to initialize the SDK connection. The caller owns the decision
// of whether to start synchronously or in a goroutine.
func New(repoRoot string) (*Client, error) {
	ctx, cancel := context.WithCancel(context.Background())

	logFile, _ := os.OpenFile("/tmp/ghq-copilot.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	logger := log.New(logFile, "copilot: ", log.LstdFlags)

	sdkClient := sdk.NewClient(&sdk.ClientOptions{
		LogLevel: "debug",
		Cwd:      repoRoot,
	})

	c := &Client{
		sdk:      sdkClient,
		ctx:      ctx,
		cancel:   cancel,
		ready:    make(chan struct{}),
		events:   make(chan agents.AgentEvent, 100),
		log:      logger,
		logFile:  logFile,
		repoRoot: repoRoot,
	}

	return c, nil
}

// Start initializes the SDK connection. It blocks until the SDK is ready or
// fails. Send waits on the result, so callers that want a non-blocking
// constructor can simply: go c.Start()
func (c *Client) Start() {
	c.log.Println("starting copilot SDK...")
	if err := c.sdk.Start(c.ctx); err != nil {
		c.log.Printf("copilot Start failed: %v", err)
		c.startErr = err
	} else {
		c.log.Println("copilot SDK started successfully")
	}
	close(c.ready)
}

// Drain returns all buffered events without blocking.
// Returns nil when no events are pending.
func (c *Client) Drain() []agents.AgentEvent {
	var events []agents.AgentEvent
	for {
		select {
		case ev := <-c.events:
			events = append(events, ev)
		default:
			return events
		}
	}
}

// Send sends a prompt to Copilot and streams back replies.
// Each comment gets its own session so multiple can run in parallel.
// It blocks until the prompt is dispatched; streaming results arrive via Drain.
func (c *Client) Send(commentID, prompt string) error {
	select {
	case <-c.ready:
	case <-time.After(30 * time.Second):
		c.log.Println("timeout waiting for copilot SDK to be ready")
		return fmt.Errorf("copilot SDK startup timeout")
	}

	if c.startErr != nil {
		return fmt.Errorf("copilot not available: %w", c.startErr)
	}

	// Create a dedicated session for this comment.
	c.log.Printf("creating session for comment %s", commentID)
	session, err := c.sdk.CreateSession(c.ctx, &sdk.SessionConfig{
		Model:               "claude-opus-4.6",
		Streaming:           true,
		OnPermissionRequest: sdk.PermissionHandler.ApproveAll,
	})
	if err != nil {
		c.log.Printf("CreateSession failed for %s: %v", commentID, err)
		return fmt.Errorf("create session: %w", err)
	}
	c.log.Printf("session created for %s: %s", commentID, session.SessionID)

	// Channel to cancel the timeout when session completes.
	done := make(chan struct{})
	// Activity channel — any event resets the timeout.
	activity := make(chan struct{}, 1)

	// Attach a listener that tags all events with this commentID.
	session.On(func(event sdk.SessionEvent) {
		c.log.Printf("event for %s: type=%s", commentID, event.Type)

		// Signal activity to reset the timeout.
		select {
		case activity <- struct{}{}:
		default:
		}

		switch event.Type {
		case sdk.SessionEventTypeAssistantMessageDelta:
			if event.Data.DeltaContent != nil && *event.Data.DeltaContent != "" {
				c.events <- agents.AgentEvent{
					CommentID: commentID,
					Kind:      agents.EventDelta,
					Payload:   agents.DeltaPayload{Delta: *event.Data.DeltaContent},
				}
			}
		case sdk.SessionEventTypeToolExecutionStart:
			name := ""
			if event.Data.ToolName != nil {
				name = *event.Data.ToolName
			}
			callID := ""
			if event.Data.ToolCallID != nil {
				callID = *event.Data.ToolCallID
			}
			var args string
			if name == "report_intent" {
				args = extractIntentArg(event.Data.Arguments)
			} else {
				args = formatToolArgs(event.Data.Arguments, c.repoRoot)
			}
			c.events <- agents.AgentEvent{CommentID: commentID, Kind: agents.EventToolStart, Payload: agents.ToolPayload{Name: name, CallID: callID, Arguments: args}}
		case sdk.SessionEventTypeToolExecutionComplete:
			name := ""
			if event.Data.ToolName != nil {
				name = *event.Data.ToolName
			}
			callID := ""
			if event.Data.ToolCallID != nil {
				callID = *event.Data.ToolCallID
			}
			c.events <- agents.AgentEvent{CommentID: commentID, Kind: agents.EventToolComplete, Payload: agents.ToolPayload{Name: name, CallID: callID}}
		case sdk.SessionEventTypeSessionIdle:
			c.log.Printf("session idle for %s", commentID)
			close(done) // Cancel timeout
			c.events <- agents.AgentEvent{CommentID: commentID, Kind: agents.EventDone}
			session.Disconnect()
		case sdk.SessionEventTypeSessionError:
			msg := "copilot error"
			if event.Data.Message != nil {
				msg = *event.Data.Message
			}
			c.log.Printf("session error for %s: %s", commentID, msg)
			close(done) // Cancel timeout
			c.events <- agents.AgentEvent{CommentID: commentID, Kind: agents.EventError, Payload: agents.ErrorPayload{Err: fmt.Errorf("%s", msg)}}
			session.Disconnect()
		default:
			c.log.Printf("unhandled session event type %q for %s", event.Type, commentID)
		}
	})

	c.log.Printf("sending prompt (%d chars) for comment %s", len(prompt), commentID)

	msgID, err := session.Send(c.ctx, sdk.MessageOptions{
		Prompt: prompt,
	})
	if err != nil {
		c.log.Printf("session.Send failed for %s: %v", commentID, err)
		close(done) // Cancel timeout
		session.Disconnect()
		return fmt.Errorf("send: %w", err)
	}

	c.log.Printf("session.Send returned for %s: messageID=%s", commentID, msgID)

	// Response timeout — resets on any activity from the session.
	go func() {
		timer := time.NewTimer(60 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-done:
				return
			case <-activity:
				timer.Reset(60 * time.Second)
			case <-timer.C:
				c.log.Printf("timeout waiting for copilot response for %s", commentID)
				c.events <- agents.AgentEvent{CommentID: commentID, Kind: agents.EventError, Payload: agents.ErrorPayload{Err: fmt.Errorf("copilot response timeout (60s)")}}
				session.Disconnect()
				return
			}
		}
	}()

	return nil
}

// Stop shuts down the client and releases resources.
func (c *Client) Stop() {
	if c.sdk != nil {
		c.sdk.Stop()
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.logFile != nil {
		c.logFile.Close()
	}
}

// formatToolArgs extracts a compact display string from SDK tool arguments.
// Picks the most meaningful value per tool rather than showing key=value pairs.
// Paths are made relative to repoRoot when possible.
func formatToolArgs(args interface{}, repoRoot string) string {
	if args == nil {
		return ""
	}

	m, ok := args.(map[string]interface{})
	if !ok {
		b, err := json.Marshal(args)
		if err != nil {
			return ""
		}
		return string(b)
	}

	// For tools with a description field, prefer that.
	if desc, ok := m["description"]; ok {
		s := fmt.Sprintf("%v", desc)
		if len(s) > 80 {
			s = s[:77] + "..."
		}
		return s
	}

	// Pick the primary value based on common tool argument names.
	// Priority order: path > pattern > query > url > skill > command > first value.
	for _, key := range []string{"path", "pattern", "query", "url", "skill", "command"} {
		if v, ok := m[key]; ok {
			s := fmt.Sprintf("%v", v)
			s = relativizePath(s, repoRoot)
			if len(s) > 80 {
				s = s[:77] + "..."
			}
			return s
		}
	}

	// Fallback: first value found.
	for _, v := range m {
		s := fmt.Sprintf("%v", v)
		s = relativizePath(s, repoRoot)
		if len(s) > 80 {
			s = s[:77] + "..."
		}
		return s
	}

	return ""
}

// relativizePath strips repoRoot prefix to produce a relative path.
func relativizePath(s, repoRoot string) string {
	if repoRoot == "" {
		return s
	}
	prefix := repoRoot + "/"
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return s
}

// extractIntentArg pulls the "intent" string from report_intent's arguments.
func extractIntentArg(args interface{}) string {
	if args == nil {
		return ""
	}
	m, ok := args.(map[string]interface{})
	if !ok {
		return ""
	}
	if v, ok := m["intent"]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}
