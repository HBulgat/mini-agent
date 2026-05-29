// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/HBulgat/mini-agent/internal/llm"
	"github.com/HBulgat/mini-agent/internal/session"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// Run is the public entry per user turn. It hydrates initial history
// (system prompts + optional resume + user message), then loops:
//
//	for step := 1..MaxSteps {
//	    stream → assistant message
//	    persist
//	    extract tool_uses; if none → end_turn
//	    execute (parallel readonly + sequential others)
//	    persist tool_results
//	    if interrupted → return StopInterrupted
//	}
//	return StopMaxSteps
//
// The function is *not* safe for concurrent calls on the same Loop.
// One Loop per session per turn is the assumed pattern.
func (l *Loop) Run(ctx context.Context, in RunInput) (RunResult, error) {
	if in.Sink == nil {
		in.Sink = uio.NopSink{}
	}
	if in.Prompter == nil {
		in.Prompter = uio.NopPrompter{}
	}

	history, err := l.prepareInitialHistory(ctx, in)
	if err != nil {
		return RunResult{StopReason: StopError, LastError: err}, err
	}

	// Persist the new user message so /show, resume, and the Web
	// transcript view all see it. system messages are NOT persisted
	// (D55) — they are rebuilt every turn from current cwd / skills
	// / AGENTS.md. Tool-result messages are persisted later, inside
	// the per-step branch that produced them.
	if in.UserMessage != "" {
		userMsg := llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.BlockText, Text: in.UserMessage}},
		}
		if _, err := l.deps.SessRepo.AppendMessage(ctx,
			session.FromLLM(in.SessionID, 0, userMsg),
		); err != nil {
			return RunResult{StopReason: StopError, LastError: err}, err
		}
	}

	fc := newFailureCounter()
	var totalUsage llm.Usage

	for step := 1; step <= l.cfg.MaxSteps; step++ {
		// Honour an early cancel before we burn another LLM round-trip.
		if err := ctx.Err(); err != nil {
			return RunResult{
				StopReason: StopInterrupted,
				Steps:      step - 1,
				Usage:      totalUsage,
			}, nil
		}

		// Compaction check (Nop in v1; T3 wires the real Compactor).
		// We treat its error as non-fatal — the model can still
		// answer with the un-compacted history.
		cr, _ := l.deps.Compactor.MaybeCompact(ctx, in.SessionID, history)
		if cr.Compacted {
			history = cr.History
		}

		// One streaming turn.
		final, usage, err := l.streamOnce(ctx, history, in.Sink)
		if err != nil {
			// Cancellation surfaces as an interrupted run, not error.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return RunResult{
					StopReason: StopInterrupted,
					Steps:      step,
					Usage:      totalUsage,
				}, nil
			}
			return RunResult{
				StopReason: StopError,
				Steps:      step,
				Usage:      totalUsage,
				LastError:  err,
			}, err
		}
		totalUsage = mergeUsage(totalUsage, usage)

		// Persist the assistant message. UserVisibility is the
		// default "visible" so /show-* slash commands work.
		assistant := final.Message
		if _, err := l.deps.SessRepo.AppendMessage(ctx,
			session.FromLLM(in.SessionID, 0, assistant),
		); err != nil {
			// Persistence failures are fatal — without persistence
			// we can't safely resume.
			return RunResult{
				StopReason: StopError,
				Steps:      step,
				Usage:      totalUsage,
				LastError:  fmt.Errorf("persist assistant: %w", err),
			}, err
		}
		history = append(history, assistant)

		// End-turn? If the assistant did not request any tool calls,
		// this is the natural conclusion of the turn.
		toolCalls := extractToolUseBlocks(assistant)
		if len(toolCalls) == 0 {
			return RunResult{
				StopReason: StopEndTurn,
				Steps:      step,
				Usage:      totalUsage,
			}, nil
		}

		// Execute every tool_use; the result list always has one
		// entry per call (synthesised on interrupt) so the next
		// turn's request stays protocol-legal.
		results, execErr := l.executeToolCalls(ctx, toolCalls, in, fc)
		// Persist & append regardless of execErr — that's the whole
		// point of the synthesised interrupt placeholders.
		for _, r := range results {
			if _, err := l.deps.SessRepo.AppendMessage(ctx,
				session.FromLLM(in.SessionID, 0, r),
			); err != nil {
				return RunResult{
					StopReason: StopError,
					Steps:      step,
					Usage:      totalUsage,
					LastError:  fmt.Errorf("persist tool_result: %w", err),
				}, err
			}
			history = append(history, r)
		}

		if errors.Is(execErr, context.Canceled) || errors.Is(execErr, context.DeadlineExceeded) {
			return RunResult{
				StopReason: StopInterrupted,
				Steps:      step,
				Usage:      totalUsage,
			}, nil
		}
	}

	// Out of steps without an end-turn. Surface to the user; future
	// turns can resume the session and let the model keep going.
	return RunResult{
		StopReason: StopMaxSteps,
		Steps:      l.cfg.MaxSteps,
		Usage:      totalUsage,
	}, nil
}

