// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/HBulgat/mini-agent/internal/llm"
	"github.com/HBulgat/mini-agent/internal/permission"
	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// executeToolCalls dispatches every tool_use block from one assistant
// turn. The output preserves input order so the persisted history
// pairs each tool_use with its corresponding tool_result block in
// the same sequence the model emitted them (OpenAI API requires it).
//
// Strategy (D58/D59 §9.6.1):
//
//   - Bucket 1: every ReadOnly call runs concurrently (sync.WaitGroup;
//     sibling failures do NOT cancel each other — the model wants
//     every result back regardless of individual outcomes).
//   - Bucket 2: every other call (Write/Execute/Network/Meta) runs
//     serially in original order. Mixing buckets in one assistant
//     turn is rare; when it happens, readonly runs first so we
//     never have "wrote then read the same path" race conditions.
//
// ctx semantics (D63 / §9.7):
//
//   - When ctx fires during the readonly batch we still wait for the
//     in-flight goroutines to finish (each tool's own Invoke responds
//     to ctx).
//   - When ctx fires between buckets, the remaining sequential calls
//     are not started; instead we synthesise [interrupted] tool_results
//     for them so the persisted history stays protocol-legal.
func (l *Loop) executeToolCalls(
	ctx context.Context,
	calls []llm.ContentBlock,
	in RunInput,
	fc *failureCounter,
) ([]llm.Message, error) {
	type indexed struct {
		idx  int
		call llm.ContentBlock
	}
	var readonly, sequential []indexed
	for i, c := range calls {
		t, ok := l.deps.Registry.Get(c.ToolName)
		if !ok || t.Category() == tool.CategoryReadOnly {
			// Unknown tool also goes to the parallel bucket — the
			// resulting "unknown tool" error is fast and order-
			// independent, no point serialising it.
			readonly = append(readonly, indexed{i, c})
		} else {
			sequential = append(sequential, indexed{i, c})
		}
	}

	results := make([]llm.Message, len(calls))

	// Bucket 1 — readonly parallel.
	if len(readonly) > 0 {
		var wg sync.WaitGroup
		for _, it := range readonly {
			wg.Add(1)
			go func(it indexed) {
				defer wg.Done()
				results[it.idx] = l.executeToolCall(ctx, it.call, in, fc)
			}(it)
		}
		wg.Wait()
	}

	// Bucket 2 — sequential. Check ctx before each call so we can
	// cleanly synthesise [interrupted] markers for the rest.
	for _, it := range sequential {
		if err := ctx.Err(); err != nil {
			// Fill the remainder of `results` (any positions we
			// haven't already filled from bucket 1) with synthesised
			// interrupted markers and bail.
			for j := 0; j < len(calls); j++ {
				if results[j].Role == "" { // zero-value sentinel
					results[j] = synthesizeInterruptedResult(calls[j])
				}
			}
			return results, err
		}
		results[it.idx] = l.executeToolCall(ctx, it.call, in, fc)
	}

	return results, nil
}

