// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

// Package agent hosts the ReAct loop that orchestrates an LLM
// provider, the tool registry, the permission gate, and the session
// repository into a working coding agent.
//
// Wiring topology (R6 §9.1):
//
//	agent.Loop ── llm.Provider           (one streaming turn per step)
//	          ── tool.Registry            (tool lookup + Invoke)
//	          ── permission.Gate          (every tool call gated)
//	          ── session.Repository       (history persistence)
//	          ── trace.Recorder           (cross-cutting observability)
//	          ── compaction.Compactor     (placeholder in v1; T3 fills)
//	          ── skill.Loader             (placeholder in v1; T4 fills)
//	          ── agentsmd.Loader          (placeholder in v1; T2.8 fills)
//
// v1 status (T2.6): the loop is functionally complete for the single-
// agent ReAct path with all 9 P0 tools. Sub-agents (Spawner / task
// tool), context compaction, skill activation, and AGENTS.md
// injection use Nop placeholders — their full implementations land
// in Iter-3/Iter-4 without changing the Loop's external API.
package agent

import (
	"context"

	"github.com/HBulgat/mini-agent/internal/llm"
	"github.com/HBulgat/mini-agent/internal/permission"
	"github.com/HBulgat/mini-agent/internal/session"
	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/trace"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// Runner is the public entry point. CLI/REPL and Web both call Run
// per user turn. Implementations MUST honor ctx cancellation within
// ~50 ms (R1 §3.7) and return a populated RunResult even on error.
type Runner interface {
	Run(ctx context.Context, in RunInput) (RunResult, error)
}

// RunInput carries everything the Loop needs to process one turn.
// SessionID is mandatory — the loop persists every assistant /
// tool_result message keyed by it. UserMessage is the verbatim
// user prompt (CLI line / Web textarea contents).
//
// Sink and Prompter let one Loop instance service many UI surfaces
// without re-construction; both fields MUST be non-nil. Use
// uio.NopSink{} / uio.NopPrompter{} as defaults.
type RunInput struct {
	SessionID   string
	UserMessage string
	Sink        uio.Sink
	Prompter    uio.Prompter

	// IsResume signals that the loop should hydrate prior history
	// from the repository before appending UserMessage. New sessions
	// pass false.
	IsResume bool
}

// RunResult is the outcome of one Run. StopReason classifies *why*
// the loop stopped; Steps is the count of LLM round-trips performed
// (1-based); Usage aggregates every turn's token + cost figures;
// LastError is non-nil only when StopReason == StopError.
type RunResult struct {
	StopReason StopReason
	Steps      int
	Usage      llm.Usage
	LastError  error
}

// StopReason enumerates the legal terminations of a Run.
type StopReason string

const (
	// StopEndTurn — the model produced an assistant message with no
	// tool_use blocks, signaling natural turn completion.
	StopEndTurn StopReason = "end_turn"

	// StopMaxSteps — the loop reached cfg.MaxSteps without an
	// end-turn message. Surfaces to the user so they can decide
	// whether to extend or accept the truncated answer.
	StopMaxSteps StopReason = "max_steps"

	// StopInterrupted — ctx was cancelled mid-loop. Tool results for
	// any in-flight tool_uses have been synthesized so the persisted
	// history remains protocol-legal (D64).
	StopInterrupted StopReason = "interrupted"

	// StopError — an unrecoverable error occurred. LastError carries
	// the wrapped cause; downstream UIs render it via EmitError.
	StopError StopReason = "error"
)

// Config contains the agent's runtime knobs. Defaults match R6 §9.12
// table; bootstrap supplies them from cfg.Agent.* once that section
// is wired (T2.6 ships sensible hardcoded defaults).
type Config struct {
	// MaxSteps caps the number of LLM round-trips per Run; 0 falls
	// back to DefaultMaxSteps. The cap protects against infinite
	// tool-call loops (R6 §9.6 anti-pattern).
	MaxSteps int

	// ToolRetryMax is the threshold past which the loop appends a
	// "please try a different way" hint to the failed tool_result
	// (D76). 0 → DefaultToolRetryMax.
	ToolRetryMax int

	// SubAgentDepthMax limits nested task tool spawns. v1 ignores
	// this (sub-agents are not yet implemented); kept in Config so
	// future releases don't need a struct migration.
	SubAgentDepthMax int

	// Compaction holds the trigger / target ratios. Honored by the
	// future compaction.Compactor; placeholder Nop ignores it.
	Compaction CompactionConfig
}

// CompactionConfig mirrors R6 §5.9.x. v1 stores the values without
// using them — Iter-3 hooks them into compaction.Compactor.
type CompactionConfig struct {
	TriggerRatio float32
	TargetRatio  float32
	KeepRecent   int
}

// Defaults applied when a Config field is the zero value.
const (
	DefaultMaxSteps     = 50
	DefaultToolRetryMax = 3
	DefaultSubDepthMax  = 1

	DefaultCompactTrigger = 0.8
	DefaultCompactTarget  = 0.5
	DefaultCompactKeep    = 5
)

// withDefaults returns cfg with zero fields filled in. Stored on the
// Loop so every Run sees identical values.
func withDefaults(cfg Config) Config {
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = DefaultMaxSteps
	}
	if cfg.ToolRetryMax <= 0 {
		cfg.ToolRetryMax = DefaultToolRetryMax
	}
	if cfg.SubAgentDepthMax <= 0 {
		cfg.SubAgentDepthMax = DefaultSubDepthMax
	}
	if cfg.Compaction.TriggerRatio <= 0 {
		cfg.Compaction.TriggerRatio = DefaultCompactTrigger
	}
	if cfg.Compaction.TargetRatio <= 0 {
		cfg.Compaction.TargetRatio = DefaultCompactTarget
	}
	if cfg.Compaction.KeepRecent <= 0 {
		cfg.Compaction.KeepRecent = DefaultCompactKeep
	}
	return cfg
}

