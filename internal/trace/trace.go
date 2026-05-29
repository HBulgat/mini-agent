// Package trace defines the canonical observability event type used
// across mini-agent. The package is intentionally **business-free**:
// it pulls in nothing from internal/llm, internal/session, etc., so
// every other package can import it without forming an import cycle.
//
// The rule (per R2 §5.1.2):
//   - This package only declares types + the Recorder interface +
//     ctx helpers.
//   - The actual JSON-Lines / file-rotation writer lives in
//     internal/logs and is added in T1.1 once R12 nails down the
//     event Fields schema.
//
// Reference: docs/system-design/05-core-abstractions.md §5.1
package trace

import (
	"context"
	"time"
)

// Type is the canonical event-type discriminator. New values land here
// rather than in ad-hoc string literals so trace consumers (CLI Sink,
// Web SSE pipeline, log rotator) can switch exhaustively.
type Type string

const (
	TypeLLMRequest        Type = "llm.request"
	TypeLLMResponse       Type = "llm.response"
	TypeLLMStreamChunk    Type = "llm.stream_chunk"
	TypeLLMReasoningChunk Type = "llm.reasoning_chunk" // R3
	TypeToolCallStart     Type = "tool.call_start"
	TypeToolCallEnd       Type = "tool.call_end"
	TypeAgentStep         Type = "agent.step"
	TypeAgentSubSpawn     Type = "agent.sub_spawn"
	TypeAgentSubReturn    Type = "agent.sub_return"
	TypeCompactStart      Type = "compaction.start"
	TypeCompactEnd        Type = "compaction.end"
	TypePermissionAsk     Type = "permission.ask"
	TypePermissionResult  Type = "permission.result"
	TypeSkillLoad         Type = "skill.load"
	TypeUserInterrupt     Type = "user.interrupt"
)

// Level mirrors the typical log-level ladder. We keep it as a
// dedicated int enum (not slog.Level) so package trace stays
// dependency-free; the slog adapter lives in internal/logs.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// TraceID and SpanID are deliberately type aliases (not distinct types)
// so callers can pass plain strings without conversion. R3 will pin
// down the generation strategy (UUIDv7 prefix? hex-encoded random?);
// either choice is wire-compatible with `string`.
type (
	TraceID = string
	SpanID  = string
)

// Event is the single struct every observable point in the agent
// emits. Fields holds module-specific structured data — its shape per
// Type is documented by R12; consumers should treat unknown keys as
// future-compatible.
//
// Why a struct instead of slog.Record (D31 + dependency rule):
//   - We need to carry TraceID/SpanID/ParentSpanID through the call
//     graph; slog's groups don't model that cleanly.
//   - Fields stays open for evolution (new keys per Type) without
//     bumping a schema version.
//   - The `logs` package will translate Event → slog.Record at the
//     output boundary.
type Event struct {
	Type         Type
	Level        Level
	Time         time.Time
	TraceID      TraceID
	SpanID       SpanID
	ParentSpanID SpanID
	Message      string
	Fields       map[string]any
	Err          error
}

// Recorder is the persistence-side contract: anything that can swallow
// a stream of Events. The CLI Sink, the Web SSE pipeline, and the
// disk-bound logs writer all implement it independently.
//
// Implementations MUST be safe for concurrent calls — multiple agent
// goroutines (parent + sub-agents + tools) record simultaneously.
type Recorder interface {
	Record(ctx context.Context, e Event)
}

// ============================================================
// Context plumbing
// ============================================================

// ctxKey is unexported so other packages can't accidentally collide on
// the same context key. The empty struct keeps the zero-value check
// trivially fast.
type ctxKey struct{}

// ctxValue is what we actually stash in the context. Bundling the two
// IDs into one allocation keeps the context chain shallow when an
// agent steps through dozens of tool calls.
type ctxValue struct {
	traceID      TraceID
	parentSpanID SpanID
}

// WithTrace returns a child context tagged with the supplied trace +
// parent span IDs. Subsequent FromContext calls (typically from inside
// a tool's Run() or an llm Provider's Stream()) read these so emitted
// Events automatically carry the right correlation IDs.
//
// We accept SpanID (not Event) so multiple sibling spans can derive
// from the same parent without re-deriving the trace.
func WithTrace(ctx context.Context, traceID TraceID, parentSpanID SpanID) context.Context {
	return context.WithValue(ctx, ctxKey{}, ctxValue{
		traceID:      traceID,
		parentSpanID: parentSpanID,
	})
}

// FromContext extracts the trace + parent span ID stashed by WithTrace.
// `ok` is false when the context never went through WithTrace —
// typically the very first agent.Loop entry; callers in that position
// should generate fresh IDs.
func FromContext(ctx context.Context) (TraceID, SpanID, bool) {
	v, ok := ctx.Value(ctxKey{}).(ctxValue)
	if !ok {
		return "", "", false
	}
	return v.traceID, v.parentSpanID, true
}

// ============================================================
// Built-in Recorder fallback
// ============================================================

// NopRecorder discards every Event. Used by tests + by code paths that
// haven't been wired to a real recorder yet (so callers don't need to
// nil-check before every Record call).
type NopRecorder struct{}

// Record satisfies Recorder. Intentionally empty.
func (NopRecorder) Record(context.Context, Event) {}

// Compile-time assertion: drift between the interface and the
// fallback trips the build instead of a silent runtime miss.
var _ Recorder = NopRecorder{}
