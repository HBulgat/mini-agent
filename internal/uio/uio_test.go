package uio

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/HBulgat/mini-agent/internal/trace"
)

// ============================================================
// NopSink
// ============================================================

// TestNopSink_DoesNotPanic exercises every Emit method to catch any
// future drift between the Sink interface and the Nop implementation.
// We deliberately don't compare outputs — there are no outputs.
func TestNopSink_DoesNotPanic(t *testing.T) {
	var s Sink = NopSink{}
	s.EmitToken("a")
	s.EmitThinkingToken("b")
	s.EmitToolCallStart(ToolCallStartEvent{CallID: "c", Name: "n"})
	s.EmitToolCallEnd(ToolCallEndEvent{CallID: "c", Succeeded: true})
	s.EmitMessage(RoleAssistant, "hi")
	s.EmitTrace(trace.Event{Type: trace.TypeAgentStep})
	s.EmitInfo("info")
	s.EmitError(errors.New("nope"))
}

// ============================================================
// NopPrompter
// ============================================================

// TestNopPrompter_AlwaysDeny verifies the "default-deny" stance the
// docstring promises: every method returns ErrNoPrompter unless the
// context is canceled (in which case ctx.Err takes precedence).
func TestNopPrompter_AlwaysDeny(t *testing.T) {
	var p Prompter = NopPrompter{}
	ctx := context.Background()

	d, err := p.AskApproval(ctx, ApprovalRequest{ToolName: "bash"})
	if !errors.Is(err, ErrNoPrompter) {
		t.Errorf("AskApproval err: got %v, want ErrNoPrompter", err)
	}
	if d != DecisionDeny {
		t.Errorf("AskApproval decision: got %v, want DecisionDeny", d)
	}

	if _, err := p.AskUser(ctx, QuestionRequest{Question: "?"}); !errors.Is(err, ErrNoPrompter) {
		t.Errorf("AskUser err: got %v, want ErrNoPrompter", err)
	}
	if _, err := p.AskChoice(ctx, ChoiceRequest{Question: "?"}); !errors.Is(err, ErrNoPrompter) {
		t.Errorf("AskChoice err: got %v, want ErrNoPrompter", err)
	}
}

// TestNopPrompter_CtxCancellation verifies that a canceled context
// surfaces ctx.Err() (not ErrNoPrompter) so callers can distinguish
// "user interrupted" from "no user attached".
func TestNopPrompter_CtxCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var p Prompter = NopPrompter{}
	if _, err := p.AskApproval(ctx, ApprovalRequest{}); !errors.Is(err, context.Canceled) {
		t.Errorf("AskApproval canceled: got %v, want context.Canceled", err)
	}
	if _, err := p.AskUser(ctx, QuestionRequest{}); !errors.Is(err, context.Canceled) {
		t.Errorf("AskUser canceled: got %v, want context.Canceled", err)
	}
	if _, err := p.AskChoice(ctx, ChoiceRequest{}); !errors.Is(err, context.Canceled) {
		t.Errorf("AskChoice canceled: got %v, want context.Canceled", err)
	}
}

// ============================================================
// MultiSink
// ============================================================

// captureSink records every Emit call to a thread-safe log so we can
// assert what the MultiSink fanned out. Methods append a tagged event
// to the slice; the test inspects it afterward.
type captureSink struct {
	mu     sync.Mutex
	events []string
}

func (c *captureSink) record(s string) {
	c.mu.Lock()
	c.events = append(c.events, s)
	c.mu.Unlock()
}
func (c *captureSink) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.events))
	copy(out, c.events)
	return out
}

func (c *captureSink) EmitToken(t string)               { c.record("token:" + t) }
func (c *captureSink) EmitThinkingToken(t string)       { c.record("think:" + t) }
func (c *captureSink) EmitToolCallStart(e ToolCallStartEvent) { c.record("tcs:" + e.CallID) }
func (c *captureSink) EmitToolCallEnd(e ToolCallEndEvent)     { c.record("tce:" + e.CallID) }
func (c *captureSink) EmitMessage(_ Role, content string)     { c.record("msg:" + content) }
func (c *captureSink) EmitTrace(e trace.Event)                 { c.record("trace:" + string(e.Type)) }
func (c *captureSink) EmitInfo(m string)                       { c.record("info:" + m) }
func (c *captureSink) EmitError(err error)                     { c.record("err:" + err.Error()) }

