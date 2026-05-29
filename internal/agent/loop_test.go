// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/HBulgat/mini-agent/internal/llm"
	"github.com/HBulgat/mini-agent/internal/permission"
	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// allowAllRules returns a RuleSet that lets every Meta tool through
// the matrix and disables the hard denylist for tests (we don't
// exercise hard-denylist patterns here — those are unit-tested in
// the permission package).
func allowAllRules() *permission.RuleSet {
	return &permission.RuleSet{}
}

// newTestLoop wires the agent.Loop with a fakeProvider, in-memory
// repo, in-memory tool registry containing only the supplied tools,
// and a real permission.Gate using ModeYes (no approvals needed).
func newTestLoop(t *testing.T, prov *fakeProvider, tools []tool.Tool) (*Loop, *memRepo, error) {
	t.Helper()
	reg := tool.NewRegistry()
	for _, tl := range tools {
		if err := reg.Register(tl); err != nil {
			return nil, nil, err
		}
	}
	gate, err := permission.NewGate(allowAllRules(), permission.ModeYes, "")
	if err != nil {
		return nil, nil, err
	}
	repo := newMemRepo()
	loop, err := New(Deps{
		Provider: prov,
		Registry: reg,
		PermGate: gate,
		SessRepo: repo,
	}, Config{MaxSteps: 10})
	return loop, repo, err
}

// recordingSink stores every emitted event so tests can assert on
// them. Methods are minimal and not thread-safe — fine because the
// loop only emits sequentially within one Run.
type recordingSink struct {
	tokens     []string
	thinking   []string
	starts     []uio.ToolCallStartEvent
	ends       []uio.ToolCallEndEvent
	infos      []string
	errs       []error
}

func (s *recordingSink) EmitToken(t string)                           { s.tokens = append(s.tokens, t) }
func (s *recordingSink) EmitThinkingToken(t string)                   { s.thinking = append(s.thinking, t) }
func (s *recordingSink) EmitToolCallStart(ev uio.ToolCallStartEvent)  { s.starts = append(s.starts, ev) }
func (s *recordingSink) EmitToolCallEnd(ev uio.ToolCallEndEvent)      { s.ends = append(s.ends, ev) }
func (s *recordingSink) EmitMessage(_ uio.Role, _ string)             {}
func (s *recordingSink) EmitTrace(_ interface{ /* trace.Event */ })   {}
func (s *recordingSink) EmitInfo(m string)                            { s.infos = append(s.infos, m) }
func (s *recordingSink) EmitError(e error)                            { s.errs = append(s.errs, e) }

// We intentionally don't add `var _ uio.Sink = (*recordingSink)(nil)`
// because uio.Sink.EmitTrace expects trace.Event; tests build with
// the interface{} signature for now to avoid depending on trace's
// specific type. The Loop's runtime check via in.Sink == nil keeps
// us safe.

// ============================================================
// End-to-end tests
// ============================================================

