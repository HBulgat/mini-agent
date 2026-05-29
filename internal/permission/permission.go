// Package permission centralises every "should this operation be
// allowed?" decision the agent makes: mode (default / --auto-edit /
// --yes / --plan), user-supplied allow/deny rules, the built-in hard
// blacklist that always wins, and approval prompting through
// uio.Prompter.
//
// # Architecture (R5 §5.5 + R4 §7.3)
//
// permission depends on tool (Mode, Category) and uio (Prompter). The
// reverse edges are forbidden — tools must NOT import permission, and
// the agent loop calls Gate.Check before invoking a tool.
//
// # Decision flow (Gate.Check)
//
//  1. Hard blacklist — non-overridable; even --yes can't relax it.
//  2. User deny rules — first matching rule wins.
//  3. User allow rules — first matching rule wins (skips mode check).
//  4. Mode × Category matrix (§4.3) — yes / auto-edit / plan / default.
//  5. Approval prompt via uio.Prompter when matrix says "ask".
//
// All five layers return one of four Decisions:
//
//   - DecisionAllow         — execute the tool
//   - DecisionDeny          — refuse, surface reason to LLM
//   - DecisionDenyHard      — refuse, hard-block (cannot be approved)
//   - DecisionNeedApproval  — used internally; Check resolves to
//                             Allow/Deny via Prompter before returning
//
// # Mode alias
//
// We expose Mode as a type alias of tool.Mode so callers don't have to
// pick which package's Mode constant to use; the canonical enum lives
// in tool to keep tool free of permission imports.

package permission

import (
	"context"

	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// Mode is the user-selected permission mode (default / auto-edit /
// yes / plan). Defined as a type alias of tool.Mode so the registry
// filtering in tool and the gating in permission share one enum.
type Mode = tool.Mode

// Re-export the four canonical mode constants for ergonomic use:
//
//	permission.ModeDefault
//	permission.ModeAutoEdit
//	permission.ModeYes
//	permission.ModePlan
const (
	ModeDefault  = tool.ModeDefault
	ModeAutoEdit = tool.ModeAutoEdit
	ModeYes      = tool.ModeYes
	ModePlan     = tool.ModePlan
)

// Operation describes the call about to happen — everything the gate
// might inspect when reaching a decision. Populated by the agent loop
// from the LLM's tool_use payload.
//
// Per D31, value type (passed to Gate.Check by value).
type Operation struct {
	// ToolName is the registered tool's Name() (e.g. "read_file").
	// Required.
	ToolName string

	// Category is the tool's Category() — kept here so the gate
	// doesn't need to look up the registry on every call.
	Category tool.Category

	// Path is the filesystem path the operation targets, when
	// applicable. Empty for tools without a path argument (e.g.
	// bash). Should already be absolute when the gate sees it; the
	// agent loop is expected to call filepath.Abs upstream.
	Path string

	// Command is the bash command string for the bash tool. Empty
	// for everything else.
	Command string

	// Args is the raw input map (used for RememberApproval keying
	// and for diagnostic "Reason" messages).
	Args map[string]any
}

// Decision is the four-state outcome of a permission check.
//
// DecisionNeedApproval is an *internal* state — Gate.Check never
// returns it to the caller; instead it triggers a Prompter call and
// resolves to Allow or Deny. CheckRulesOnly *can* return it (mostly
// for tests / Web UI inspection).
type Decision int

const (
	// DecisionAllow: proceed with the tool invocation.
	DecisionAllow Decision = iota

	// DecisionDeny: refuse this specific call. The agent loop turns
	// the reason into a tool_result with appropriate framing. Counts
	// as a *policy* refusal, NOT a tool failure (does not contribute
	// to the failure counter, per §5.5.5).
	DecisionDeny

	// DecisionDenyHard: refuse and forbid retry. Even --yes can't
	// override this; the only fix is to change the request. Used by
	// the hard blacklist exclusively.
	DecisionDenyHard

	// DecisionNeedApproval: matrix says "ask user". Returned by
	// CheckRulesOnly so callers can show approval UI separately;
	// Gate.Check resolves it via Prompter.AskApproval before
	// returning to the caller.
	DecisionNeedApproval
)

// String returns the snake_case decision label used in trace events
// and the matrix doc.
func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionDeny:
		return "deny"
	case DecisionDenyHard:
		return "deny_hard"
	case DecisionNeedApproval:
		return "need_approval"
	default:
		return "unknown"
	}
}