// Deps bundles the collaborators the Loop needs. Every field is
// required *except* the placeholder loaders (Compactor / SkillLoader
// / AgentsMDLoader) which fall back to Nop variants when nil.
type Deps struct {
	Provider       llm.Provider
	Registry       tool.Registry
	PermGate       permission.Gate
	SessRepo       session.Repository
	Recorder       trace.Recorder
	Compactor      Compactor
	SkillLoader    SkillLoader
	AgentsMDLoader AgentsMDLoader
}

// New constructs a Loop. Required deps are validated; missing
// optional deps are filled with Nop variants. Returns an error
// rather than panicking so bootstrap can surface configuration
// mistakes through its standard error path.
func New(deps Deps, cfg Config) (*Loop, error) {
	if deps.Provider == nil {
		return nil, errMissing("Provider")
	}
	if deps.Registry == nil {
		return nil, errMissing("Registry")
	}
	if deps.PermGate == nil {
		return nil, errMissing("PermGate")
	}
	if deps.SessRepo == nil {
		return nil, errMissing("SessRepo")
	}
	if deps.Recorder == nil {
		deps.Recorder = trace.NopRecorder{}
	}
	if deps.Compactor == nil {
		deps.Compactor = NopCompactor{}
	}
	if deps.SkillLoader == nil {
		deps.SkillLoader = NopSkillLoader{}
	}
	if deps.AgentsMDLoader == nil {
		deps.AgentsMDLoader = NopAgentsMDLoader{}
	}

	pb, err := NewPromptBuilder()
	if err != nil {
		return nil, err
	}

	return &Loop{
		deps:    deps,
		cfg:     withDefaults(cfg),
		prompts: pb,
	}, nil
}

// Loop is the concrete Runner. Construct via New; methods are safe
// for serial use per session — concurrent Run calls on the same Loop
// instance are NOT supported (each call needs its own Loop).
type Loop struct {
	deps    Deps
	cfg     Config
	prompts *PromptBuilder
}

// Compile-time check that Loop satisfies Runner.
var _ Runner = (*Loop)(nil)

// errMissing is a tiny sentinel for required-dep validation. Kept
// inline (no package-level error vars) because the message is the
// canonical way to identify the missing field.
func errMissing(name string) error {
	return &MissingDepError{Name: name}
}

// MissingDepError is the error returned by New when a required
// dependency is nil. Exposed so bootstrap can branch on it (currently
// not needed but cheap insurance).
type MissingDepError struct {
	Name string
}

func (e *MissingDepError) Error() string { return "agent.New: required dep " + e.Name + " is nil" }