// TestLoop_EndTurnWithoutTools is the simplest happy path: the model
// answers in one turn with no tool_calls. Loop returns StopEndTurn
// after one step and persists exactly one assistant message.
func TestLoop_EndTurnWithoutTools(t *testing.T) {
	prov := &fakeProvider{turns: []fakeTurn{
		{Text: "Hello!", Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5}},
	}}
	loop, repo, err := newTestLoop(t, prov, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := loop.Run(ctx, RunInput{
		SessionID:   "s1",
		UserMessage: "Hi",
		Sink:        uio.NopSink{},
		Prompter:    uio.NopPrompter{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopEndTurn {
		t.Errorf("stop = %s; want %s", res.StopReason, StopEndTurn)
	}
	if res.Steps != 1 {
		t.Errorf("steps = %d; want 1", res.Steps)
	}
	if res.Usage.PromptTokens != 10 || res.Usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v; want {10, 5, ...}", res.Usage)
	}
	msgs, _ := repo.ListLiveMessages(ctx, "s1")
	// Persisted: user + assistant(end) = 2.
	if len(msgs) != 2 {
		t.Errorf("persisted = %d; want 2", len(msgs))
	}
}

// TestLoop_OneToolCallThenEndTurn exercises the full ReAct cycle:
// turn 1 emits a tool_use, turn 2 returns plain text. Verifies the
// tool ran exactly once, the result was persisted, and the loop
// terminated after the second turn.
func TestLoop_OneToolCallThenEndTurn(t *testing.T) {
	echo := &echoTool{}
	prov := &fakeProvider{turns: []fakeTurn{
		{
			ToolCalls: []llm.ContentBlock{{
				Type:      llm.BlockToolUse,
				ToolUseID: "c1",
				ToolName:  "echo",
				ToolInput: map[string]any{"text": "ping"},
			}},
		},
		{Text: "Done."},
	}}
	loop, repo, err := newTestLoop(t, prov, []tool.Tool{echo})
	if err != nil {
		t.Fatal(err)
	}
	res, err := loop.Run(context.Background(), RunInput{
		SessionID:   "s1",
		UserMessage: "go",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopEndTurn || res.Steps != 2 {
		t.Errorf("stop=%s steps=%d; want end_turn / 2", res.StopReason, res.Steps)
	}
	if got := echo.calls.Load(); got != 1 {
		t.Errorf("echo called %d times; want 1", got)
	}
	// Persisted: user + assistant(turn1) + tool_result + assistant(turn2) = 4
	msgs, _ := repo.ListLiveMessages(context.Background(), "s1")
	if len(msgs) != 4 {
		t.Errorf("persisted = %d; want 4", len(msgs))
	}
}

// TestLoop_FailingToolHintAfterRetryMax: a tool that always fails
// gets the "please try a different way" hint appended to its
// tool_result body once the failure counter hits ToolRetryMax.
func TestLoop_FailingToolHintAfterRetryMax(t *testing.T) {
	tl := &failTool{err: errors.New("disk on fire")}
	// Three consecutive identical-tool turns, then end.
	mkCall := func(id string) llm.ContentBlock {
		return llm.ContentBlock{
			Type: llm.BlockToolUse, ToolUseID: id, ToolName: "fail",
			ToolInput: map[string]any{},
		}
	}
	prov := &fakeProvider{turns: []fakeTurn{
		{ToolCalls: []llm.ContentBlock{mkCall("c1")}},
		{ToolCalls: []llm.ContentBlock{mkCall("c2")}},
		{ToolCalls: []llm.ContentBlock{mkCall("c3")}},
		{Text: "Giving up."},
	}}
	loop, repo, err := newTestLoop(t, prov, []tool.Tool{tl})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loop.Run(context.Background(), RunInput{
		SessionID:   "s1",
		UserMessage: "try",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := tl.calls.Load(); got != 3 {
		t.Errorf("tool called %d times; want 3", got)
	}
	// The third tool_result should contain the retry hint.
	msgs, _ := repo.ListLiveMessages(context.Background(), "s1")
	var lastResult string
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				lastResult = b.Output
			}
		}
	}
	if !strings.Contains(lastResult, "please try a different way") {
		t.Errorf("expected retry hint after %d failures; got:\n%s",
			tl.calls.Load(), lastResult)
	}
}

// TestLoop_MaxStepsCap verifies the loop bails at MaxSteps when the
// model keeps emitting tool_calls forever.
func TestLoop_MaxStepsCap(t *testing.T) {
	echo := &echoTool{}
	// Provider always emits a tool call (never end-turns).
	prov := &fakeProvider{turns: []fakeTurn{
		{ToolCalls: []llm.ContentBlock{{
			Type: llm.BlockToolUse, ToolUseID: "loop", ToolName: "echo",
			ToolInput: map[string]any{"text": "x"},
		}}},
	}}
	reg := tool.NewRegistry()
	_ = reg.Register(echo)
	gate, _ := permission.NewGate(allowAllRules(), permission.ModeYes, "")
	loop, _ := New(Deps{
		Provider: prov,
		Registry: reg,
		PermGate: gate,
		SessRepo: newMemRepo(),
	}, Config{MaxSteps: 3})
	res, err := loop.Run(context.Background(), RunInput{
		SessionID:   "s1",
		UserMessage: "go",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopMaxSteps {
		t.Errorf("stop = %s; want %s", res.StopReason, StopMaxSteps)
	}
	if res.Steps != 3 {
		t.Errorf("steps = %d; want 3", res.Steps)
	}
}

// TestLoop_CtxCancelMidLoop: cancelling between turns yields
// StopInterrupted. We cancel before the second turn by wiring a
// turn-2 fake that itself triggers cancel via the context.
func TestLoop_CtxCancelMidLoop(t *testing.T) {
	echo := &echoTool{}
	prov := &fakeProvider{turns: []fakeTurn{
		{ToolCalls: []llm.ContentBlock{{
			Type: llm.BlockToolUse, ToolUseID: "c1", ToolName: "echo",
			ToolInput: map[string]any{"text": "ping"},
		}}},
		{Text: "Should not reach here."},
	}}
	loop, _, err := newTestLoop(t, prov, []tool.Tool{echo})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after the first echo call has been made (we know that
	// happens before the second turn starts).
	go func() {
		// Busy-wait one increment; the test is fast so this won't
		// race meaningfully.
		for echo.calls.Load() == 0 {
		}
		cancel()
	}()
	res, _ := loop.Run(ctx, RunInput{
		SessionID:   "s1",
		UserMessage: "go",
	})
	if res.StopReason != StopInterrupted {
		t.Errorf("stop = %s; want %s", res.StopReason, StopInterrupted)
	}
}

// TestLoop_ProviderStreamErrorBecomesStopError verifies that a
// StreamError event surfaces as StopError with the underlying err.
func TestLoop_ProviderStreamErrorBecomesStopError(t *testing.T) {
	prov := &fakeProvider{turns: []fakeTurn{
		{Err: errors.New("provider blew up")},
	}}
	loop, _, err := newTestLoop(t, prov, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, runErr := loop.Run(context.Background(), RunInput{
		SessionID:   "s1",
		UserMessage: "x",
	})
	if res.StopReason != StopError {
		t.Errorf("stop = %s; want %s", res.StopReason, StopError)
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "blew up") {
		t.Errorf("err should mention provider failure; got %v", runErr)
	}
}

// TestLoop_ParallelReadOnlyBucket: two concurrent ReadOnly tool_uses
// from the same assistant turn run in parallel. We verify both ran
// and both produced tool_results in original order.
func TestLoop_ParallelReadOnlyBucket(t *testing.T) {
	echo := &echoTool{}
	// Two echo calls in the same turn.
	prov := &fakeProvider{turns: []fakeTurn{
		{ToolCalls: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ToolUseID: "a", ToolName: "echo", ToolInput: map[string]any{"text": "1"}},
			{Type: llm.BlockToolUse, ToolUseID: "b", ToolName: "echo", ToolInput: map[string]any{"text": "2"}},
		}},
		{Text: "done"},
	}}
	loop, repo, err := newTestLoop(t, prov, []tool.Tool{echo})
	if err != nil {
		t.Fatal(err)
	}
	res, err := loop.Run(context.Background(), RunInput{
		SessionID:   "s1",
		UserMessage: "go",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopEndTurn {
		t.Errorf("stop = %s; want %s", res.StopReason, StopEndTurn)
	}
	if got := echo.calls.Load(); got != 2 {
		t.Errorf("echo called %d times; want 2", got)
	}
	msgs, _ := repo.ListLiveMessages(context.Background(), "s1")
	// user + assistant(2 tool_use blocks) + tool_result × 2 + assistant(end) = 5
	if len(msgs) != 5 {
		t.Errorf("persisted = %d; want 5", len(msgs))
	}
	// Verify tool_result order: first must reference "a", second "b".
	var refs []string
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				refs = append(refs, b.ToolUseRefID)
			}
		}
	}
	if len(refs) != 2 || refs[0] != "a" || refs[1] != "b" {
		t.Errorf("tool_result order broken: %v", refs)
	}
}

// TestLoop_NilSinkPrompter: passing nil for Sink/Prompter falls back
// to Nop so the Loop never panics. Important for headless test runs.
func TestLoop_NilSinkPrompter(t *testing.T) {
	prov := &fakeProvider{turns: []fakeTurn{{Text: "ok"}}}
	loop, _, err := newTestLoop(t, prov, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := loop.Run(context.Background(), RunInput{
		SessionID:   "s1",
		UserMessage: "x",
		// Sink and Prompter intentionally left nil.
	})
	if err != nil {
		t.Fatalf("Run with nil sink/prompter: %v", err)
	}
	if res.StopReason != StopEndTurn {
		t.Errorf("stop = %s; want %s", res.StopReason, StopEndTurn)
	}
}

// TestNew_RejectsMissingDeps: bootstrap should surface configuration
// mistakes early via New, not panic at first Run.
func TestNew_RejectsMissingDeps(t *testing.T) {
	cases := []struct {
		desc string
		deps Deps
	}{
		{"no Provider", Deps{Registry: tool.NewRegistry(), SessRepo: newMemRepo()}},
		{"no Registry", Deps{Provider: &fakeProvider{}, SessRepo: newMemRepo()}},
		{"no PermGate", Deps{Provider: &fakeProvider{}, Registry: tool.NewRegistry(), SessRepo: newMemRepo()}},
		{"no SessRepo", Deps{Provider: &fakeProvider{}, Registry: tool.NewRegistry()}},
	}
	gate, _ := permission.NewGate(allowAllRules(), permission.ModeYes, "")
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			// PermGate gets injected on cases where it's not the
			// missing field; same for the rest.
			if c.deps.PermGate == nil && c.desc != "no PermGate" {
				c.deps.PermGate = gate
			}
			_, err := New(c.deps, Config{})
			if err == nil {
				t.Fatal("expected error; got nil")
			}
			var me *MissingDepError
			if !errors.As(err, &me) {
				t.Errorf("expected MissingDepError; got %T: %v", err, err)
			}
		})
	}
}
