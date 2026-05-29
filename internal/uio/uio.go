// Package uio is the agent ↔ user interaction boundary. Two interfaces:
//
//	Sink     — non-blocking, one-way events from agent to user
//	Prompter — blocking, two-way questions (approval / free-form / choice)
//
// Both interfaces are implemented twice: once for the CLI (REPL) and
// once for the Web UI backend. The agent / tool / permission packages
// depend on these interfaces ONLY — they never reach for stdin, stdout,
// or HTTP directly. That decoupling is what lets the same agent core
// drive both surfaces unchanged.
//
// Reference:
//   - docs/system-design/03-uio-abstraction.md  (R1, design narrative)
//   - docs/system-design/05-core-abstractions.md §5.2 (R2, locked types)
package uio

import (
	"context"
	"time"

	"github.com/HBulgat/mini-agent/internal/trace"
)

// Role is the speaker classification used by EmitMessage. Kept separate
// from session.Role / llm.Role so the uio package stays free of those
// dependencies (uio is consumed by both, never the other way around).
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSystem    Role = "system"
)

// ============================================================
// Sink — one-way, non-blocking
// ============================================================

// Sink receives every observable, non-interactive event the agent
// produces. Implementations:
//
//   - MUST NOT block — these methods are called from hot paths
//     (LLM streaming, tool execution). A slow Sink stalls the agent.
//   - MUST be safe for concurrent calls — multiple goroutines (the
//     LLM stream consumer + tool runners + the loop scheduler) emit
//     simultaneously.
//   - MUST NOT return errors — a dropped event is preferable to a
//     stalled agent. The CLI ReplUIO simply prints; the WebUIO buffers
//     into the SSE channel and drops on overflow.
//
// Why no error return values: per R1 §3.2.1 the contract is "best
// effort delivery". Every `Emit*` is fire-and-forget; failures (full
// SSE buffer, broken stdout pipe, etc.) are the implementation's
// problem to log internally.
type Sink interface {
	// EmitToken pushes one streaming text delta from the assistant.
	// Aggregation into full messages is the consumer's job (the CLI
	// prints as-is; the Web UI accumulates into a Bubble).
	EmitToken(text string)

	// EmitThinkingToken pushes one streaming reasoning delta.
	// Hidden by default in CLI; collapsed in Web UI (see UICfg.ShowThinking).
	EmitThinkingToken(text string)

	// EmitToolCallStart announces a tool invocation about to happen.
	// Mirrored exactly by a later EmitToolCallEnd carrying the same
	// CallID — UIs use that pairing to tie a tool card together.
	EmitToolCallStart(ev ToolCallStartEvent)

	// EmitToolCallEnd announces the result of an earlier tool call.
	// Either Succeeded == true (with Display) or Succeeded == false
	// (with Err) — never both.
	EmitToolCallEnd(ev ToolCallEndEvent)

	// EmitMessage delivers a complete, non-streamed message — used
	// when the model returns a one-shot response or when the agent
	// surfaces a synthetic message (e.g. compaction summary).
	EmitMessage(role Role, content string)

	// EmitTrace forwards a trace.Event to whatever the user has
	// enabled (`/trace on` in the REPL, or the Trace panel in Web).
	// Implementations honor the per-session toggle internally.
	EmitTrace(e trace.Event)

	// EmitInfo surfaces a free-form, low-priority status line —
	// "loaded skill 'frontend'", "session resumed", etc. UIs render
	// these as muted, dismissible notes.
	EmitInfo(msg string)

	// EmitError surfaces a non-fatal error to the user. Fatal errors
	// (the kind that abort the agent loop) flow back as a Run() return
	// instead — EmitError is for "this tool failed but we'll keep going".
	EmitError(err error)
}

// ============================================================
// Sink event payloads
// ============================================================

