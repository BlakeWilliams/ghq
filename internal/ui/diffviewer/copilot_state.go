package diffviewer

import (
	"strings"
	"time"

	"github.com/blakewilliams/gg/internal/review/agents"
	"github.com/blakewilliams/gg/internal/review/comments"
	"github.com/blakewilliams/gg/internal/ui/components"
)

// CopilotState manages all copilot reply state for a DiffViewer.
// It accumulates streaming agent events and provides render-ready output.
type CopilotState struct {
	ReplyBuf map[string][]comments.ContentBlock // commentID -> accumulated blocks
	Pending  map[string]CopilotPendingInfo     // commentID -> pending info
	Dots     int                               // shared animation frame (0-3)
	Intent   map[string]string                 // commentID -> latest report_intent text
}

// NewCopilotState creates a CopilotState with initialized maps.
func NewCopilotState() CopilotState {
	return CopilotState{
		ReplyBuf: make(map[string][]comments.ContentBlock),
		Pending:  make(map[string]CopilotPendingInfo),
	}
}

// SetPending marks a comment as awaiting a Copilot reply.
func (cs *CopilotState) SetPending(commentID, path string, line int, side string) {
	if cs.Pending == nil {
		cs.Pending = make(map[string]CopilotPendingInfo)
	}
	cs.Pending[commentID] = CopilotPendingInfo{Path: path, Line: line, Side: side}
}

// ClearPending removes a single pending Copilot session.
func (cs *CopilotState) ClearPending(commentID string) {
	delete(cs.Pending, commentID)
}

// CancelPendingAt cancels any pending Copilot reply at the given file/side/line.
// Returns the comment ID that was cancelled, or "" if none found.
func (cs *CopilotState) CancelPendingAt(path, side string, line int) string {
	for id, info := range cs.Pending {
		if info.Path == path && info.Side == side && info.Line == line {
			delete(cs.Pending, id)
			delete(cs.ReplyBuf, id)
			delete(cs.Intent, id)
			return id
		}
	}
	return ""
}

// HasPending returns true if there are any pending Copilot sessions.
func (cs CopilotState) HasPending() bool {
	return len(cs.Pending) > 0
}

// IsPending returns true if the given comment is pending.
func (cs CopilotState) IsPending(commentID string) bool {
	_, ok := cs.Pending[commentID]
	return ok
}

// IsPendingAt returns true if there's a pending Copilot reply at the given file/side/line.
func (cs CopilotState) IsPendingAt(path, side string, line int) bool {
	for _, info := range cs.Pending {
		if info.Path == path && info.Side == side && info.Line == line {
			return true
		}
	}
	return false
}

// PendingPath returns the path of a pending Copilot session, or "".
func (cs CopilotState) PendingPath(commentID string) string {
	if info, ok := cs.Pending[commentID]; ok {
		return info.Path
	}
	return ""
}

// AdvanceDots increments the animation frame counter (0-3).
func (cs *CopilotState) AdvanceDots() {
	cs.Dots = (cs.Dots + 1) % 4
}

// PendingRenderComments returns pending Copilot reply comments for a file
// as RenderComments keyed by thread position, preserving accumulated content blocks.
func (cs CopilotState) PendingRenderComments(filename string) map[components.CommentKey][]components.RenderComment {
	result := make(map[components.CommentKey][]components.RenderComment)
	dots := strings.Repeat(".", cs.Dots+1)
	for commentID, info := range cs.Pending {
		if info.Path != filename {
			continue
		}
		blocks := cs.ReplyBuf[commentID]

		var renderBlocks []comments.ContentBlock

		placeholder := "Thinking"
		if intent, ok := cs.Intent[commentID]; ok && intent != "" {
			placeholder = intent
		}

		if len(blocks) > 0 {
			renderBlocks = make([]comments.ContentBlock, len(blocks))
			copy(renderBlocks, blocks)
			if n := len(renderBlocks); n > 0 {
				if tb, ok := renderBlocks[n-1].(comments.TextBlock); ok {
					renderBlocks[n-1] = comments.TextBlock{Text: tb.Text + dots}
				} else {
					renderBlocks = append(renderBlocks, comments.TextBlock{Text: placeholder + dots})
				}
			}
		} else {
			renderBlocks = []comments.ContentBlock{comments.TextBlock{Text: placeholder + dots}}
		}

		key := components.CommentKey{Side: info.Side, Line: info.Line}
		result[key] = append(result[key], components.RenderComment{
			Author:    "copilot",
			CreatedAt: time.Now(),
			Blocks:    renderBlocks,
		})
	}
	return result
}