// prepareInitialHistory assembles the three independent system
// messages plus any resumed history plus the new user message.
//
// Per D54 the three system messages are written separately into the
// canonical history; provider codecs that flatten (Anthropic) handle
// the merge themselves. Per D55 these system messages are NOT
// persisted — they're regenerated every Run from the live env so
// cwd/time changes are reflected.
func (l *Loop) prepareInitialHistory(ctx context.Context, in RunInput) ([]llm.Message, error) {
	cwd := getCwdFromEnv() // Best-effort; T3.x will pull from session.

	history := make([]llm.Message, 0, 8)

	// [1] Built-in system prompt.
	sys, err := l.prompts.BuildSystem(cwd)
	if err != nil {
		return nil, fmt.Errorf("build system prompt: %w", err)
	}
	history = append(history, newSystemMsg(sys))

	// [2] Skill list.
	skills, err := l.deps.SkillLoader.List(ctx)
	if err != nil {
		// Treat as no skills — non-fatal.
		skills = nil
	}
	if text := BuildSkillList(skills); text != "" {
		history = append(history, newSystemMsg(text))
	}

	// [3] AGENTS.md.
	md, err := l.deps.AgentsMDLoader.Load(ctx, cwd)
	if err != nil {
		// Best-effort — missing AGENTS.md is the common case.
		md = ""
	}
	if wrapped := BuildAgentsMD(md); wrapped != "" {
		history = append(history, newSystemMsg(wrapped))
	}

	// Resumed conversation: hydrate persisted live messages
	// (excluding system rows which we never persist).
	if in.IsResume {
		persisted, err := l.deps.SessRepo.ListLiveMessages(ctx, in.SessionID)
		if err != nil {
			return nil, fmt.Errorf("list live messages: %w", err)
		}
		for _, m := range persisted {
			if m.Role == session.RoleSystem {
				continue
			}
			history = append(history, m.ToLLM())
		}
	}

	// Current user message.
	if in.UserMessage != "" {
		history = append(history, newUserMsg(in.UserMessage))
	}

	return history, nil
}

// streamOnce drains one Provider.Stream channel into a final assistant
// message. We forward token / thinking / boundary events to the Sink
// so the UI can render incrementally; the FinalResponse carries the
// full Message we attach to history.
func (l *Loop) streamOnce(
	ctx context.Context,
	history []llm.Message,
	sink uio.Sink,
) (*llm.FinalResponse, llm.Usage, error) {
	req := llm.Request{
		Messages:   history,
		Tools:      l.deps.Registry.ToSpecs(l.deps.PermGate.GetMode()),
		ToolChoice: llm.ToolChoice{Mode: llm.ToolChoiceAuto},
	}

	events, err := l.deps.Provider.Stream(ctx, req)
	if err != nil {
		return nil, llm.Usage{}, fmt.Errorf("provider stream: %w", err)
	}

	var final *llm.FinalResponse
	for ev := range events {
		switch ev.Type {
		case llm.StreamDelta:
			if ev.Delta.Content != "" {
				sink.EmitToken(ev.Delta.Content)
			}
			if ev.Delta.Thinking != "" {
				sink.EmitThinkingToken(ev.Delta.Thinking)
			}
			// ToolCallDelta is consumed by the codec; the final
			// message carries the assembled ToolUse blocks.
		case llm.StreamFinal:
			final = ev.Final
		case llm.StreamError:
			if ev.Err != nil {
				return nil, llm.Usage{}, ev.Err
			}
		case llm.StreamBlockBoundary:
			// Boundary events are advisory; UIs use them to open/
			// close collapse panels. Default Sink ignores them — we
			// don't have a dedicated emit method on the Sink yet.
		}
	}

	if final == nil {
		// Stream ended without a Final event — almost certainly a
		// provider bug or a network drop. Treat as a recoverable
		// error so the loop reports it cleanly.
		return nil, llm.Usage{}, errors.New("provider stream ended without final event")
	}
	return final, final.Usage, nil
}

// ============================================================
// helpers
// ============================================================

// newSystemMsg builds a single-block system message with the given
// text. Used by prepareInitialHistory three times — once per system
// slot (D54).
func newSystemMsg(text string) llm.Message {
	return llm.Message{
		Role: llm.RoleSystem,
		Content: []llm.ContentBlock{{
			Type: llm.BlockText,
			Text: text,
		}},
	}
}

// newUserMsg builds a plain text user message. Used for the initial
// user turn; tool_results use toolResultMsg from interrupt.go.
func newUserMsg(text string) llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{
			Type: llm.BlockText,
			Text: text,
		}},
	}
}

// mergeUsage accumulates per-turn usage into a session-level total.
// All fields add element-wise; cache fields (which only Anthropic
// emits today) follow the same pattern.
func mergeUsage(a, b llm.Usage) llm.Usage {
	a.PromptTokens += b.PromptTokens
	a.CompletionTokens += b.CompletionTokens
	a.CachedPromptTokens += b.CachedPromptTokens
	a.CacheCreationTokens += b.CacheCreationTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.CostUSD += b.CostUSD
	return a
}

// getCwdFromEnv is a placeholder that returns the process cwd until
// session-scoped cwd lands (T3.x). Wrapped as a `var func` so tests
// can stub it without t.Chdir (which races with parallel tests).
var getCwdFromEnv = func() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}
