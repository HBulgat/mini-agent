// E2E smoke: BootstrapV1 → swap in mock provider → Run a 3-round
// ReAct conversation against the real toolchain (read_file, list_dir),
// real SQLite-backed Repository, real permission.Gate, real
// agentsmd.Loader. Asserts the whole pipeline end-to-end: persistence,
// tool execution, Sink event ordering, AGENTS.md injection.
//
// We deliberately do NOT reach for the real DeepSeek HTTP backend —
// CI must not depend on a live network. Instead we let BootstrapV1
// wire its real Provider, then immediately register a mock Provider
// on the LLM Registry, SetActive() onto it, and rebuild app.Agent
// with the mock.

package bootstrap_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/HBulgat/mini-agent/internal/agent"
	"github.com/HBulgat/mini-agent/internal/agentsmd"
	"github.com/HBulgat/mini-agent/internal/bootstrap"
	"github.com/HBulgat/mini-agent/internal/config"
	"github.com/HBulgat/mini-agent/internal/llm"
	"github.com/HBulgat/mini-agent/internal/session"
	"github.com/HBulgat/mini-agent/internal/trace"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// =====================================================================
// Mock provider — scriptable, captures stream requests
// =====================================================================

// scriptedTurn is one assistant response. The loop emits text first
// (if non-empty), then any tool_use blocks; StopReason is derived.
type scriptedTurn struct {
	Text      string
	ToolCalls []llm.ContentBlock
}

// mockProvider plays a fixed script and records every Stream call so
// the test can assert on what the model "saw".
type mockProvider struct {
	turns       []scriptedTurn
	idx         atomic.Int32
	mu          sync.Mutex
	seenReqs    []llm.Request
	streamCalls atomic.Int32
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Capabilities() llm.Capabilities {
	return llm.Capabilities{Model: "mock-1", ContextWindow: 128_000}
}

func (m *mockProvider) SetModel(string) error { return nil }

func (m *mockProvider) ComputeCost(_ string, _ llm.Usage) float64 { return 0 }

func (m *mockProvider) Stream(_ context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	m.mu.Lock()
	m.seenReqs = append(m.seenReqs, req)
	m.streamCalls.Add(1)
	i := int(m.idx.Load())
	if i >= len(m.turns) {
		i = len(m.turns) - 1
	}
	turn := m.turns[i]
	m.idx.Add(1)
	m.mu.Unlock()

	out := make(chan llm.StreamEvent, 8)
	go func() {
		defer close(out)
		// 1. text delta (if any)
		if turn.Text != "" {
			out <- llm.StreamEvent{
				Type:  llm.StreamDelta,
				Delta: llm.Delta{Content: turn.Text},
			}
		}
		// 2. final event with both text and tool_use blocks
		blocks := []llm.ContentBlock{}
		if turn.Text != "" {
			blocks = append(blocks, llm.ContentBlock{Type: llm.BlockText, Text: turn.Text})
		}
		blocks = append(blocks, turn.ToolCalls...)

		stop := llm.StopReasonEnd
		if len(turn.ToolCalls) > 0 {
			stop = llm.StopReasonToolCall
		}
		out <- llm.StreamEvent{
			Type: llm.StreamFinal,
			Final: &llm.FinalResponse{
				Message: llm.Message{
					Role:    llm.RoleAssistant,
					Content: blocks,
				},
				StopReason: stop,
				Usage:      llm.Usage{PromptTokens: 50, CompletionTokens: 20},
			},
		}
	}()
	return out, nil
}

var _ llm.Provider = (*mockProvider)(nil)

// =====================================================================
// Capturing sink — records the Sink contract surface
// =====================================================================

type captureSink struct {
	mu         sync.Mutex
	tokens     []string
	toolStarts []uio.ToolCallStartEvent
	toolEnds   []uio.ToolCallEndEvent
	infos      []string
	errs       []error
}

func (s *captureSink) EmitToken(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = append(s.tokens, t)
}
func (s *captureSink) EmitThinkingToken(string) {}
func (s *captureSink) EmitToolCallStart(ev uio.ToolCallStartEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolStarts = append(s.toolStarts, ev)
}
func (s *captureSink) EmitToolCallEnd(ev uio.ToolCallEndEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolEnds = append(s.toolEnds, ev)
}
func (s *captureSink) EmitMessage(_ uio.Role, _ string) {}
func (s *captureSink) EmitTrace(_ trace.Event)         {}
func (s *captureSink) EmitInfo(m string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.infos = append(s.infos, m)
}
func (s *captureSink) EmitError(e error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, e)
}

