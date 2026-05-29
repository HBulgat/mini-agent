// Package tool defines the unified contract every executable capability
// of the agent must implement. It is intentionally a *leaf* package in
// the dependency graph (architecture doc §1.4): everything in the agent
// pipeline depends on tool, but tool itself depends only on stdlib + the
// canonical types in internal/llm.
//
// The tool package owns:
//   - The Tool interface (Name / Description / Schema / Category /
//     Invoke), R7-1' §10.2 R6.
//   - The Result type, R7-1' §10.4.4 (UserLimited vs ForcedTruncated
//     instead of the original R2 Truncated boolean).
//   - The Error type + ErrorCode enum (9 codes, R7-1' §10.2 R3).
//   - The Category enum (read-only / write / execute / network / meta).
//   - A duplicate of permission.Mode kept locally so the dependency
//     edge points permission→tool, not the other way around (avoiding
//     an import cycle that would otherwise force an awkward refactor).
//
// Per D31, public interface methods take value types; method receivers
// on the registry implementation are pointer.

package tool

import (
	"context"
)

// Tool is the unified contract every agent capability implements.
//
// All four metadata accessors must be cheap (called by the registry on
// startup, by the LLM-spec serializer on every request, and by trace
// callsites). Implementations must NOT cache mutable per-call state in
// the receiver — Invoke is the only stateful entry point.
type Tool interface {
	// Name returns a stable, lowercase identifier (e.g. "read_file").
	// Used as the dictionary key in Registry and as the tool_use name
	// sent to the LLM. Returning an empty string is a programming bug
	// caught by Registry.Register at startup (D84).
	Name() string

	// Description is the long-form English text the LLM sees when
	// deciding whether to call this tool. R7-1' D71 mandates the
	// "when to use / when NOT to use / notes" structure.
	Description() string

	// Schema returns the JSON-Schema for the tool's input parameters,
	// produced by reflecting an internal <Name>Args struct via
	// invopop/jsonschema (D69). The map MUST contain at least a "type"
	// key (validated at registration time by D84).
	Schema() map[string]any

	// Category drives the permission matrix (mode × category table in
	// docs §4) and the agent loop's parallel bucketing (D58/D59).
	Category() Category

	// Invoke executes the tool. The map is decoded from the LLM's
	// JSON tool_use input; implementations should turn it into a
	// typed struct via decodeArgs(...) (D78) and validate ranges.
	//
	// Errors: every tool error MUST be a *Error so the agent's
	// failure-counter signature (sha256 of name + code + arg digest)
	// is computable. Plain errors are still accepted but lose the
	// fine-grained code; prefer wrapping via &Error{Code: ..., Cause: err}.
	//
	// ctx contract (D63 + R7-1' §10.2 R5):
	//   - context.Canceled         → ErrInterrupted
	//   - context.DeadlineExceeded → ErrTimeout
	// Tools doing slow IO (network, subprocess) MUST plumb ctx into
	// the OS API; short synchronous reads (a small file, a few KB) may
	// rely on natural completion finishing before the deadline.
	Invoke(ctx context.Context, input map[string]any) (Result, error)
}

// Result is what every tool returns on success. The two boolean flags
// disambiguate "user explicitly limited the output (healthy)" from
// "the tool was forced to truncate (degraded)" — important inputs to
// the compaction policy and to the trace UI.
type Result struct {
	// Content is the LLM-visible payload. For non-trivial tools it is
	// wrapped in a structured tag (e.g. <file ...>) per D73 so the
	// model can recognize the tool's identity even when many tool
	// results stream back-to-back.
	Content string

	// Display is the user-facing one-liner shown in CLI trace lines or
	// Web UI cards. Format: "<tool> <subject> (<size>) → <action>"
	// per D74; suffix "[truncated]" when ForcedTruncated || UserLimited.
	Display string

	// UserLimited is true iff the caller (LLM) passed an explicit
	// offset/limit/range parameter that narrowed the output.
	UserLimited bool

	// ForcedTruncated is true iff the tool's own internal cap (e.g.
	// read_file's 200 KB max_bytes, grep's match cap) clipped the
	// output independently of any caller-supplied limit.
	ForcedTruncated bool
}

// Category enumerates how risky / how to permission-gate a tool. The
// integer order is part of the API: ReadOnly < Write < Execute is used
// when the agent loop schedules parallel calls (it groups read-only
// calls into one parallel batch and serializes the rest, D58/D59).
type Category int

const (
	// CategoryReadOnly: pure inspection. Always allowed in every mode.
	// Examples: read_file, list_dir, grep, glob.
	CategoryReadOnly Category = iota

	// CategoryWrite: filesystem mutation. Requires --auto-edit / --yes
	// or per-call approval in default mode. Refused in --plan mode.
	// Examples: write_file, edit_file, delete_file.
	CategoryWrite

	// CategoryExecute: arbitrary command execution. Requires per-call
	// approval in default and --auto-edit; allowed in --yes (subject
	// to the hard blacklist). Refused in --plan.
	// Examples: bash.
	CategoryExecute

	// CategoryNetwork: outbound traffic to remote services. Same
	// approval policy as Execute. Examples: web_fetch, web_search.
	CategoryNetwork

	// CategoryMeta: agent internal control. Always allowed (no IO
	// risk). Examples: write_plan, task, ask_user.
	CategoryMeta
)

// String returns the snake_case category label used in trace events
// and the permission matrix doc.
func (c Category) String() string {
	switch c {
	case CategoryReadOnly:
		return "read_only"
	case CategoryWrite:
		return "write"
	case CategoryExecute:
		return "execute"
	case CategoryNetwork:
		return "network"
	case CategoryMeta:
		return "meta"
	default:
		return "unknown"
	}
}

// Mode mirrors permission.Mode. We duplicate the enum here (instead of
// importing permission) so the dependency edge stays permission→tool,
// matching the architecture doc's topology. permission.Mode can be a
// `type Mode = tool.Mode` alias if zero overhead is desired.
type Mode int

const (
	// ModeDefault: every Write/Execute/Network call requires user
	// approval; ReadOnly is always allowed.
	ModeDefault Mode = iota

	// ModeAutoEdit: filesystem writes are auto-approved; Execute /
	// Network still need approval.
	ModeAutoEdit

	// ModeYes: everything except the hard blacklist is auto-approved.
	// Used in CI / automation. Hard blacklist (rm -rf /, etc.) still
	// fires.
	ModeYes

	// ModePlan: read-only mode. Write / Execute / Network are refused
	// outright; the agent must propose a plan instead.
	ModePlan
)

// String returns the CLI-flag-friendly mode name used in --help and
// trace output.
func (m Mode) String() string {
	switch m {
	case ModeDefault:
		return "default"
	case ModeAutoEdit:
		return "auto-edit"
	case ModeYes:
		return "yes"
	case ModePlan:
		return "plan"
	default:
		return "unknown"
	}
}
