package tokenest

import (
	"strings"
	"testing"

	"github.com/HBulgat/mini-agent/internal/llm"
)

// TestCharRatio_PureASCII checks the en-weighted path. A 100-char ASCII
// string at en=0.25 should round to 25 tokens.
func TestCharRatio_PureASCII(t *testing.T) {
	c := NewCharRatio(0.6, 0.25)
	got := c.EstimateText(strings.Repeat("a", 100))
	if got < 23 || got > 27 {
		t.Errorf("100 ASCII chars: got %d, want ~25", got)
	}
}

// TestCharRatio_PureCJK checks the zh-weighted path. 100 Han chars at
// zh=0.6 should round to ~60.
func TestCharRatio_PureCJK(t *testing.T) {
	c := NewCharRatio(0.6, 0.25)
	got := c.EstimateText(strings.Repeat("中", 100))
	if got < 58 || got > 62 {
		t.Errorf("100 Han chars: got %d, want ~60", got)
	}
}

// TestCharRatio_Mixed verifies the per-rune classification.
func TestCharRatio_Mixed(t *testing.T) {
	c := NewCharRatio(1, 1) // every char counts as 1 token
	got := c.EstimateText("hi 你好")
	// "hi 你好" = h, i, space (3 ascii) + 你, 好 (2 cjk) = 5
	if got != 5 {
		t.Errorf("mixed string: got %d, want 5", got)
	}
}

func TestCharRatio_EmptyAndShort(t *testing.T) {
	c := NewCharRatio(0.6, 0.25)
	if got := c.EstimateText(""); got != 0 {
		t.Errorf("empty: got %d, want 0", got)
	}
	// Single ASCII char: 1 * 0.25 = 0.25, should round up to 1 (cheap
	// non-zero floor for non-empty input).
	if got := c.EstimateText("a"); got != 1 {
		t.Errorf("single ascii: got %d, want 1", got)
	}
}

// TestCharRatio_EstimateMessages exercises the per-message overhead +
// the multi-block branches (text / thinking / tool_use / tool_result).
func TestCharRatio_EstimateMessages(t *testing.T) {
	c := NewCharRatio(0.6, 0.25)
	msgs := []*llm.Message{
		{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{
				{Type: llm.BlockText, Text: strings.Repeat("a", 40)}, // ~10 tokens
			},
		},
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				{Type: llm.BlockThinking, Thinking: strings.Repeat("a", 40)}, // ~10
				{Type: llm.BlockToolUse, ToolName: "bash", ToolInput: map[string]any{"cmd": strings.Repeat("a", 40)}},
			},
		},
	}
	got := c.EstimateMessages(msgs)
	// Per-msg overhead: 4 * 2 = 8.
	// Body: 10 + 10 + (4 for "bash") + 10 (cmd value) = ~34.
	// Total ~42, allow ±20% slack.
	if got < 32 || got > 60 {
		t.Errorf("estimate = %d, want ~42 (±)", got)
	}
}

// TestCharRatio_NilMessages: a nil pointer in the slice must not panic.
func TestCharRatio_NilMessages(t *testing.T) {
	c := NewCharRatio(0.6, 0.25)
	got := c.EstimateMessages([]*llm.Message{nil, nil})
	if got != 0 {
		t.Errorf("nil msgs estimate = %d, want 0", got)
	}
}

func TestCharRatio_RedactedThinking(t *testing.T) {
	c := NewCharRatio(0.6, 0.25)
	msgs := []*llm.Message{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				{Type: llm.BlockRedactedThinking},
			},
		},
	}
	got := c.EstimateMessages(msgs)
	// 4 (overhead) + 64 (redacted fixed cost) = 68
	if got != 68 {
		t.Errorf("redacted thinking estimate = %d, want 68", got)
	}
}