// CompletedReply holds the data for a copilot reply that finished successfully.
// The caller is responsible for persisting this to its comment store.
type CompletedReply struct {
	CommentID string
	Body      string
	Blocks    []comments.ContentBlock
}

// HandleEventResult is returned by HandleEvent to tell the caller what happened.
type HandleEventResult struct {
	// FilePath is the file that needs re-rendering, or "" if none.
	FilePath string
	// Reply is non-nil if the agent completed and produced a reply to persist.
	Reply *CompletedReply
}

// HandleEvent processes a single agent event and mutates the copilot state.
// Returns a result indicating what the caller should do (re-render, persist reply, etc.).
func (cs *CopilotState) HandleEvent(ev agents.AgentEvent) HandleEventResult {
	switch ev.Kind {
	case agents.EventDelta:
		p := ev.Payload.(agents.DeltaPayload)
		blocks := cs.ReplyBuf[ev.CommentID]
		if n := len(blocks); n > 0 {
			if tb, ok := blocks[n-1].(comments.TextBlock); ok {
				blocks[n-1] = comments.TextBlock{Text: tb.Text + p.Delta}
			} else {
				blocks = append(blocks, comments.TextBlock{Text: p.Delta})
			}
		} else {
			blocks = append(blocks, comments.TextBlock{Text: p.Delta})
		}
		cs.ReplyBuf[ev.CommentID] = blocks
		return HandleEventResult{}

	case agents.EventDone:
		blocks := cs.ReplyBuf[ev.CommentID]
		delete(cs.ReplyBuf, ev.CommentID)
		pendingPath := cs.PendingPath(ev.CommentID)
		cs.ClearPending(ev.CommentID)
		body := comments.BodyFromBlocks(blocks)
		if body != "" || len(blocks) > 0 {
			return HandleEventResult{
				FilePath: pendingPath,
				Reply: &CompletedReply{
					CommentID: ev.CommentID,
					Body:      strings.TrimSpace(body),
					Blocks:    blocks,
				},
			}
		}
		return HandleEventResult{FilePath: pendingPath}

	case agents.EventError:
		pendingPath := cs.PendingPath(ev.CommentID)
		cs.ClearPending(ev.CommentID)
		return HandleEventResult{FilePath: pendingPath}

	case agents.EventToolStart:
		p := ev.Payload.(agents.ToolPayload)
		blocks := cs.ReplyBuf[ev.CommentID]

		if p.Name == "report_intent" {
			if cs.Intent == nil {
				cs.Intent = make(map[string]string)
			}
			if p.Arguments != "" {
				cs.Intent[ev.CommentID] = p.Arguments
			}
			if n := len(blocks); n > 0 {
				if tg, ok := blocks[n-1].(comments.ToolGroupBlock); ok && p.Arguments != "" {
					tg.Label = p.Arguments
					blocks[n-1] = tg
					cs.ReplyBuf[ev.CommentID] = blocks
				}
			}
			return HandleEventResult{}
		}

		tc := comments.ToolCall{Name: p.Name, CallID: p.CallID, Status: "running", Arguments: p.Arguments}
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
		cs.ReplyBuf[ev.CommentID] = blocks
		return HandleEventResult{}

	case agents.EventToolComplete:
		p := ev.Payload.(agents.ToolPayload)
		if p.Name == "report_intent" {
			return HandleEventResult{}
		}
		blocks := cs.ReplyBuf[ev.CommentID]
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
						cs.ReplyBuf[ev.CommentID] = blocks
						return HandleEventResult{}
					}
				}
			}
		}
		return HandleEventResult{}
	}
	return HandleEventResult{}
}
