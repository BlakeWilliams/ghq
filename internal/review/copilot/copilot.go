package copilot

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	sdk "github.com/github/copilot-sdk/go"
)

// ReplyMsg carries a streaming or complete Copilot reply.
type ReplyMsg struct {
	CommentID string // the local comment ID this is replying to
	Content   string // delta content (streaming) or full content (done)
	Done      bool
}

// ToolMsg signals a tool execution event.
type ToolMsg struct {
	CommentID string
	Name      string
	Done      bool
}

// ErrorMsg carries a Copilot error.
type ErrorMsg struct {
	CommentID string
	Err       error
}

// Client wraps the Copilot SDK for code review conversations.
type Client struct {
	sdk              *sdk.Client
	session          *sdk.Session
	mu               sync.Mutex
	ctx              context.Context
	cancel           context.CancelFunc
	ready            chan struct{}
	startErr         error
	events           chan tea.Msg
	activeCommentID  string // current comment being replied to
	listenerAttached bool
	lastSendAt       time.Time
	log              *log.Logger
}

// New creates and starts a Copilot client.
func New(repoRoot string) (*Client, error) {
	ctx, cancel := context.WithCancel(context.Background())

	logFile, _ := os.OpenFile("/tmp/ghq-copilot.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	logger := log.New(logFile, "copilot: ", log.LstdFlags)

	sdkClient := sdk.NewClient(&sdk.ClientOptions{
		LogLevel: "debug",
		Cwd:      repoRoot,
	})

	c := &Client{
		sdk:    sdkClient,
		ctx:    ctx,
		cancel: cancel,
		ready:  make(chan struct{}),
		events: make(chan tea.Msg, 100),
		log:    logger,
	}

	go func() {
		logger.Println("starting copilot SDK...")
		if err := sdkClient.Start(ctx); err != nil {
			logger.Printf("copilot Start failed: %v", err)
			c.startErr = err
		} else {
			logger.Println("copilot SDK started successfully")
		}
		close(c.ready)
	}()

	return c, nil
}

// ListenCmd returns a tea.Cmd that blocks until a Copilot event is available.
func (c *Client) ListenCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-c.events:
			return msg
		case <-c.ctx.Done():
			return nil
		}
	}
}

// SendComment sends a comment with diff context to Copilot and streams back replies.
func (c *Client) SendComment(commentID, body, filePath, fileContent, fullDiff, diffHunk string, threadHistory []string) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-c.ready:
		case <-time.After(30 * time.Second):
			c.log.Println("timeout waiting for copilot SDK to be ready")
			return ErrorMsg{CommentID: commentID, Err: fmt.Errorf("copilot SDK startup timeout")}
		}

		if c.startErr != nil {
			return ErrorMsg{CommentID: commentID, Err: fmt.Errorf("copilot not available: %w", c.startErr)}
		}

		c.mu.Lock()
		c.activeCommentID = commentID

		// If session seems stuck (last send was > 2 min ago with no idle), reset it.
		if c.session != nil && !c.lastSendAt.IsZero() && time.Since(c.lastSendAt) > 2*time.Minute {
			c.log.Println("session appears stuck, resetting...")
			c.session.Disconnect()
			c.session = nil
			c.listenerAttached = false
		}

		if c.session == nil {
			c.log.Println("creating copilot session...")
			session, err := c.sdk.CreateSession(c.ctx, &sdk.SessionConfig{
				Model:               "claude-opus-4.6",
				Streaming:           true,
				OnPermissionRequest: sdk.PermissionHandler.ApproveAll,
			})
			if err != nil {
				c.mu.Unlock()
				c.log.Printf("CreateSession failed: %v", err)
				return ErrorMsg{CommentID: commentID, Err: fmt.Errorf("create session: %w", err)}
			}
			c.log.Printf("session created: %s", session.SessionID)
			c.session = session
		}

		if !c.listenerAttached {
			c.setupListener()
			c.listenerAttached = true
			c.log.Println("listener attached")
		}
		c.mu.Unlock()

		prompt := buildReviewPrompt(body, filePath, fileContent, fullDiff, diffHunk, threadHistory)
		c.log.Printf("sending prompt (%d chars) for comment %s", len(prompt), commentID)

		c.mu.Lock()
		c.lastSendAt = time.Now()
		c.mu.Unlock()

		_, err := c.session.Send(c.ctx, sdk.MessageOptions{
			Prompt: prompt,
		})
		if err != nil {
			c.log.Printf("session.Send failed: %v", err)
			return ErrorMsg{CommentID: commentID, Err: fmt.Errorf("send: %w", err)}
		}

		c.log.Println("session.Send returned successfully")

		// Start a response timeout — if no events within 60s, error out.
		go func() {
			time.Sleep(60 * time.Second)
			c.mu.Lock()
			// Only fire timeout if this send is still active.
			if c.activeCommentID == commentID && !c.lastSendAt.IsZero() {
				c.log.Printf("response timeout for %s", commentID)
				c.lastSendAt = time.Time{}
				c.mu.Unlock()
				c.events <- ErrorMsg{CommentID: commentID, Err: fmt.Errorf("copilot response timeout (60s)")}
			} else {
				c.mu.Unlock()
			}
		}()

		return nil
	}
}

