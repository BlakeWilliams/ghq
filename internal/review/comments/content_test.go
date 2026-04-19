package comments

import (
	"testing"
)

func TestContentBlockJSON_RoundTrip(t *testing.T) {
	blocks := []ContentBlock{
		TextBlock{Text: "Here is the fix:"},
		ToolGroupBlock{Tools: []ToolCall{
			{Name: "read_file", Status: "done"},
			{Name: "search_code", Status: "running"},
		}},
		TextBlock{Text: "Applied the change."},
	}

	data, err := MarshalBlocksJSON(blocks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := UnmarshalBlocksJSON(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(got))
	}

	tb, ok := got[0].(TextBlock)
	if !ok {
		t.Fatalf("block 0: expected TextBlock, got %T", got[0])
	}
	if tb.Text != "Here is the fix:" {
		t.Errorf("block 0 text = %q, want %q", tb.Text, "Here is the fix:")
	}

	tg, ok := got[1].(ToolGroupBlock)
	if !ok {
		t.Fatalf("block 1: expected ToolGroupBlock, got %T", got[1])
	}
	if len(tg.Tools) != 2 {
		t.Fatalf("block 1: expected 2 tools, got %d", len(tg.Tools))
	}
	if tg.Tools[0].Name != "read_file" || tg.Tools[0].Status != "done" {
		t.Errorf("tool 0: %+v", tg.Tools[0])
	}
	if tg.Tools[1].Name != "search_code" || tg.Tools[1].Status != "running" {
		t.Errorf("tool 1: %+v", tg.Tools[1])
	}

	tb2, ok := got[2].(TextBlock)
	if !ok {
		t.Fatalf("block 2: expected TextBlock, got %T", got[2])
	}
	if tb2.Text != "Applied the change." {
		t.Errorf("block 2 text = %q", tb2.Text)
	}
}

func TestContentBlockJSON_NilAndEmpty(t *testing.T) {
	got, err := UnmarshalBlocksJSON(nil)
	if err != nil {
		t.Fatalf("nil: %v", err)
	}
	if got != nil {
		t.Errorf("nil input should return nil, got %v", got)
	}

	got, err = UnmarshalBlocksJSON([]byte("null"))
	if err != nil {
		t.Fatalf("null: %v", err)
	}
	if got != nil {
		t.Errorf("null input should return nil, got %v", got)
	}
}

func TestToolGroupStatus(t *testing.T) {
	tests := []struct {
		name   string
		tools  []ToolCall
		expect string
	}{
		{"all done", []ToolCall{{Status: "done"}, {Status: "done"}}, "done"},
		{"one running", []ToolCall{{Status: "done"}, {Status: "running"}}, "running"},
		{"one failed", []ToolCall{{Status: "done"}, {Status: "failed"}}, "failed"},
		{"running beats failed", []ToolCall{{Status: "failed"}, {Status: "running"}}, "running"},
		{"empty", nil, "done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := ToolGroupBlock{Tools: tt.tools}
			if got := g.ToolGroupStatus(); got != tt.expect {
				t.Errorf("ToolGroupStatus() = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestNormalizedBlocks(t *testing.T) {
	// With blocks: returns blocks directly.
	blocks := []ContentBlock{TextBlock{Text: "new text"}}
	got := NormalizedBlocks(blocks, "old body")
	if len(got) != 1 {
		t.Fatalf("expected 1 block, got %d", len(got))
	}
	if got[0].(TextBlock).Text != "new text" {
		t.Errorf("wrong text: %q", got[0].(TextBlock).Text)
	}

	// Without blocks: falls back to body.
	got2 := NormalizedBlocks(nil, "fallback")
	if len(got2) != 1 {
		t.Fatalf("expected 1 block, got %d", len(got2))
	}
	if got2[0].(TextBlock).Text != "fallback" {
		t.Errorf("wrong text: %q", got2[0].(TextBlock).Text)
	}

	// Empty: returns nil.
	if got3 := NormalizedBlocks(nil, ""); got3 != nil {
		t.Errorf("expected nil, got %v", got3)
	}
}

func TestBodyFromBlocks(t *testing.T) {
	blocks := []ContentBlock{
		TextBlock{Text: "First part"},
		ToolGroupBlock{Tools: []ToolCall{{Name: "read_file", Status: "done"}}},
		TextBlock{Text: "Second part"},
	}
	got := BodyFromBlocks(blocks)
	if got != "First part\nSecond part" {
		t.Errorf("BodyFromBlocks = %q", got)
	}
}
