// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package agent

import (
	"context"

	"github.com/HBulgat/mini-agent/internal/llm"
)

// Compactor compresses a long history into a shorter one once it
// approaches the model's context limit. The full implementation
// lives in `internal/compaction` (Iter-3, R8); this package only
// needs the interface to plug a Nop variant in T2.6.
//
// Contract (placeholder, will be tightened by R8):
//   - MaybeCompact returns the (possibly trimmed) history plus
//     metadata describing what was archived. When no compaction is
//     needed it MUST return history unchanged with empty Archived.
//   - Implementations must be ctx-aware and idempotent: calling
//     twice on the same history yields the same archive set.
type Compactor interface {
	MaybeCompact(ctx context.Context, sessionID string, history []llm.Message) (CompactionResult, error)
}

// CompactionResult is the metadata surface for one compaction pass.
// History is the new working history (may be == input). Archived
// lists the message IDs that have been moved to the archive store.
// Summaries are the synthesised messages that replaced them.
type CompactionResult struct {
	History    []llm.Message
	Archived   []string
	Summaries  []llm.Message
	Compacted  bool // true iff the result differs from the input
}

// NopCompactor leaves history alone. Used in v1 until R8 lands.
type NopCompactor struct{}

// MaybeCompact returns the input history unchanged.
func (NopCompactor) MaybeCompact(_ context.Context, _ string, history []llm.Message) (CompactionResult, error) {
	return CompactionResult{History: history}, nil
}

// SkillLoader fetches the catalog of installed skills. The full
// implementation in `internal/skill` (Iter-4, R10) reads from disk;
// here we just need the interface so prepareInitialHistory can call
// it without an import cycle.
type SkillLoader interface {
	List(ctx context.Context) ([]SkillSummary, error)
}

// NopSkillLoader returns an empty skill list, causing
// BuildSkillList to skip injecting a "## Available Skills" system
// message. Wired into bootstrap as the v1 default.
type NopSkillLoader struct{}

// List always returns nil, nil.
func (NopSkillLoader) List(_ context.Context) ([]SkillSummary, error) { return nil, nil }

// AgentsMDLoader reads project-level AGENTS.md and returns its raw
// contents (BuildAgentsMD wraps it in <project_guidelines> tags).
// The full implementation lives in `internal/agentsmd` (T2.8);
// here we just plug a Nop fallback.
//
// Loaders MUST tolerate a missing file (return "" not an error) —
// most projects won't have AGENTS.md and that's not an error.
type AgentsMDLoader interface {
	Load(ctx context.Context, cwd string) (string, error)
}

// NopAgentsMDLoader returns no project guidance. Bootstrap installs
// it as the default until T2.8 wires the real loader.
type NopAgentsMDLoader struct{}

// Load always returns "", nil.
func (NopAgentsMDLoader) Load(_ context.Context, _ string) (string, error) { return "", nil }