// Result wraps a decision with an explanatory reason. The reason is
// shown to the LLM in the tool_result block on Deny / DenyHard and in
// the trace event on Allow.
type Result struct {
	Decision Decision
	Reason   string
}

// Gate is the permission service. The agent loop holds one Gate per
// process and calls Check before every tool invocation.
//
// Implementations must be safe for concurrent calls — the agent loop
// dispatches read-only tools in parallel batches (D58) and Check is
// on every path.
type Gate interface {
	// Check is the main entry point. It runs the full 5-step flow
	// (hard blacklist → user deny → user allow → mode matrix →
	// approval) and returns a terminal Decision (Allow / Deny /
	// DenyHard — never NeedApproval).
	//
	// prompter is invoked only when the matrix says "ask"; tests
	// can pass uio.NopPrompter (which always denies) for non-
	// interactive paths.
	//
	// ctx cancellation during the prompt phase resolves to Deny with
	// reason "interrupted by user" (D63 / R6).
	Check(ctx context.Context, op Operation, mode Mode, prompter uio.Prompter) (Result, error)

	// CheckRulesOnly runs only the rule layers (steps 1–3) and the
	// pure-matrix step 4, returning DecisionNeedApproval when the
	// matrix says "ask". Used by:
	//   - bash tool's command-string preflight (rule check before
	//     full Check)
	//   - the trace UI to show "what would have happened"
	//   - tests that don't want to wire a Prompter
	CheckRulesOnly(op Operation, mode Mode) Result

	// SetMode and GetMode let the REPL's `/mode` command swap the
	// active mode at runtime. Implementations must guard with a
	// mutex so concurrent agent invocations see a consistent value.
	SetMode(mode Mode)
	GetMode() Mode

	// RememberApproval records that the user picked "approve always"
	// for an operation. Subsequent identical Operations skip the
	// prompt and resolve to Allow directly (the equivalence rule is
	// implementation-defined; default = same tool name + same Path
	// or Command).
	RememberApproval(op Operation)
}

// RuleType selects allow vs deny on a UserRule. Distinct from the
// Decision enum — rules express *what to do on match*, not the final
// outcome (mode matrix can still override allow rules in some paths,
// but deny rules always win past the hard blacklist).
type RuleType int

const (
	// RuleAllow grants permission (skips mode-matrix prompting).
	RuleAllow RuleType = iota

	// RuleDeny refuses (D29).
	RuleDeny
)

// String returns the YAML-friendly label used in permissions.yaml.
func (t RuleType) String() string {
	switch t {
	case RuleAllow:
		return "allow"
	case RuleDeny:
		return "deny"
	default:
		return "unknown"
	}
}

// Granularity selects which Operation field a UserRule's pattern
// matches against (R4 §7.3.4). Values match the YAML key
// "granularity:" verbatim.
type Granularity int

const (
	// GranCommand: pattern globs against Operation.Command (bash
	// tool only; ignored for non-bash tools).
	GranCommand Granularity = iota

	// GranPath: pattern doublestar-globs against Operation.Path
	// after ${cwd} / ${home} / ~ expansion (read_file / write_file
	// / edit_file / delete_file).
	GranPath

	// GranTool: pattern is the exact tool name (no wildcards). Most
	// general — applies to every tool.
	GranTool
)

// String returns the YAML-friendly label used in permissions.yaml.
func (g Granularity) String() string {
	switch g {
	case GranCommand:
		return "command"
	case GranPath:
		return "path"
	case GranTool:
		return "tool"
	default:
		return "unknown"
	}
}
