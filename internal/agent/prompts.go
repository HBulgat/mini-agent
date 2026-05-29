// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package agent

import (
	"bytes"
	_ "embed"
	"fmt"
	"runtime"
	"strings"
	"text/template"
	"time"
)

// systemPromptTemplate is the canonical built-in system prompt
// (R6 §9.2). Embedded at build time so swapping the file requires
// recompilation — that's intentional per D51 (consistent behaviour
// across every mini-agent install).
//
//go:embed prompts/system.md
var systemPromptTemplate string

// PromptBuilder assembles the three independent system messages the
// agent prepends to every Run (D54). Each system message is written
// as a separate llm.Message with Role=System; codecs that flatten
// multiple system messages (Anthropic) handle the merge themselves.
type PromptBuilder struct {
	tpl *template.Template
}

// NewPromptBuilder parses the embedded template once and reuses it
// across every BuildSystem call. Returns an error only when the
// template fails to parse — a build-time bug we want to catch
// during bootstrap rather than at first turn.
func NewPromptBuilder() (*PromptBuilder, error) {
	tpl, err := template.New("system").Parse(systemPromptTemplate)
	if err != nil {
		return nil, fmt.Errorf("agent: parse system prompt template: %w", err)
	}
	return &PromptBuilder{tpl: tpl}, nil
}

// BuildSystem renders the built-in system prompt with the runtime
// environment substituted in. Time is RFC3339 with timezone so the
// model sees an unambiguous instant.
//
// We deliberately keep this short (~110 words). Anything longer
// burns tokens on every turn for marginal benefit; project-specific
// guidance belongs in AGENTS.md, not here.
func (b *PromptBuilder) BuildSystem(cwd string) (string, error) {
	now := time.Now()
	return b.buildSystemAt(cwd, now)
}

// buildSystemAt is the testable variant — accepts an explicit time
// so unit tests can pin the output.
func (b *PromptBuilder) buildSystemAt(cwd string, t time.Time) (string, error) {
	var buf bytes.Buffer
	err := b.tpl.Execute(&buf, struct {
		Cwd  string
		OS   string
		Time string
	}{
		Cwd:  cwd,
		OS:   runtime.GOOS,
		Time: t.Format(time.RFC3339),
	})
	if err != nil {
		return "", fmt.Errorf("agent: render system prompt: %w", err)
	}
	return buf.String(), nil
}

// SkillSummary is the minimum shape BuildSkillList needs from a
// loaded skill: name and one-line description. The full skill type
// lives in `internal/skill` (Iter-4); we keep this local copy so the
// agent package doesn't pull in the whole loader before it exists.
type SkillSummary struct {
	Name        string
	Description string
}

// BuildSkillList renders the skill enumeration system message
// (D54 second slot). Returns "" when there are no skills so the
// caller can skip injecting an empty system message.
//
// The format is intentionally verbose ("To activate a skill...") so
// the model understands the two-step pattern: it sees the list here,
// then calls the `skill` tool to pull a specific skill's full
// instructions into context.
func BuildSkillList(skills []SkillSummary) string {
	if len(skills) == 0 {
		return ""
	}
	var buf bytes.Buffer
	buf.WriteString("## Available Skills\n\n")
	buf.WriteString("The following skills are available. To activate a skill (load its full instructions into context), use the `skill` tool with the skill name.\n\n")
	for _, s := range skills {
		fmt.Fprintf(&buf, "- **%s**: %s\n", s.Name, s.Description)
	}
	buf.WriteString("\nIf no skill matches the task, proceed without loading any skill.\n")
	return buf.String()
}

// BuildAgentsMD wraps the project-level AGENTS.md content in the
// canonical <project_guidelines> tag (D52). Returns "" for empty
// input so the caller skips the message.
//
// The wrapping tag is what tells the model "this is project-specific
// instruction, not generic guidance" — without it, AGENTS.md content
// blends in with the built-in system prompt.
func BuildAgentsMD(content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	return "<project_guidelines>\n" + content + "\n</project_guidelines>"
}
