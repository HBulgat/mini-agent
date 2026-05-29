// Package llm defines the canonical, provider-agnostic conversation
// contract used by everything above the LLM driver layer (agent, tool,
// session, compaction, etc.). Concrete protocol adapters live in
// sub-packages under internal/llm/<provider>/.
//
// This file is the T1.5 early slice of T1.3 — only the *types* the
// session repository needs to round-trip messages through SQLite. The
// Provider implementations (network layer, codecs, retries) are the
// rest of T1.3 / T1.4 and intentionally absent here.
//
// Reference: docs/system-design/05-core-abstractions.md §5.3 (R3 + R5
// revisions). When updating types, always cross-check against §5.3 and
// against the Codec contract in 06-session-storage.md §6.9.
package llm

import "context"

// ============================================================
// Provider — concrete impls land in T1.4 and beyond.
// ============================================================

// Provider is the entry point every concrete LLM driver implements.
// The agent loop and tooling never branch on `Name()` or on the concrete
// type — they only call `Stream` and read `Capabilities`.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
}

// Capabilities advertises model-level static traits (looked up once at
// provider construction). Dynamic behavior (latency, rate limits) is not
// expressed here.
type Capabilities struct {
	Model             string
	ContextWindow     int
	MaxOutputTokens   int
	SupportsTools     bool
	SupportsStreaming bool
	SupportsThinking  bool
}

// ============================================================
// Request — what the agent sends to a Provider.
// ============================================================

// Request is the canonical, provider-agnostic request body. Each
// Provider's Codec (private to its sub-package) maps this onto the
// SDK's native shape.
type Request struct {
	Messages       []Message
	Tools          []ToolSpec
	ToolChoice     ToolChoice
	Temperature    *float32
	MaxTokens      *int
	Stop           []string
	EnableThinking bool   // R3
	ThinkingEffort string // R5: "" | "low" | "medium" | "high"
}

// Message is one turn in the canonical conversation. Note `Content` is
// always a list of ContentBlocks — never a flat string. Providers that
// only accept strings (e.g. OpenAI's plain-text path) flatten in their
// Codec.
type Message struct {
	Role    Role
	Content []ContentBlock
	Name    string
}

// Role is the canonical role enum. Note: `RoleTool` exists for
// completeness but the canonical layer prefers
// `RoleUser + ToolResultBlock` to avoid a "fake tool role" leaking out
// of the OpenAI codec.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ContentBlock is the multi-modal content unit. The `Type` field gates
// which other fields carry meaning — readers MUST switch on `Type`
// before touching anything else.
type ContentBlock struct {
	Type ContentBlockType

	// Type=Text
	Text string

	// Type=Thinking | RedactedThinking (R3)
	Thinking          string
	ThinkingSignature string // Anthropic signature; preserve verbatim

	// Type=ToolUse — assistant requests a tool invocation
	ToolUseID string
	ToolName  string
	ToolInput map[string]any // already parsed; not a JSON string

	// Type=ToolResult — links back to ToolUseID via ToolUseRefID
	ToolUseRefID string
	Output       string
	IsError      bool
}

// ContentBlockType is the discriminator for ContentBlock.
type ContentBlockType string

const (
	BlockText             ContentBlockType = "text"
	BlockThinking         ContentBlockType = "thinking"
	BlockRedactedThinking ContentBlockType = "redacted_thinking"
	BlockToolUse          ContentBlockType = "tool_use"
	BlockToolResult       ContentBlockType = "tool_result"
)

// ============================================================
// Tools — agent supplies these per request.
// ============================================================

// ToolSpec is the LLM-facing description of a tool. Schema is the JSON
// Schema dictionary already shaped to the provider's expected layout.
type ToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
}

// ToolChoice tells the provider how aggressively to call tools.
type ToolChoice struct {
	Mode ToolChoiceMode
	Name string // populated only when Mode == ToolChoiceSpecific
}

// ToolChoiceMode is the typed enum for ToolChoice.Mode.
type ToolChoiceMode int

const (
	ToolChoiceAuto ToolChoiceMode = iota
	ToolChoiceNone
	ToolChoiceRequired
	ToolChoiceSpecific
)

// ============================================================
// Streaming — Provider.Stream emits these.
// ============================================================

// StreamEvent is the one tagged-union event type all Provider streams
// emit. Consumers MUST switch on `Type`; the other fields are nil/zero
// outside the relevant variant.
type StreamEvent struct {
	Type     StreamEventType
	Delta    Delta
	Final    *FinalResponse
	Boundary *BlockBoundary // R5: only set when Type == StreamBlockBoundary
	Err      error
}

// StreamEventType discriminates StreamEvent.
type StreamEventType int

const (
	StreamDelta StreamEventType = iota
	StreamFinal
	StreamError
	StreamBlockBoundary // R5
)

// Delta carries the per-chunk additions during streaming.
type Delta struct {
	Content       string         // normal answer text
	Thinking      string         // reasoning text (R3)
	ToolCallDelta *ToolCallDelta // partial tool call json
}

// ToolCallDelta is the streaming form of a (partial) tool call.
type ToolCallDelta struct {
	Index    int
	ID       string
	Name     string
	ArgsDiff string // JSON fragment — caller concatenates into a buffer
}

// BlockBoundary (R5) signals the start/end of a content block during
// streaming so UIs can correctly open/close collapse panels for
// thinking/tool_use blocks.
type BlockBoundary struct {
	BlockType ContentBlockType
	IsStart   bool
	Index     int
}

// FinalResponse is emitted exactly once per successful stream as the
// terminal StreamFinal event.
type FinalResponse struct {
	Message    Message
	Usage      Usage
	StopReason StopReason
}

// Usage is the token + cost accounting for one LLM turn. Providers MUST
// extract token counts from the API response — string-counting is
// explicitly forbidden by the requirements doc.
type Usage struct {
	PromptTokens        int
	CompletionTokens    int     // OpenAI counts include reasoning_tokens
	ReasoningTokens     int     // R3
	CachedPromptTokens  int     // R5 — OpenAI cached_input / Gemini cachedContent
	CacheCreationTokens int     // R5 — Anthropic cache_creation_input_tokens
	CacheReadTokens     int     // R5 — Anthropic cache_read_input_tokens
	TotalTokens         int
	CostUSD             float64
}

// StopReason is the canonical termination reason. Codecs map their
// provider-specific values onto these; unknown values fall back to
// StopReasonEnd.
type StopReason string

const (
	StopReasonEnd           StopReason = "end_turn"
	StopReasonToolCall      StopReason = "tool_calls"
	StopReasonMaxTokens     StopReason = "max_tokens"
	StopReasonStop          StopReason = "stop_sequence"
	StopReasonContentFilter StopReason = "content_filter"
)
