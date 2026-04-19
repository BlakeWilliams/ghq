package comments

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ContentBlock is a segment of a comment body. A comment is a sequence of
// content blocks — typically text interspersed with tool-call groups.
type ContentBlock interface {
	blockType() string
}

// TextBlock holds a markdown text segment.
type TextBlock struct {
	Text string `json:"text"`
}

func (TextBlock) blockType() string { return "text" }

// ToolCall records a single tool invocation.
type ToolCall struct {
	Name      string `json:"name"`
	CallID    string `json:"call_id,omitempty"` // unique ID for correlating start/complete
	Status    string `json:"status"`            // "running", "done", "failed"
	Arguments string `json:"arguments"`         // truncated display string (may be empty)
}

// ToolGroupBlock holds a group of consecutive tool calls.
type ToolGroupBlock struct {
	Tools []ToolCall `json:"tools"`
	Label string     `json:"label,omitempty"` // optional title (e.g. from report_intent)
}

func (ToolGroupBlock) blockType() string { return "tool_group" }

// ToolGroupStatus returns the aggregate status of a tool group:
//   - "running" if any tool is still running
//   - "failed"  if any tool failed (and none are still running)
//   - "done"    if all tools completed successfully
func (g ToolGroupBlock) ToolGroupStatus() string {
	hasRunning := false
	hasFailed := false
	for _, t := range g.Tools {
		switch t.Status {
		case "running":
			hasRunning = true
		case "failed":
			hasFailed = true
		}
	}
	if hasRunning {
		return "running"
	}
	if hasFailed {
		return "failed"
	}
	return "done"
}

// NormalizedBlocks returns the given blocks directly if non-empty. Otherwise
// it synthesizes a single TextBlock from body for backward compatibility.
func NormalizedBlocks(blocks []ContentBlock, body string) []ContentBlock {
	if len(blocks) > 0 {
		return blocks
	}
	if body != "" {
		return []ContentBlock{TextBlock{Text: body}}
	}
	return nil
}

// BodyFromBlocks derives a plain-text body from a slice of content blocks
// by concatenating all TextBlock values. Used when persisting so that older
// code that only reads Body can still display something useful.
func BodyFromBlocks(blocks []ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if tb, ok := b.(TextBlock); ok {
			parts = append(parts, tb.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// --- JSON serialization for ContentBlock slices ---

type blockEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// MarshalBlocksJSON serializes a slice of ContentBlock to JSON.
func MarshalBlocksJSON(blocks []ContentBlock) ([]byte, error) {
	envs := make([]blockEnvelope, len(blocks))
	for i, b := range blocks {
		data, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("marshal block %d: %w", i, err)
		}
		envs[i] = blockEnvelope{Type: b.blockType(), Data: data}
	}
	return json.Marshal(envs)
}

// UnmarshalBlocksJSON deserializes a JSON array into a slice of ContentBlock.
func UnmarshalBlocksJSON(data []byte) ([]ContentBlock, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var envs []blockEnvelope
	if err := json.Unmarshal(data, &envs); err != nil {
		return nil, err
	}
	blocks := make([]ContentBlock, len(envs))
	for i, env := range envs {
		switch env.Type {
		case "text":
			var tb TextBlock
			if err := json.Unmarshal(env.Data, &tb); err != nil {
				return nil, fmt.Errorf("unmarshal text block %d: %w", i, err)
			}
			blocks[i] = tb
		case "tool_group":
			var tg ToolGroupBlock
			if err := json.Unmarshal(env.Data, &tg); err != nil {
				return nil, fmt.Errorf("unmarshal tool_group block %d: %w", i, err)
			}
			blocks[i] = tg
		default:
			return nil, fmt.Errorf("unknown block type %q at index %d", env.Type, i)
		}
	}
	return blocks, nil
}
