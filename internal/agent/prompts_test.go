// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/HBulgat/mini-agent/internal/llm"
)

// TestBuildSystemPrompt_RendersEnv pins the rendered output: the
// template must substitute Cwd / OS / Time without leaving raw
// {{...}} markers behind.
func TestBuildSystemPrompt_RendersEnv(t *testing.T) {
	pb, err := NewPromptBuilder()
	if err != nil {
		t.Fatal(err)
	}
	t0, _ := time.Parse(time.RFC3339, "2026-05-27T09:42:00+08:00")
	got, err := pb.buildSystemAt("/tmp/proj", t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"You are mini-agent",
		"cwd: /tmp/proj",
		"time: 2026-05-27T09:42:00+08:00",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("system prompt missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "{{") {
		t.Errorf("template not fully rendered:\n%s", got)
	}
}

// TestBuildSkillList_EmptyReturnsEmpty: no skills → no system message.
// Caller checks for empty string and skips injection.
func TestBuildSkillList_EmptyReturnsEmpty(t *testing.T) {
	if got := BuildSkillList(nil); got != "" {
		t.Errorf("empty skills should return empty string; got:\n%s", got)
	}
	if got := BuildSkillList([]SkillSummary{}); got != "" {
		t.Errorf("empty slice should return empty string; got:\n%s", got)
	}
}

// TestBuildSkillList_NonEmpty checks the rendered list shape.
func TestBuildSkillList_NonEmpty(t *testing.T) {
	skills := []SkillSummary{
		{Name: "frontend", Description: "React + AntD project guidance"},
		{Name: "go-test", Description: "Go testing best practices"},
	}
	got := BuildSkillList(skills)
	for _, want := range []string{
		"## Available Skills",
		"`skill` tool",
		"**frontend**: React + AntD",
		"**go-test**: Go testing best practices",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill list missing %q in:\n%s", want, got)
		}
	}
}

// TestBuildAgentsMD_EmptyReturnsEmpty: missing AGENTS.md → no message.
func TestBuildAgentsMD_EmptyReturnsEmpty(t *testing.T) {
	if got := BuildAgentsMD(""); got != "" {
		t.Errorf("empty AGENTS.md must return empty string; got:\n%s", got)
	}
	if got := BuildAgentsMD("   \n  \t  "); got != "" {
		t.Errorf("whitespace-only AGENTS.md must return empty string; got:\n%s", got)
	}
}

// TestBuildAgentsMD_WrapsInTag verifies D52 tag wrapping.
func TestBuildAgentsMD_WrapsInTag(t *testing.T) {
	got := BuildAgentsMD("Use go fmt before commit.")
	if !strings.HasPrefix(got, "<project_guidelines>\n") {
		t.Errorf("missing opening tag in:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n</project_guidelines>") {
		t.Errorf("missing closing tag in:\n%s", got)
	}
	if !strings.Contains(got, "Use go fmt before commit.") {
		t.Errorf("body lost in:\n%s", got)
	}
}

// ============================================================
// interrupt.go helpers
// ============================================================

// TestSynthesizeInterruptedResult shapes a placeholder tool_result
// matching the canonical block layout. We assert the key invariants
// the LLM API enforces (ToolUseRefID matches, IsError=true).
func TestSynthesizeInterruptedResult(t *testing.T) {
	call := llm.ContentBlock{
		Type:      llm.BlockToolUse,
		ToolUseID: "call-abc",
		ToolName:  "read_file",
		ToolInput: map[string]any{"path": "go.mod"},
	}
	got := synthesizeInterruptedResult(call)

	if got.Role != llm.RoleUser {
		t.Errorf("role = %q; want %q", got.Role, llm.RoleUser)
	}
	if len(got.Content) != 1 {
		t.Fatalf("content len = %d; want 1", len(got.Content))
	}
	b := got.Content[0]
	if b.Type != llm.BlockToolResult {
		t.Errorf("block type = %q; want %q", b.Type, llm.BlockToolResult)
	}
	if b.ToolUseRefID != "call-abc" {
		t.Errorf("ref id = %q; want call-abc", b.ToolUseRefID)
	}
	if !b.IsError {
		t.Error("synthesised result must mark IsError=true")
	}
	if !strings.Contains(b.Output, "interrupted") {
		t.Errorf("output should mention interruption; got %q", b.Output)
	}
}

// TestExtractToolUseBlocks_PreservesOrder ensures we hand the loop
// tool_uses in original order so the OpenAI tool_call_id pairing
// remains valid.
func TestExtractToolUseBlocks_PreservesOrder(t *testing.T) {
	msg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			{Type: llm.BlockText, Text: "thinking..."},
			{Type: llm.BlockToolUse, ToolUseID: "1", ToolName: "read_file"},
			{Type: llm.BlockText, Text: "now write..."},
			{Type: llm.BlockToolUse, ToolUseID: "2", ToolName: "write_file"},
		},
	}
	got := extractToolUseBlocks(msg)
	if len(got) != 2 {
		t.Fatalf("len = %d; want 2", len(got))
	}
	if got[0].ToolUseID != "1" || got[1].ToolUseID != "2" {
		t.Errorf("order broken: %v", []string{got[0].ToolUseID, got[1].ToolUseID})
	}
}

// TestExtractToolUseBlocks_NoToolUses returns an empty slice when
// the assistant message is pure text — the loop's signal for
// end-of-turn.
func TestExtractToolUseBlocks_NoToolUses(t *testing.T) {
	msg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			{Type: llm.BlockText, Text: "Done."},
		},
	}
	if got := extractToolUseBlocks(msg); len(got) != 0 {
		t.Errorf("text-only message should yield 0 tool_uses; got %d", len(got))
	}
}

// TestToolResultMsg checks the helper produces a well-formed
// user+tool_result envelope.
func TestToolResultMsg(t *testing.T) {
	call := llm.ContentBlock{Type: llm.BlockToolUse, ToolUseID: "x"}
	got := toolResultMsg(call, "hello", false)
	if got.Role != llm.RoleUser {
		t.Errorf("role = %q; want %q", got.Role, llm.RoleUser)
	}
	if got.Content[0].Output != "hello" {
		t.Errorf("output = %q; want hello", got.Content[0].Output)
	}
	if got.Content[0].IsError {
		t.Error("isError should be false")
	}

	gotErr := toolResultMsg(call, "boom", true)
	if !gotErr.Content[0].IsError {
		t.Error("isError should be true")
	}
}
