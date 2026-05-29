package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HBulgat/mini-agent/internal/agentsmd"
	"github.com/HBulgat/mini-agent/internal/llm"
	"github.com/HBulgat/mini-agent/internal/permission"
	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// newTestLoopWithAgentsMD wires the loop with a real agentsmd.Loader
// pointing at the supplied global / project file paths. Either may
// be empty to skip that source.
func newTestLoopWithAgentsMD(t *testing.T, prov *fakeProvider, globalPath string, projectLookup bool) (*Loop, *memRepo, error) {
	t.Helper()
	reg := tool.NewRegistry()
	gate, err := permission.NewGate(allowAllRules(), permission.ModeYes, "")
	if err != nil {
		return nil, nil, err
	}
	repo := newMemRepo()
	loader := agentsmd.New(&agentsmd.Config{
		GlobalPath:    globalPath,
		ProjectLookup: projectLookup,
	})
	loop, err := New(Deps{
		Provider:       prov,
		Registry:       reg,
		PermGate:       gate,
		SessRepo:       repo,
		AgentsMDLoader: loader,
	}, Config{MaxSteps: 5})
	return loop, repo, err
}

// findSystemContent flattens every system message in req.Messages
// into a single string so tests can grep for embedded markers.
func findSystemContent(req llm.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		if m.Role != llm.RoleSystem {
			continue
		}
		for _, blk := range m.Content {
			if blk.Type == llm.BlockText {
				b.WriteString(blk.Text)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

// =====================================================================
// Tests
// =====================================================================

// TestLoop_AgentsMDInjected_GlobalAndProject verifies the end-to-end
// AGENTS.md flow: real loader → prepareInitialHistory → BuildAgentsMD
// → into req.Messages as a system message wrapped in
// <project_guidelines>. The fakeProvider captures the request so we
// can assert on what the model would have seen.
func TestLoop_AgentsMDInjected_GlobalAndProject(t *testing.T) {
	tmp := t.TempDir()
	gpath := filepath.Join(tmp, "global.md")
	if err := os.WriteFile(gpath, []byte("Use TypeScript strict mode."), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write a project-level AGENTS.md in the cwd that the loop will
	// see. The loop uses os.Getwd() so we t.Chdir() into tmp.
	t.Chdir(tmp)
	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("Always run gofmt."), 0o644); err != nil {
		t.Fatal(err)
	}

	prov := &fakeProvider{turns: []fakeTurn{
		{Text: "ok"},
	}}
	loop, _, err := newTestLoopWithAgentsMD(t, prov, gpath, true)
	if err != nil {
		t.Fatal(err)
	}

	_, err = loop.Run(context.Background(), RunInput{
		SessionID:   "s1",
		UserMessage: "hi",
		Sink:        uio.NopSink{},
		Prompter:    uio.NopPrompter{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	sys := findSystemContent(prov.lastReq)
	for _, want := range []string{
		"<project_guidelines>",
		"Use TypeScript strict mode.",
		"---",
		"Always run gofmt.",
		"</project_guidelines>",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system content missing %q\n--- got ---\n%s", want, sys)
		}
	}
}

// TestLoop_AgentsMDAbsent_NoSystemMessage verifies that when no
// AGENTS.md file is found, the loader returns empty and the third
// system message is omitted entirely (no stray <project_guidelines/>
// shell).
func TestLoop_AgentsMDAbsent_NoSystemMessage(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)

	prov := &fakeProvider{turns: []fakeTurn{{Text: "ok"}}}
	// global path doesn't exist; project lookup off.
	loop, _, err := newTestLoopWithAgentsMD(t, prov, filepath.Join(tmp, "missing.md"), false)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := loop.Run(context.Background(), RunInput{
		SessionID:   "s1",
		UserMessage: "hi",
		Sink:        uio.NopSink{},
		Prompter:    uio.NopPrompter{},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sys := findSystemContent(prov.lastReq)
	if strings.Contains(sys, "<project_guidelines>") {
		t.Errorf("expected no <project_guidelines> when AGENTS.md absent; got:\n%s", sys)
	}
}

// TestLoop_AgentsMDLoaderError_FailSoft: if the loader returns a
// non-nil error from Load, the loop must NOT propagate it as a Run
// error. AGENTS.md is best-effort guidance — losing it should
// degrade quality, not break the agent loop.
//
// This matches loop.go's prepareInitialHistory contract: "Best-effort
// — missing AGENTS.md is the common case."
func TestLoop_AgentsMDLoaderError_FailSoft(t *testing.T) {
	prov := &fakeProvider{turns: []fakeTurn{{Text: "ok"}}}
	reg := tool.NewRegistry()
	gate, _ := permission.NewGate(allowAllRules(), permission.ModeYes, "")
	repo := newMemRepo()

	loop, err := New(Deps{
		Provider:       prov,
		Registry:       reg,
		PermGate:       gate,
		SessRepo:       repo,
		AgentsMDLoader: errLoader{},
	}, Config{MaxSteps: 5})
	if err != nil {
		t.Fatal(err)
	}

	res, runErr := loop.Run(context.Background(), RunInput{
		SessionID:   "s1",
		UserMessage: "hi",
		Sink:        uio.NopSink{},
		Prompter:    uio.NopPrompter{},
	})
	if runErr != nil {
		t.Errorf("expected loader error to be swallowed; got: %v", runErr)
	}
	if res.StopReason != StopEndTurn {
		t.Errorf("stop = %s; want end_turn", res.StopReason)
	}

	// And: no <project_guidelines> block leaks into the system
	// content even though the loader returned (empty, error).
	sys := findSystemContent(prov.lastReq)
	if strings.Contains(sys, "<project_guidelines>") {
		t.Errorf("loader error should suppress guidelines wrapper; got:\n%s", sys)
	}
}

// errLoader fails every Load with a sentinel error.
type errLoader struct{}

func (errLoader) Load(_ context.Context, _ string) (string, error) {
	return "", os.ErrPermission
}