func (c *Client) setupListener() {
	c.session.On(func(event sdk.SessionEvent) {
		c.mu.Lock()
		commentID := c.activeCommentID
		c.mu.Unlock()

		switch event.Type {
		case sdk.SessionEventTypeAssistantMessageDelta:
			if event.Data.DeltaContent != nil && *event.Data.DeltaContent != "" {
				c.log.Printf("delta: %d chars for %s", len(*event.Data.DeltaContent), commentID)
				c.events <- ReplyMsg{
					CommentID: commentID,
					Content:   *event.Data.DeltaContent,
				}
			}
		case sdk.SessionEventTypeAssistantMessage:
			c.log.Printf("assistant message complete for %s", commentID)
		case sdk.SessionEventTypeToolExecutionStart:
			name := ""
			if event.Data.ToolName != nil {
				name = *event.Data.ToolName
			}
			c.log.Printf("tool start: %s", name)
			c.events <- ToolMsg{CommentID: commentID, Name: name, Done: false}
		case sdk.SessionEventTypeToolExecutionComplete:
			name := ""
			if event.Data.ToolName != nil {
				name = *event.Data.ToolName
			}
			c.log.Printf("tool complete: %s", name)
			c.events <- ToolMsg{CommentID: commentID, Name: name, Done: true}
		case sdk.SessionEventTypeSessionIdle:
			c.log.Printf("session idle for %s", commentID)
			c.mu.Lock()
			c.lastSendAt = time.Time{}
			c.mu.Unlock()
			c.events <- ReplyMsg{
				CommentID: commentID,
				Done:      true,
			}
		case sdk.SessionEventTypeSessionError:
			msg := "copilot error"
			if event.Data.Message != nil {
				msg = *event.Data.Message
			}
			c.log.Printf("session error: %s", msg)
			c.events <- ErrorMsg{
				CommentID: commentID,
				Err:       fmt.Errorf("%s", msg),
			}
		default:
			c.log.Printf("unhandled event type: %v", event.Type)
		}
	})
}

func buildReviewPrompt(comment, filePath, fileContent, fullDiff, diffHunk string, threadHistory []string) string {
	var b strings.Builder
	b.WriteString("You are reviewing local code changes. Respond concisely to the developer's comment about this diff.\n\n")
	b.WriteString("File: " + filePath + "\n\n")

	if fileContent != "" {
		b.WriteString("Full file contents:\n```\n")
		b.WriteString(fileContent)
		b.WriteString("\n```\n\n")
	}

	if fullDiff != "" {
		b.WriteString("Full diff for this file:\n```diff\n")
		b.WriteString(fullDiff)
		b.WriteString("\n```\n\n")
	}

	if diffHunk != "" {
		b.WriteString("Commented section (the comment is on this specific area):\n```diff\n")
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

// Stop shuts down the client and releases resources.
func (c *Client) Stop() {
	c.mu.Lock()
	if c.session != nil {
		c.session.Disconnect()
		c.session = nil
	}
	c.mu.Unlock()
	if c.sdk != nil {
		c.sdk.Stop()
	}
	if c.cancel != nil {
		c.cancel()
	}
}
