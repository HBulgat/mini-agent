// Four-mode end-to-end acceptance: drives the full agent.Loop /
// permission.Gate / Repository pipeline through a representative
// matrix of (permission mode × tool category) cases. Every test in
// this file is an end-to-end scenario: BootstrapV1 → mock provider
// requests one tool_use → assert the gate decision flowed all the
// way to the tool layer.
//
// Reuses the mockProvider / captureSink / scriptedTurn types from
// e2e_smoke_test.go (same package, same _test.go scope).
//
// Coverage philosophy: permission_test.go already exhaustively tests
// the gate's decision logic. Here we verify that those decisions
// actually *materialise* in the live agent loop — i.e. an approved
// write_file really writes; a denied bash really doesn't fork; a
// hard-blacklisted command is denied even under --yes.
//
// The matrix selected (8 cases) hits every column of §4.3 at least
// once and every row of the four permission modes at least once:
//
//	Default   read_file  → Allow (read-only bypasses approval)
//	Default   write_file → NeedApproval → user denies → tool not invoked
//	Auto-edit write_file → Allow (auto-edit grants writes)
//	Yes       bash safe  → Allow (yes mode bypasses approval)
//	Yes       bash rm-rf-/ → DenyHard (hard blacklist overrides --yes)
//	Plan      read_file  → Allow (plan still permits reads)
//	Plan      write_file → DenyMode (plan forbids any write)
//	Default   ask_user   → Allow (Meta category passes every mode)

package bootstrap_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HBulgat/mini-agent/internal/agent"
	"github.com/HBulgat/mini-agent/internal/agentsmd"
	"github.com/HBulgat/mini-agent/internal/bootstrap"
	"github.com/HBulgat/mini-agent/internal/config"
	"github.com/HBulgat/mini-agent/internal/llm"
	"github.com/HBulgat/mini-agent/internal/session"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// =====================================================================
// Prompter fakes
// =====================================================================

// approvePrompter answers every approval with DecisionApprove.
type approvePrompter struct {
	calls int
}

func (p *approvePrompter) AskApproval(_ context.Context, _ uio.ApprovalRequest) (uio.ApprovalDecision, error) {
	p.calls++
	return uio.DecisionApprove, nil
}
func (*approvePrompter) AskUser(_ context.Context, q uio.QuestionRequest) (string, error) {
	return "ok", nil
}
func (*approvePrompter) AskChoice(_ context.Context, _ uio.ChoiceRequest) (string, error) {
	return "", nil
}

// denyPrompter answers every approval with DecisionDeny.
type denyPrompter struct {
	calls int
}

func (p *denyPrompter) AskApproval(_ context.Context, _ uio.ApprovalRequest) (uio.ApprovalDecision, error) {
	p.calls++
	return uio.DecisionDeny, nil
}
func (*denyPrompter) AskUser(_ context.Context, _ uio.QuestionRequest) (string, error) {
	return "ok", nil
}
func (*denyPrompter) AskChoice(_ context.Context, _ uio.ChoiceRequest) (string, error) {
	return "", nil
}

var (
	_ uio.Prompter = (*approvePrompter)(nil)
	_ uio.Prompter = (*denyPrompter)(nil)
)

// =====================================================================
// Common fixture
// =====================================================================

// fourModeFixture wires BootstrapV1 with the requested mode + a
// scripted mockProvider, then rebuilds Agent with the mock attached.
// Returns everything the per-test code needs: the App handle (for
// Repository access), the Sink (for tool-call assertions), and the
// fresh session ID.
type fourModeFixture struct {
	app    *bootstrap.App
	sink   *captureSink
	mock   *mockProvider
	prompt uio.Prompter
	cwd    string
	sessID string
}

