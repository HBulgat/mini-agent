// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/HBulgat/mini-agent/internal/llm"
	"github.com/HBulgat/mini-agent/internal/session"
	"github.com/HBulgat/mini-agent/internal/tool"
)

// ============================================================
// Fake LLM provider
// ============================================================

// fakeProvider scripts a sequence of stream turns. Each call to
// Stream returns the next turn from `turns`; reaching the end loops
// back to the last entry (so a "done" turn at the tail keeps firing
// if the loop ever asks again).
type fakeProvider struct {
	turns []fakeTurn
	idx   atomic.Int32
	mu    sync.Mutex
	// lastReq is the last Request passed to Stream. Tests use it
	// to assert on the system messages prepareInitialHistory built
	// (e.g. AGENTS.md injection).
	lastReq llm.Request
}

// fakeTurn is one scripted assistant response. ToolCalls is the list
// of tool_use blocks the assistant emits this turn; Text is any plain
// answer text. When ToolCalls is empty the loop treats it as
// end-of-turn.
type fakeTurn struct {
	Text      string
	ToolCalls []llm.ContentBlock
	Usage     llm.Usage
	Err       error // when non-nil, send a StreamError instead of Final
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Capabilities() llm.Capabilities {
	return llm.Capabilities{Model: "fake-1", ContextWindow: 100000}
}

func (f *fakeProvider) SetModel(string) error { return nil }

func (f *fakeProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	f.mu.Lock()
	f.lastReq = req
	i := int(f.idx.Load())
	if i >= len(f.turns) {
		i = len(f.turns) - 1 // clamp to last
	}
	turn := f.turns[i]
	f.idx.Add(1)
	f.mu.Unlock()

	ch := make(chan llm.StreamEvent, 4)
	go func() {
		defer close(ch)
		if turn.Err != nil {
			ch <- llm.StreamEvent{Type: llm.StreamError, Err: turn.Err}
			return
		}
		// Stream the text as a single delta first.
		if turn.Text != "" {
			ch <- llm.StreamEvent{Type: llm.StreamDelta, Delta: llm.Delta{Content: turn.Text}}
		}
		content := []llm.ContentBlock{}
		if turn.Text != "" {
			content = append(content, llm.ContentBlock{Type: llm.BlockText, Text: turn.Text})
		}
		content = append(content, turn.ToolCalls...)
		stop := llm.StopReasonEnd
		if len(turn.ToolCalls) > 0 {
			stop = llm.StopReasonToolCall
		}
		ch <- llm.StreamEvent{
			Type: llm.StreamFinal,
			Final: &llm.FinalResponse{
				Message:    llm.Message{Role: llm.RoleAssistant, Content: content},
				Usage:      turn.Usage,
				StopReason: stop,
			},
		}
	}()
	return ch, nil
}

func (f *fakeProvider) ComputeCost(_ string, _ llm.Usage) float64 { return 0 }

var _ llm.Provider = (*fakeProvider)(nil)

// ============================================================
// In-memory Repository
// ============================================================

// memRepo is an in-memory session.Repository sufficient for loop
// tests. We only implement AppendMessage / ListLiveMessages — the
// other interface methods panic if invoked, surfacing test bugs.
type memRepo struct {
	mu       sync.Mutex
	sessions map[string]session.Session
	messages map[string][]session.Message // sessionID → ordered append
	seq      int
}

func newMemRepo() *memRepo {
	return &memRepo{
		sessions: map[string]session.Session{},
		messages: map[string][]session.Message{},
	}
}

func (r *memRepo) CreateSession(_ context.Context, s session.Session) (session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[s.ID] = s
	return s, nil
}

func (r *memRepo) GetSession(_ context.Context, id string) (session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return session.Session{}, errors.New("not found")
	}
	return s, nil
}

func (r *memRepo) ListSessions(_ context.Context, _, _ int) ([]session.Session, error) {
	return nil, nil
}

func (r *memRepo) UpdateSession(_ context.Context, _ session.Session) error { return nil }
func (r *memRepo) DeleteSession(_ context.Context, _ string) error          { return nil }

func (r *memRepo) AppendMessage(_ context.Context, m session.Message) (session.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	m.SeqNo = r.seq
	r.messages[m.SessionID] = append(r.messages[m.SessionID], m)
	return m, nil
}

func (r *memRepo) ListLiveMessages(_ context.Context, sessionID string) ([]session.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]session.Message, len(r.messages[sessionID]))
	copy(out, r.messages[sessionID])
	return out, nil
}

func (r *memRepo) ListVisibleMessages(ctx context.Context, sessionID string) ([]session.Message, error) {
	return r.ListLiveMessages(ctx, sessionID)
}

func (r *memRepo) ListAllMessages(ctx context.Context, sessionID string) ([]session.Message, error) {
	return r.ListLiveMessages(ctx, sessionID)
}

func (r *memRepo) ApplyCompaction(_ context.Context, _ string, _ []string, _ []session.Message) error {
	return nil
}

func (r *memRepo) ListTodos(_ context.Context, _ string) ([]session.Todo, error) {
	return nil, nil
}

func (r *memRepo) ReplaceTodos(_ context.Context, _ string, _ []session.Todo) error {
	return nil
}

func (r *memRepo) AddUsage(_ context.Context, _, _, _ string, _ session.Usage) error {
	return nil
}

func (r *memRepo) SessionUsage(_ context.Context, _ string) (session.Usage, error) {
	return session.Usage{}, nil
}

func (r *memRepo) GlobalUsage(_ context.Context) (session.Usage, error) {
	return session.Usage{}, nil
}

var _ session.Repository = (*memRepo)(nil)

// ============================================================
// Echo tool — a Meta tool we can call without permission grief
// ============================================================

// echoTool is a trivial Meta-category tool: takes a `text` arg and
// returns the same text in Result.Content. Lets us exercise the loop
// end-to-end without depending on real fs/bash tools (which would
// require sandboxing).
type echoTool struct {
	calls atomic.Int32
}

func (t *echoTool) Name() string                  { return "echo" }
func (t *echoTool) Description() string           { return "Echo back the input text." }
func (t *echoTool) Category() tool.Category       { return tool.CategoryMeta }
func (t *echoTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string"},
		},
		"required": []any{"text"},
	}
}

func (t *echoTool) Invoke(_ context.Context, in map[string]any) (tool.Result, error) {
	t.calls.Add(1)
	text, _ := in["text"].(string)
	return tool.Result{
		Content: "echo: " + text,
		Display: "echo (" + text + ")",
	}, nil
}

var _ tool.Tool = (*echoTool)(nil)

// failTool returns the configured error every Invoke. Used for the
// failure-counter integration test.
type failTool struct {
	err   error
	calls atomic.Int32
}

func (t *failTool) Name() string                  { return "fail" }
func (t *failTool) Description() string           { return "Always fails." }
func (t *failTool) Category() tool.Category       { return tool.CategoryMeta }
func (t *failTool) Schema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *failTool) Invoke(_ context.Context, _ map[string]any) (tool.Result, error) {
	t.calls.Add(1)
	return tool.Result{}, t.err
}

var _ tool.Tool = (*failTool)(nil)

