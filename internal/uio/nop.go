package uio

import (
	"context"
	"errors"

	"github.com/HBulgat/mini-agent/internal/trace"
)

// NopSink is the do-nothing Sink. Hot path with no real surface (unit
// tests, dry-run sub-agents) wires it instead of nil-checking every
// Emit call.
type NopSink struct{}

// All Emit* methods are intentionally empty — see Sink for the
// contract; this implementation says "every event is silently dropped".
func (NopSink) EmitToken(string)                  {}
func (NopSink) EmitThinkingToken(string)          {}
func (NopSink) EmitToolCallStart(ToolCallStartEvent) {}
func (NopSink) EmitToolCallEnd(ToolCallEndEvent)     {}
func (NopSink) EmitMessage(Role, string)              {}
func (NopSink) EmitTrace(trace.Event)                  {}
func (NopSink) EmitInfo(string)                          {}
func (NopSink) EmitError(error)                          {}

// Compile-time assertion: drift between the interface and the fallback
// trips the build instead of a silent runtime miss.
var _ Sink = NopSink{}

// ============================================================
// NopPrompter
// ============================================================

// ErrNoPrompter is the sentinel returned by NopPrompter and by Prompter
// implementations that refuse to handle a particular call (e.g. a
// non-interactive batch driver). Callers should treat it as "user
// unavailable" — the same as a ctx cancellation, just with a clearer
// error message.
var ErrNoPrompter = errors.New("uio: no Prompter wired (interactive prompt requested)")

// NopPrompter is the default-deny Prompter. Every method returns
// ErrNoPrompter so accidental "is there a user there?" calls in
// non-interactive contexts (cron-style automations, CI runs without
// `--yes`) fail fast instead of hanging on stdin.
//
// We deny rather than approve as the safer default: silently approving
// every request from a missing user would happily delete files under
// `--auto-edit`. Callers that *want* unattended approval should wire
// `--yes` mode + the matching Prompter implementation.
type NopPrompter struct{}

// AskApproval always returns DecisionDeny + ErrNoPrompter. Honoring
// ctx.Err() takes precedence so a canceled context surfaces correctly
// even on this stub.
func (NopPrompter) AskApproval(ctx context.Context, _ ApprovalRequest) (ApprovalDecision, error) {
	if err := ctx.Err(); err != nil {
		return DecisionDeny, err
	}
	return DecisionDeny, ErrNoPrompter
}

// AskUser returns ErrNoPrompter (or ctx.Err if canceled).
func (NopPrompter) AskUser(ctx context.Context, _ QuestionRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", ErrNoPrompter
}

// AskChoice returns ErrNoPrompter (or ctx.Err if canceled).
func (NopPrompter) AskChoice(ctx context.Context, _ ChoiceRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", ErrNoPrompter
}

var _ Prompter = NopPrompter{}