// executeToolCall handles one tool_use end-to-end:
//
//  1. Tool lookup. Unknown name → tool_result with isError=true.
//  2. Permission gate. Deny / DenyHard → tool_result with isError=true,
//     does NOT increment the failure counter (D66 / §5.5.5).
//  3. Sink emit ToolCallStart with args.
//  4. Tool.Invoke. Success → reset failure counter. Failure →
//     increment, append "please try a different way" hint past the
//     retry threshold (D76).
//  5. Sink emit ToolCallEnd with display + duration + outcome.
//
// Returns a tool_result message in every case so the caller can
// blindly append it to history.
func (l *Loop) executeToolCall(
	ctx context.Context,
	call llm.ContentBlock,
	in RunInput,
	fc *failureCounter,
) llm.Message {
	t, ok := l.deps.Registry.Get(call.ToolName)
	if !ok {
		// Unknown tools are a model error — surface as tool_result.
		return toolResultMsg(call, "unknown tool: "+call.ToolName, true)
	}

	op := buildOperation(call, t)

	// Permission check. Errors from Check itself (e.g. ctx cancel
	// before the prompter answered) flow through as a denied call
	// so the model gets a tool_result rather than the loop bailing.
	pr, err := l.deps.PermGate.Check(ctx, op, l.deps.PermGate.GetMode(), in.Prompter)
	if err != nil {
		// ctx cancellation reaches here — no failure counter bump
		// (this is not the tool's fault).
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return synthesizeInterruptedResult(call)
		}
		return toolResultMsg(call, "permission check failed: "+err.Error(), true)
	}
	switch pr.Decision {
	case permission.DecisionDenyHard:
		// Surface the denial as a Start+End pair so any Sink can
		// render a "tool was attempted but blocked" row in the
		// transcript. Without this the UI would silently swallow
		// hard denials.
		emitDeniedToolEvents(in.Sink, call, "hard denylist: "+pr.Reason)
		return toolResultMsg(call,
			"blocked by hard denylist: "+pr.Reason, true)
	case permission.DecisionDeny:
		emitDeniedToolEvents(in.Sink, call, "permission denied: "+pr.Reason)
		return toolResultMsg(call,
			"permission denied: "+pr.Reason, true)
	}

	// Announce the start.
	if in.Sink != nil {
		in.Sink.EmitToolCallStart(uio.ToolCallStartEvent{
			CallID:  call.ToolUseID,
			Name:    call.ToolName,
			Args:    call.ToolInput,
			StartAt: time.Now(),
		})
	}
	start := time.Now()
	// Thread the per-request Prompter / Sink through ctx so tools
	// that need them (e.g. ask_user) can pick up the active values
	// rather than the bootstrap-time placeholders. See uio/context.go.
	invokeCtx := uio.WithPrompter(ctx, in.Prompter)
	invokeCtx = uio.WithSink(invokeCtx, in.Sink)
	result, invErr := t.Invoke(invokeCtx, call.ToolInput)
	elapsed := time.Since(start)
	if in.Sink != nil {
		in.Sink.EmitToolCallEnd(uio.ToolCallEndEvent{
			CallID:    call.ToolUseID,
			Name:      call.ToolName,
			Succeeded: invErr == nil,
			Display:   result.Display,
			Err:       invErr,
			Duration:  elapsed,
		})
	}

	sig := signature(call.ToolName, call.ToolInput)
	if invErr != nil {
		n := fc.Increment(sig)
		body := invErr.Error()
		if n >= l.cfg.ToolRetryMax {
			body += "\n\n[Note] This approach has failed " + strconv.Itoa(n) +
				" times in a row — please try a different way."
		}
		return toolResultMsg(call, body, true)
	}

	// Success — reset the counter so the next failure starts fresh.
	fc.Reset(sig)
	return toolResultMsg(call, result.Content, false)
}

// buildOperation projects the tool_use block into the permission
// gate's Operation type. Path / Command extraction is best-effort:
//
//   - For path-style args we pull `path` (read_file, write_file,
//     edit_file, delete_file, list_dir) — the permission gate uses
//     it for path-prefix rule matching.
//   - For bash we pull `command` so the hard-blacklist patterns
//     can fire.
//   - Everything else has Path / Command empty; the gate's matrix
//     decision still works (mode × category).
//
// Path is converted to absolute when possible; the gate's matcher
// substitutes ${cwd}/${home} into rule patterns and then matches
// against the absolute form.
func buildOperation(call llm.ContentBlock, t tool.Tool) permission.Operation {
	op := permission.Operation{
		ToolName: call.ToolName,
		Category: t.Category(),
		Args:     call.ToolInput,
	}
	if path, ok := call.ToolInput["path"].(string); ok && path != "" {
		// best-effort absolute conversion; if it fails, leave as-is
		// so the gate at least sees the literal.
		if abs, err := filepath.Abs(path); err == nil {
			op.Path = abs
		} else {
			op.Path = path
		}
	}
	if cmd, ok := call.ToolInput["command"].(string); ok {
		op.Command = cmd
	}
	return op
}

// emitDeniedToolEvents surfaces a permission denial through the
// Sink as a Start/End pair. This keeps the Sink contract uniform
// — every attempted tool invocation shows up regardless of outcome
// — so the CLI / Web UI can render denials with the same widget as
// failed tool calls.
//
// Duration is left at zero because no actual work was performed.
// Err carries a sentinel-style permission error that the UI can
// pattern-match on (e.g. to render in red).
func emitDeniedToolEvents(sink uio.Sink, call llm.ContentBlock, reason string) {
	if sink == nil {
		return
	}
	sink.EmitToolCallStart(uio.ToolCallStartEvent{
		CallID:  call.ToolUseID,
		Name:    call.ToolName,
		Args:    call.ToolInput,
		StartAt: time.Now(),
	})
	sink.EmitToolCallEnd(uio.ToolCallEndEvent{
		CallID:    call.ToolUseID,
		Name:      call.ToolName,
		Succeeded: false,
		Err:       errors.New(reason),
	})
}

// _ keeps the fmt import in scope when we add formatted error
// messages later — gofmt would otherwise drop it. Cheap insurance.
var _ = fmt.Sprintf
