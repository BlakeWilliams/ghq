package comments

import (
	"encoding/json"
	"testing"
)

func TestLocalComment_JSONRoundTrip_WithBlocks(t *testing.T) {
	c := LocalComment{
		ID:     "test-1",
		Body:   "will be overwritten",
		Author: "copilot",
		Blocks: []ContentBlock{
			TextBlock{Text: "Here's the fix"},
			ToolGroupBlock{Tools: []ToolCall{{Name: "read_file", Status: "done"}}},
		},
	}
	c.prepareForSave()

	if c.Body != "Here's the fix" {
		t.Errorf("Body after prepare = %q", c.Body)
	}

	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var loaded LocalComment
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	loaded.hydrateBlocks()

	if len(loaded.Blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(loaded.Blocks))
	}
	if _, ok := loaded.Blocks[0].(TextBlock); !ok {
		t.Errorf("block 0: expected TextBlock, got %T", loaded.Blocks[0])
	}
	if _, ok := loaded.Blocks[1].(ToolGroupBlock); !ok {
		t.Errorf("block 1: expected ToolGroupBlock, got %T", loaded.Blocks[1])
	}
}

func TestLocalComment_JSONRoundTrip_NoBlocks(t *testing.T) {
	// Old-format comment: only Body, no blocks.
	c := LocalComment{ID: "old-1", Body: "just text", Author: "you"}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var loaded LocalComment
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	loaded.hydrateBlocks()

	// Blocks should be nil (old format), NormalizedBlocks falls back.
	if loaded.Blocks != nil {
		t.Errorf("expected nil Blocks for old format, got %v", loaded.Blocks)
	}
	norm := NormalizedBlocks(loaded.Blocks, loaded.Body)
	if len(norm) != 1 {
		t.Fatalf("expected 1 normalized block, got %d", len(norm))
	}
	if norm[0].(TextBlock).Text != "just text" {
		t.Errorf("normalized text = %q", norm[0].(TextBlock).Text)
	}
}