var _ uio.Sink = (*captureSink)(nil)

// =====================================================================
// The smoke test
// =====================================================================

// TestE2E_ReadFileAndListDirAndSummarise drives the full BootstrapV1
// pipeline through a 3-round ReAct conversation:
//
//	Turn 1: assistant → read_file{README.md}
//	Turn 2: assistant → list_dir{.}
//	Turn 3: assistant → "I summarise: ..."   (end_turn)
//
// Verifies:
//   - BootstrapV1 wires every layer correctly
//   - Real fs/read_file and fs/list_dir tools execute against disk
//   - Permission gate (ModeYes) lets read-only tools through
//     without prompting (per matrixDecision)
//   - SQLite Repository persists user + 3 assistant + 2 tool_result
//   - AGENTS.md is injected as a system message wrapped in
//     <project_guidelines> and rebuilt every turn (D55)
//   - Sink sees exactly two ToolCallStart and two ToolCallEnd events
//   - The model "saw" the tool_result blocks on round 2 and 3
//   - StopReason == StopEndTurn, Steps == 3
//
// This is the Iter-1 T1.9 deliverable rolled forward into Iter-2 once
// agent.Loop landed (T2.6) and the AGENTS.md loader landed (T2.8).
func TestE2E_ReadFileAndListDirAndSummarise(t *testing.T) {
	// ---- 1. Filesystem fixture: cwd containing README.md and AGENTS.md.
	workDir := t.TempDir()
	readmePath := filepath.Join(workDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Project Title\n\nThis is a tiny demo.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "AGENTS.md"), []byte("Always be concise."), 0o644); err != nil {
		t.Fatal(err)
	}
	// Add a second file so list_dir's output is non-trivial.
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The Loop reads cwd via os.Getwd; redirect it for this test.
	t.Chdir(workDir)

	// ---- 2. Build cfg + run BootstrapV1.
	dbPath := filepath.Join(t.TempDir(), "data.db")
	cfg := &config.Config{
		LLM: config.LLMCfg{
			ActiveModel: "deepseek:deepseek-chat",
			Providers: config.ProvidersCfg{
				OpenAICompat: []*config.OpenAICompatCfg{{
					Name:         "deepseek",
					BaseURL:      "https://api.deepseek.com/v1",
					APIKey:       "test-not-real",
					DefaultModel: "deepseek-chat",
				}},
			},
		},
		Permission: config.PermissionCfg{Mode: config.ModeYes},
		Storage:    config.StorageCfg{DatabasePath: dbPath},
		Log:        config.LogCfg{Level: "warn"},
		AgentsMD: config.AgentsMDCfg{
			ProjectLookup: true,
			// No global path — only the per-cwd AGENTS.md should
			// land in the system message.
		},
		Agent: config.AgentCfg{MaxSteps: 10, ToolRetryMax: 3},
	}

	app, err := bootstrap.BootstrapV1(cfg)
	if err != nil {
		t.Fatalf("BootstrapV1: %v", err)
	}
	if app == nil {
		t.Fatal("BootstrapV1 returned nil App")
	}

	// ---- 3. Inject mock provider, swap app.Agent.
	mock := &mockProvider{turns: []scriptedTurn{
		// Round 1: ask to read README.md
		{
			ToolCalls: []llm.ContentBlock{{
				Type:      llm.BlockToolUse,
				ToolUseID: "call_1",
				ToolName:  "read_file",
				ToolInput: map[string]any{"path": "README.md"},
			}},
		},
		// Round 2: ask to list cwd
		{
			ToolCalls: []llm.ContentBlock{{
				Type:      llm.BlockToolUse,
				ToolUseID: "call_2",
				ToolName:  "list_dir",
				ToolInput: map[string]any{"path": "."},
			}},
		},
		// Round 3: summarise (no tool calls → end_turn)
		{
			Text: "Summary: a tiny demo project with go.mod and README.md.",
		},
	}}
	if err := app.LLM.Register(mock); err != nil {
		t.Fatalf("register mock provider: %v", err)
	}
	if err := app.LLM.SetActive("mock:mock-1"); err != nil {
		t.Fatalf("set mock active: %v", err)
	}

	// Rebuild Agent with the mock provider so the loop dispatches
	// to it. Reuse every other dep from the bootstrapped app —
	// that's the whole point of the smoke test.
	loop, err := agent.New(agent.Deps{
		Provider:       mock,
		Registry:       app.Tools,
		PermGate:       app.Permission,
		SessRepo:       app.Repository,
		Recorder:       app.Trace,
		AgentsMDLoader: agentsmd.New(&agentsmd.Config{ProjectLookup: true}),
	}, agent.Config{MaxSteps: cfg.Agent.MaxSteps, ToolRetryMax: cfg.Agent.ToolRetryMax})
	if err != nil {
		t.Fatalf("rebuild agent.Loop: %v", err)
	}
	app.Agent = loop

	// ---- 4. Open a fresh session in the Repository.
	ctx := context.Background()
	sess, err := app.Repository.CreateSession(ctx, session.Session{
		Title: "e2e-smoke",
		Cwd:   workDir,
		Model: "mock-1",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// ---- 5. Drive Run with a capturing Sink + NopPrompter.
	sink := &captureSink{}
	res, err := app.Agent.Run(ctx, agent.RunInput{
		SessionID:   sess.ID,
		UserMessage: "Read README.md, list the cwd, then summarise.",
		Sink:        sink,
		Prompter:    uio.NopPrompter{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// ---- 6. Assertions.

	// 6a. Stop reason + step count.
	if res.StopReason != agent.StopEndTurn {
		t.Errorf("StopReason = %s; want %s", res.StopReason, agent.StopEndTurn)
	}
	if res.Steps != 3 {
		t.Errorf("Steps = %d; want 3 (read → list → summarise)", res.Steps)
	}

	// 6b. Mock provider was called exactly 3 times.
	if got := mock.streamCalls.Load(); got != 3 {
		t.Errorf("mock.Stream called %d times; want 3", got)
	}

	// 6c. AGENTS.md was injected on EVERY request (D55: not
	// persisted; rebuilt each turn).
	for i, req := range mock.seenReqs {
		var sysText strings.Builder
		for _, msg := range req.Messages {
			if msg.Role != llm.RoleSystem {
				continue
			}
			for _, blk := range msg.Content {
				if blk.Type == llm.BlockText {
					sysText.WriteString(blk.Text)
					sysText.WriteByte('\n')
				}
			}
		}
		got := sysText.String()
		if !strings.Contains(got, "<project_guidelines>") || !strings.Contains(got, "Always be concise.") {
			t.Errorf("turn %d system content missing AGENTS.md injection:\n%s", i+1, got)
		}
	}

	// 6d. Sink saw exactly 2 tool starts + 2 tool ends, in order.
	if got := len(sink.toolStarts); got != 2 {
		t.Errorf("tool starts = %d; want 2", got)
	}
	if got := len(sink.toolEnds); got != 2 {
		t.Errorf("tool ends = %d; want 2", got)
	}
	if len(sink.toolStarts) >= 2 {
		if sink.toolStarts[0].Name != "read_file" {
			t.Errorf("first tool start = %q; want read_file", sink.toolStarts[0].Name)
		}
		if sink.toolStarts[1].Name != "list_dir" {
			t.Errorf("second tool start = %q; want list_dir", sink.toolStarts[1].Name)
		}
	}
	if len(sink.toolEnds) >= 2 {
		// Both tools should have succeeded — neither may carry an
		// error code.
		if sink.toolEnds[0].Err != nil {
			t.Errorf("read_file end carried err: %v", sink.toolEnds[0].Err)
		}
		if sink.toolEnds[1].Err != nil {
			t.Errorf("list_dir end carried err: %v", sink.toolEnds[1].Err)
		}
	}

	// 6e. The tool_result blocks fed back to the model in round 2
	// must contain the actual file contents the tool produced.
	if len(mock.seenReqs) >= 2 {
		round2 := mock.seenReqs[1]
		var allUserOutputs strings.Builder
		for _, m := range round2.Messages {
			if m.Role != llm.RoleUser {
				continue
			}
			for _, blk := range m.Content {
				if blk.Type == llm.BlockToolResult {
					// llm.ContentBlock packs ToolResult body
					// directly into Output.
					allUserOutputs.WriteString(blk.Output)
					allUserOutputs.WriteByte('\n')
				}
			}
		}
		if !strings.Contains(allUserOutputs.String(), "Project Title") {
			t.Errorf("round 2 tool_result missing README content:\n%s", allUserOutputs.String())
		}
	}

	// 6f. Repository persisted the live conversation:
	// 1 user + 3 assistant + 2 tool_result = 6 messages.
	msgs, err := app.Repository.ListLiveMessages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("ListLiveMessages: %v", err)
	}
	if got := len(msgs); got != 6 {
		t.Errorf("persisted live messages = %d; want 6\nmessages: %+v", got, summariseMsgs(msgs))
	}
	// First message must be the user input.
	if len(msgs) > 0 && msgs[0].Role != session.RoleUser {
		t.Errorf("first persisted message role = %s; want user", msgs[0].Role)
	}
	// Last assistant message must contain the summary text.
	var foundSummary bool
	for _, m := range msgs {
		if m.Role != session.RoleAssistant {
			continue
		}
		for _, blk := range m.Blocks {
			if blk.Type == llm.BlockText && strings.Contains(blk.Text, "Summary:") {
				foundSummary = true
			}
		}
	}
	if !foundSummary {
		t.Errorf("summary text not persisted:\n%s", summariseMsgs(msgs))
	}

	// 6g. Sink saw NO errors.
	if len(sink.errs) != 0 {
		t.Errorf("sink.errs not empty: %v", sink.errs)
	}

	// 6h. Usage was accumulated across turns: 3 turns × 50 prompt
	// + 3 × 20 completion = 150 / 60.
	if res.Usage.PromptTokens != 150 || res.Usage.CompletionTokens != 60 {
		t.Errorf("Usage = %+v; want {Prompt:150, Completion:60}", res.Usage)
	}
}

// summariseMsgs renders persisted messages compactly for failure
// diagnostics — full Block dumps make t.Errorf output unreadable.
func summariseMsgs(msgs []session.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		var b strings.Builder
		b.WriteString(string(m.Role))
		b.WriteByte(' ')
		for _, blk := range m.Blocks {
			b.WriteByte('[')
			b.WriteString(string(blk.Type))
			switch blk.Type {
			case llm.BlockText:
				txt := blk.Text
				if len(txt) > 40 {
					txt = txt[:40] + "..."
				}
				b.WriteByte(':')
				b.WriteString(txt)
			case llm.BlockToolUse:
				b.WriteByte(':')
				b.WriteString(blk.ToolName)
			}
			b.WriteByte(']')
		}
		out = append(out, b.String())
	}
	return out
}