// Compile-time check that captureSink satisfies Sink.
var _ Sink = (*captureSink)(nil)

// TestMultiSink_FanOut verifies that every emit reaches every delegate
// in order. We use two delegates so we catch both "all fired" and
// "in the right order" with one suite.
func TestMultiSink_FanOut(t *testing.T) {
	a := &captureSink{}
	b := &captureSink{}
	m := NewMultiSink(a, b)

	m.EmitToken("hi")
	m.EmitThinkingToken("zzz")
	m.EmitToolCallStart(ToolCallStartEvent{CallID: "1", Name: "read", StartAt: time.Now()})
	m.EmitToolCallEnd(ToolCallEndEvent{CallID: "1", Succeeded: true})
	m.EmitMessage(RoleAssistant, "hello")
	m.EmitTrace(trace.Event{Type: trace.TypeAgentStep})
	m.EmitInfo("ready")
	m.EmitError(errors.New("warn"))

	want := []string{
		"token:hi", "think:zzz", "tcs:1", "tce:1",
		"msg:hello", "trace:agent.step", "info:ready", "err:warn",
	}
	for _, sink := range []*captureSink{a, b} {
		got := sink.snapshot()
		if len(got) != len(want) {
			t.Errorf("delegate len: got %d, want %d (events=%v)", len(got), len(want), got)
			continue
		}
		for i, ev := range want {
			if got[i] != ev {
				t.Errorf("delegate[%d] event[%d]: got %q, want %q", 0, i, got[i], ev)
			}
		}
	}
}

// TestMultiSink_NilFiltering ensures a nil delegate is silently
// discarded — bootstrap may pass nil for an optional Sink.
func TestMultiSink_NilFiltering(t *testing.T) {
	a := &captureSink{}
	m := NewMultiSink(nil, a, nil)
	if m.Len() != 1 {
		t.Fatalf("Len after nil filtering: got %d, want 1", m.Len())
	}
	m.EmitToken("x")
	if got := a.snapshot(); len(got) != 1 || got[0] != "token:x" {
		t.Errorf("event reached delegate: got %v", got)
	}
}

// TestMultiSink_NilReceiver verifies that calling methods on a nil
// *MultiSink is safe. Useful for the "no observability wired" path.
func TestMultiSink_NilReceiver(t *testing.T) {
	var m *MultiSink
	m.EmitToken("safe")
	m.EmitInfo("safe")
	if m.Len() != 0 {
		t.Errorf("nil Len: got %d, want 0", m.Len())
	}
}

// TestMultiSink_PanicPropagates documents that we deliberately do NOT
// recover from delegate panics — a buggy Sink should surface, not be
// swallowed.
func TestMultiSink_PanicPropagates(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from delegate to propagate")
		}
	}()
	m := NewMultiSink(panicSink{})
	m.EmitToken("boom")
}

type panicSink struct{ NopSink }

func (panicSink) EmitToken(string) { panic("intentional") }

// ============================================================
// Enum sanity
// ============================================================

// TestApprovalDecisionOrder pins the integer ordering — agent code
// that compares Decisions (e.g. "is this at least Approve?") relies
// on it.
func TestApprovalDecisionOrder(t *testing.T) {
	if !(DecisionDeny < DecisionApprove && DecisionApprove < DecisionApproveAlways) {
		t.Errorf("ApprovalDecision order broken: deny=%d approve=%d always=%d",
			DecisionDeny, DecisionApprove, DecisionApproveAlways)
	}
}

func TestRiskLevelOrder(t *testing.T) {
	if !(RiskLow < RiskMedium && RiskMedium < RiskHigh) {
		t.Errorf("RiskLevel order broken: low=%d medium=%d high=%d", RiskLow, RiskMedium, RiskHigh)
	}
}