// ToolCallStartEvent is the metadata the UI needs to render the
// "tool is running" affordance. Args is unfiltered — sensitive payload
// scrubbing is the caller's job (see permission.Gate / D26).
type ToolCallStartEvent struct {
	CallID  string         // matches the eventual ToolCallEndEvent.CallID
	Name    string         // tool name, e.g. "read_file" / "bash"
	Args    map[string]any // already JSON-decoded
	StartAt time.Time      // wall clock of invocation start
}

// ToolCallEndEvent terminates a previously-announced ToolCallStartEvent.
// Display is the human-facing summary the UI shows when the card is
// collapsed (full output, if any, ships separately as message content).
type ToolCallEndEvent struct {
	CallID    string
	Name      string
	Succeeded bool
	Display   string        // short, single-paragraph summary; safe to print
	Err       error         // populated iff Succeeded == false
	Duration  time.Duration // measured by the agent loop, not the tool
}

// ============================================================
// Prompter — two-way, blocking
// ============================================================

// Prompter is what the agent / permission gate / tool/ask use whenever
// they need a user decision. All methods are blocking and accept a
// context.Context so a Ctrl-C / page-close can unblock them via
// ctx.Done() (R1 §3.7).
//
// Implementations MUST:
//   - Honor ctx.Err() promptly (within ~50 ms of cancellation) and
//     return it as the error result.
//   - Only return one of the documented success values when err == nil.
//   - Be safe for *serial* use — the agent never asks two things at
//     once on the same Prompter, but multiple sub-agents may share one
//     Prompter and the implementation is expected to serialize them.
type Prompter interface {
	// AskApproval blocks until the user decides on a tool invocation.
	// On ctx cancellation returns DecisionDeny + ctx.Err() so callers
	// can treat the cancelation as a refusal without special-casing.
	AskApproval(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error)

	// AskUser is the backing call for the `ask_user` tool. Returns
	// the user's free-form text answer. The empty string is a valid
	// answer; only an error indicates the user couldn't be reached.
	AskUser(ctx context.Context, req QuestionRequest) (string, error)

	// AskChoice presents a fixed set of options and returns the chosen
	// one (verbatim from req.Options). Implementations MUST validate
	// that the returned string is in req.Options before returning.
	AskChoice(ctx context.Context, req ChoiceRequest) (string, error)
}

// ApprovalRequest is the payload for AskApproval. The Risk field hints
// at the UI affordance (color, default focus); Reason explains *why*
// approval is needed (e.g. "writes outside cwd").
type ApprovalRequest struct {
	ToolName    string         // e.g. "bash" / "write_file"
	Args        map[string]any // already JSON-decoded; UIs may pretty-print
	Risk        RiskLevel
	Reason      string // one-line rationale displayed below the prompt
	Description string // longer help text, optional
}

// RiskLevel is the typed enum for ApprovalRequest.Risk. Plain ints so
// the value can be compared directly; YAML/JSON serialization is
// intentionally not supported — risk is computed at call time, not
// persisted.
type RiskLevel int

const (
	RiskLow RiskLevel = iota
	RiskMedium
	RiskHigh
)

// ApprovalDecision is the typed enum for AskApproval's return value.
// The "ApproveAlways" variant unlocks the optional remember-this rule
// path in permission.Gate; semantics (session-scope vs process-scope)
// are pinned by R7 D86 (currently: session-scope).
type ApprovalDecision int

const (
	DecisionDeny ApprovalDecision = iota
	DecisionApprove
	DecisionApproveAlways
)

// QuestionRequest is the payload for AskUser. Hint is rendered as
// muted placeholder text — implementations may ignore it.
type QuestionRequest struct {
	Question string
	Hint     string
}

// ChoiceRequest is the payload for AskChoice. Options MUST be
// non-empty; the implementation may add an implicit "cancel" path that
// surfaces as ctx.Err() rather than appearing in Options.
type ChoiceRequest struct {
	Question string
	Options  []string
}