func newFourModeFixture(
	t *testing.T,
	mode config.PermissionMode,
	turns []scriptedTurn,
	prompter uio.Prompter,
) *fourModeFixture {
	t.Helper()

	cwd := t.TempDir()
	t.Chdir(cwd)
	// Seed with a few files so read_file / list_dir etc. have
	// real targets.
	if err := os.WriteFile(filepath.Join(cwd, "hello.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

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
		Permission: config.PermissionCfg{Mode: mode},
		Storage:    config.StorageCfg{DatabasePath: dbPath},
		Log:        config.LogCfg{Level: "warn"},
		AgentsMD:   config.AgentsMDCfg{ProjectLookup: false},
		Agent:      config.AgentCfg{MaxSteps: 5, ToolRetryMax: 2},
	}

	app, err := bootstrap.BootstrapV1(cfg)
	if err != nil {
		t.Fatalf("BootstrapV1(%s): %v", mode, err)
	}

	mock := &mockProvider{turns: turns}
	if err := app.LLM.Register(mock); err != nil {
		t.Fatalf("register mock: %v", err)
	}
	if err := app.LLM.SetActive("mock:mock-1"); err != nil {
		t.Fatalf("set active: %v", err)
	}

	loop, err := agent.New(agent.Deps{
		Provider:       mock,
		Registry:       app.Tools,
		PermGate:       app.Permission,
		SessRepo:       app.Repository,
		Recorder:       app.Trace,
		AgentsMDLoader: agentsmd.New(&agentsmd.Config{ProjectLookup: false}),
	}, agent.Config{MaxSteps: cfg.Agent.MaxSteps, ToolRetryMax: cfg.Agent.ToolRetryMax})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	app.Agent = loop

	sess, err := app.Repository.CreateSession(context.Background(), session.Session{
		Title: "four-mode-" + string(mode),
		Cwd:   cwd,
		Model: "mock-1",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	return &fourModeFixture{
		app:    app,
		sink:   &captureSink{},
		mock:   mock,
		prompt: prompter,
		cwd:    cwd,
		sessID: sess.ID,
	}
}

// run drives one Run with the fixture's sink + prompter.
func (f *fourModeFixture) run(t *testing.T, userMsg string) agent.RunResult {
	t.Helper()
	res, err := f.app.Agent.Run(context.Background(), agent.RunInput{
		SessionID:   f.sessID,
		UserMessage: userMsg,
		Sink:        f.sink,
		Prompter:    f.prompt,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// lastToolEnd returns the last ToolCallEndEvent the sink saw, or
// nil if no tool ended. Most tests in this file expect exactly one.
func (f *fourModeFixture) lastToolEnd() *uio.ToolCallEndEvent {
	f.sink.mu.Lock()
	defer f.sink.mu.Unlock()
	if len(f.sink.toolEnds) == 0 {
		return nil
	}
	return &f.sink.toolEnds[len(f.sink.toolEnds)-1]
}

// =====================================================================
// Helpers for shaping mock turns
// =====================================================================

// turnToolUse builds a single-tool-call assistant turn.
func turnToolUse(callID, toolName string, input map[string]any) scriptedTurn {
	return scriptedTurn{
		ToolCalls: []llm.ContentBlock{{
			Type:      llm.BlockToolUse,
			ToolUseID: callID,
			ToolName:  toolName,
			ToolInput: input,
		}},
	}
}

// turnText builds an end-of-turn assistant message with no tool calls.
func turnText(text string) scriptedTurn {
	return scriptedTurn{Text: text}
}

// =====================================================================
// Case 1 — Default + read_file: read-only bypasses approval
// =====================================================================

func TestFourModes_Default_ReadFile_Allowed(t *testing.T) {
	prompter := &denyPrompter{} // would-be deny; must NOT be consulted
	f := newFourModeFixture(t, config.ModeDefault, []scriptedTurn{
		turnToolUse("c1", "read_file", map[string]any{"path": "hello.txt"}),
		turnText("done"),
	}, prompter)

	res := f.run(t, "read it")

	if res.StopReason != agent.StopEndTurn {
		t.Errorf("StopReason = %s; want end_turn", res.StopReason)
	}
	if prompter.calls != 0 {
		t.Errorf("read-only must not trigger approval; calls = %d", prompter.calls)
	}
	end := f.lastToolEnd()
	if end == nil || end.Err != nil {
		t.Errorf("read_file must succeed; got %+v", end)
	}
}

// =====================================================================
// Case 2 — Default + write_file → user denies → tool not invoked
// =====================================================================

func TestFourModes_Default_WriteFile_DeniedByUser(t *testing.T) {
	prompter := &denyPrompter{}
	f := newFourModeFixture(t, config.ModeDefault, []scriptedTurn{
		turnToolUse("c1", "write_file", map[string]any{
			"path":    "out.txt",
			"content": "should-not-write",
		}),
		turnText("ok, not writing"),
	}, prompter)

	f.run(t, "write it")

	// Approval was requested exactly once.
	if prompter.calls != 1 {
		t.Errorf("approval calls = %d; want 1", prompter.calls)
	}
	// File must NOT have been written — denial blocks the tool
	// before invocation.
	if _, err := os.Stat(filepath.Join(f.cwd, "out.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("write_file should not have run; stat err = %v", err)
	}
	// The tool_end event must carry an error (permission denied).
	end := f.lastToolEnd()
	if end == nil {
		t.Fatal("expected ToolCallEnd even on denied tool")
	}
	if end.Err == nil {
		t.Errorf("denied write_file should carry Err; got nil")
	}
}

// =====================================================================
// Case 3 — Auto-edit + write_file: writes flow without approval
// =====================================================================

func TestFourModes_AutoEdit_WriteFile_Allowed(t *testing.T) {
	prompter := &denyPrompter{} // must NOT be consulted
	f := newFourModeFixture(t, config.ModeAutoEdit, []scriptedTurn{
		turnToolUse("c1", "write_file", map[string]any{
			"path":    "auto.txt",
			"content": "auto-edited",
		}),
		turnText("written"),
	}, prompter)

	f.run(t, "write it")

	if prompter.calls != 0 {
		t.Errorf("auto-edit mode must skip approval; calls = %d", prompter.calls)
	}
	body, err := os.ReadFile(filepath.Join(f.cwd, "auto.txt"))
	if err != nil {
		t.Fatalf("auto.txt should exist: %v", err)
	}
	if string(body) != "auto-edited" {
		t.Errorf("file content = %q; want %q", string(body), "auto-edited")
	}
}

// =====================================================================
// Case 4 — Yes + bash (safe): runs without approval
// =====================================================================

func TestFourModes_Yes_BashSafe_Allowed(t *testing.T) {
	prompter := &denyPrompter{} // must NOT be consulted
	f := newFourModeFixture(t, config.ModeYes, []scriptedTurn{
		turnToolUse("c1", "bash", map[string]any{
			"command": "echo hello-from-bash",
		}),
		turnText("done"),
	}, prompter)

	f.run(t, "run it")

	if prompter.calls != 0 {
		t.Errorf("yes mode must skip approval; calls = %d", prompter.calls)
	}
	end := f.lastToolEnd()
	if end == nil || end.Err != nil {
		t.Errorf("bash should succeed; got %+v", end)
	}
}

// =====================================================================
// Case 5 — Yes + bash (rm -rf /) → hard blacklist denies even --yes
// =====================================================================

func TestFourModes_Yes_BashHardBlacklist_DeniedHard(t *testing.T) {
	prompter := &approvePrompter{} // would approve; must not be consulted either
	f := newFourModeFixture(t, config.ModeYes, []scriptedTurn{
		turnToolUse("c1", "bash", map[string]any{
			"command": "rm -rf /",
		}),
		turnText("blocked, not retrying"),
	}, prompter)

	f.run(t, "rm rf /")

	// Approval must NOT have been asked — hard blacklist short-
	// circuits before any user-facing path.
	if prompter.calls != 0 {
		t.Errorf("hard blacklist must not trigger approval; calls = %d", prompter.calls)
	}
	end := f.lastToolEnd()
	if end == nil || end.Err == nil {
		t.Errorf("rm -rf / must be denied; end = %+v", end)
	}
	// And the result reason must mention permission/denied so
	// the model can recover.
	if end != nil && end.Err != nil {
		msg := end.Err.Error()
		if !strings.Contains(strings.ToLower(msg), "denied") &&
			!strings.Contains(strings.ToLower(msg), "permission") &&
			!strings.Contains(strings.ToLower(msg), "denylist") &&
			!strings.Contains(strings.ToLower(msg), "blacklist") {
			t.Errorf("error message should hint at denial; got %q", msg)
		}
	}
}

// =====================================================================
// Case 6 — Plan + read_file: reads still allowed
// =====================================================================

func TestFourModes_Plan_ReadFile_Allowed(t *testing.T) {
	prompter := &denyPrompter{}
	f := newFourModeFixture(t, config.ModePlan, []scriptedTurn{
		turnToolUse("c1", "read_file", map[string]any{"path": "hello.txt"}),
		turnText("done"),
	}, prompter)

	res := f.run(t, "read it")

	if res.StopReason != agent.StopEndTurn {
		t.Errorf("StopReason = %s; want end_turn", res.StopReason)
	}
	end := f.lastToolEnd()
	if end == nil || end.Err != nil {
		t.Errorf("read_file in plan mode should succeed; got %+v", end)
	}
}

// =====================================================================
// Case 7 — Plan + write_file: forbidden by mode (no approval offered)
// =====================================================================

func TestFourModes_Plan_WriteFile_DeniedByMode(t *testing.T) {
	prompter := &approvePrompter{} // would approve; mode must override
	f := newFourModeFixture(t, config.ModePlan, []scriptedTurn{
		turnToolUse("c1", "write_file", map[string]any{
			"path":    "plan-write.txt",
			"content": "should-not-write",
		}),
		turnText("blocked"),
	}, prompter)

	f.run(t, "try to write")

	// In plan mode write_file is forbidden outright — the gate
	// decides without consulting the prompter.
	if prompter.calls != 0 {
		t.Errorf("plan-mode write must NOT prompt; calls = %d", prompter.calls)
	}
	if _, err := os.Stat(filepath.Join(f.cwd, "plan-write.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("plan-write.txt should not exist; stat err = %v", err)
	}
	end := f.lastToolEnd()
	if end == nil || end.Err == nil {
		t.Errorf("plan-mode write_file must surface as error; end = %+v", end)
	}
}

// =====================================================================
// Case 8 — Default + ask_user: Meta category passes every mode
// =====================================================================

func TestFourModes_Default_AskUser_Allowed(t *testing.T) {
	prompter := &approvePrompter{} // its AskUser returns "ok"
	f := newFourModeFixture(t, config.ModeDefault, []scriptedTurn{
		turnToolUse("c1", "ask_user", map[string]any{
			"question": "May I proceed?",
		}),
		turnText("user said: ok"),
	}, prompter)

	res := f.run(t, "ask")

	if res.StopReason != agent.StopEndTurn {
		t.Errorf("StopReason = %s; want end_turn", res.StopReason)
	}
	end := f.lastToolEnd()
	if end == nil || end.Err != nil {
		t.Errorf("ask_user must succeed in default mode; end = %+v", end)
	}
	// approvePrompter.AskApproval returns Approve, but it should
	// NOT have been called: ask_user is Meta and bypasses the
	// approval gate. (We only check that the tool ran cleanly;
	// AskUser's own counter is separate from AskApproval's.)
	if prompter.calls != 0 {
		t.Errorf("ask_user (Meta) must not invoke AskApproval; calls = %d", prompter.calls)
	}
}

// =====================================================================
// Bonus — make sure the harness itself doesn't accidentally pass
// =====================================================================

// TestFourModes_HarnessSanity verifies that newFourModeFixture
// actually wires a working pipeline by running a no-tool turn.
// Catches regressions where a future bootstrap change silently
// skips agent registration etc.
func TestFourModes_HarnessSanity(t *testing.T) {
	f := newFourModeFixture(t, config.ModeDefault, []scriptedTurn{
		turnText("hello"),
	}, &approvePrompter{})
	res := f.run(t, "hi")
	if res.StopReason != agent.StopEndTurn {
		t.Errorf("StopReason = %s; want end_turn", res.StopReason)
	}
	if res.Steps != 1 {
		t.Errorf("Steps = %d; want 1", res.Steps)
	}
}
