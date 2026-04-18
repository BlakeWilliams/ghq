package copilot

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
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
	sdk      *sdk.Client
	ctx      context.Context
	cancel   context.CancelFunc
	ready    chan struct{}
	startErr error
	events   chan tea.Msg
	log      *log.Logger
	logFile  *os.File
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
		sdk:     sdkClient,
		ctx:     ctx,
		cancel:  cancel,
		ready:   make(chan struct{}),
		events:  make(chan tea.Msg, 100),
		log:     logger,
		logFile: logFile,
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
// Each comment gets its own session so multiple can run in parallel.
func (c *Client) SendComment(commentID, body, filePath, diffMode, diffHunk string, threadHistory []string) tea.Cmd {
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

		// Create a dedicated session for this comment.
		c.log.Printf("creating session for comment %s", commentID)
		session, err := c.sdk.CreateSession(c.ctx, &sdk.SessionConfig{
			Model:               "claude-opus-4.6",
			Streaming:           true,
			OnPermissionRequest: sdk.PermissionHandler.ApproveAll,
		})
		if err != nil {
			c.log.Printf("CreateSession failed for %s: %v", commentID, err)
			return ErrorMsg{CommentID: commentID, Err: fmt.Errorf("create session: %w", err)}
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
					c.events <- ReplyMsg{
						CommentID: commentID,
						Content:   *event.Data.DeltaContent,
					}
				}
			case sdk.SessionEventTypeToolExecutionStart:
				name := ""
				if event.Data.ToolName != nil {
					name = *event.Data.ToolName
				}
				c.events <- ToolMsg{CommentID: commentID, Name: name, Done: false}
			case sdk.SessionEventTypeToolExecutionComplete:
				name := ""
				if event.Data.ToolName != nil {
					name = *event.Data.ToolName
				}
				c.events <- ToolMsg{CommentID: commentID, Name: name, Done: true}
			case sdk.SessionEventTypeSessionIdle:
				c.log.Printf("session idle for %s", commentID)
				close(done) // Cancel timeout
				c.events <- ReplyMsg{CommentID: commentID, Done: true}
				session.Disconnect()
			case sdk.SessionEventTypeSessionError:
				msg := "copilot error"
				if event.Data.Message != nil {
					msg = *event.Data.Message
				}
				c.log.Printf("session error for %s: %s", commentID, msg)
				close(done) // Cancel timeout
				c.events <- ErrorMsg{CommentID: commentID, Err: fmt.Errorf("%s", msg)}
				session.Disconnect()
			}
		})

		prompt := buildReviewPrompt(body, filePath, diffMode, diffHunk, threadHistory)
		c.log.Printf("sending prompt (%d chars) for comment %s", len(prompt), commentID)

		msgID, err := session.Send(c.ctx, sdk.MessageOptions{
			Prompt: prompt,
		})
		if err != nil {
			c.log.Printf("session.Send failed for %s: %v", commentID, err)
			close(done) // Cancel timeout
			session.Disconnect()
			return ErrorMsg{CommentID: commentID, Err: fmt.Errorf("send: %w", err)}
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
					c.events <- ErrorMsg{CommentID: commentID, Err: fmt.Errorf("copilot response timeout (60s)")}
					session.Disconnect()
					return
				}
			}
		}()

		return nil
	}
}


func buildReviewPrompt(comment, filePath, diffMode, diffHunk string, threadHistory []string) string {
	var b strings.Builder
	b.WriteString("You are reviewing local code changes. Respond concisely to the developer's comment about this diff.\n\n")
	b.WriteString("File: " + filePath + "\n")

	// Mode-specific git instructions.
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
