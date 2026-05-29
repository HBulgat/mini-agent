// Package session is the persistence-side counterpart of internal/llm.
//
// It owns the domain model (Session, Message, Todo, Usage, Visibility,
// UserVisibility) and the Repository interface. The actual SQLite
// implementation lives under internal/session/store/ — this package is
// kept import-free of database packages so callers (agent, compaction,
// view) can depend on it without dragging in a sql/sqlite transitive.
//
// Reference: docs/system-design/05-core-abstractions.md §5.8
//            docs/system-design/06-session-storage.md (R3 + R5).
package session

import (
	"context"
	"errors"
	"time"

	"github.com/HBulgat/mini-agent/internal/llm"
)

// ErrNotFound is returned by Repository getters when the requested row
// is absent. Wrapping with errors.Is keeps the sentinel comparable
// across packages.
var ErrNotFound = errors.New("session: not found")

// ============================================================
// Session
// ============================================================

// Session is the top-level conversation container. UsageTotal is a
// view-only field — it is filled in by GetSession via an aggregate query
// on usage_log and is NOT persisted on the sessions row itself (D13).
type Session struct {
	ID         string
	Title      string
	Cwd        string
	Model      string
	Status     SessionStatus
	CreatedAt  time.Time
	UpdatedAt  time.Time
	UsageTotal Usage
}

// SessionStatus is a string-backed enum so YAML / JSON dumps round-trip
// cleanly; we never compare numerically.
type SessionStatus string

const (
	SessionActive    SessionStatus = "active"
	SessionEnded     SessionStatus = "ended"
	SessionAbandoned SessionStatus = "abandoned"
)

// ============================================================
// Message — the persistence-layer counterpart of llm.Message
// ============================================================

// Message is what we actually store. It carries the canonical content
// blocks plus the two visibility axes and the source provider tag.
//
// Why two visibility axes (D24):
//   - Visibility    : controls whether the row is fed into the next LLM
//                     turn (live/summary go in; archived stays out).
//   - UserVisibility: controls whether the row is rendered in the CLI /
//                     Web UI by default (visible/hidden/system).
//
// They are independent: a "system" prompt is UserSystem + VisibilityLive;
// a compacted-away tool result is UserVisible + VisibilityArchived.
type Message struct {
	ID        string
	SessionID string
	SeqNo     int

	Role   Role
	Blocks []llm.ContentBlock

	Tokens         int
	SourceProvider string         // "openai" | "anthropic" | "gemini" | "" (user)
	Visibility     Visibility     // LLM visibility (D24)
	UserVisibility UserVisibility // UI visibility (D24)
	OriginalIDs    []string       // populated only when Visibility == Summary
	CreatedAt      time.Time
}

// Role mirrors llm.Role 1:1 — kept as its own type so storage code
// doesn't accidentally couple to the llm package's enum value layout.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Visibility — LLM-side visibility (D24). live/summary feed the next
// LLM turn; archived does not. Compaction is the *only* legal path that
// flips live → archived (enforced by the store layer).
type Visibility string

const (
	VisibilityLive     Visibility = "live"
	VisibilityArchived Visibility = "archived"
	VisibilitySummary  Visibility = "summary"
)

// UserVisibility — UI-side visibility (D24). The store treats these as
// opaque tags; the CLI / Web UI honor them in their default render.
type UserVisibility string

const (
	UserVisible UserVisibility = "visible"
	UserHidden  UserVisibility = "hidden"
	UserSystem  UserVisibility = "system"
)

// ToLLM strips storage-only metadata and returns the canonical
// llm.Message shape used by Provider.Stream.
//
// We use a *Message receiver per D31 — the Message struct is mid-sized
// (six pointer-width fields plus a slice header) and will be called
// once per turn, so avoiding the copy is worth the receiver style.
func (m *Message) ToLLM() llm.Message {
	if m == nil {
		return llm.Message{}
	}
	// Copy the slice so a caller mutating the result can't reach back
	// and corrupt our persisted blocks. Cheap — the elements are
	// already value types.
	blocks := make([]llm.ContentBlock, len(m.Blocks))
	copy(blocks, m.Blocks)
	return llm.Message{
		Role:    llm.Role(m.Role),
		Content: blocks,
	}
}

// FromLLMOption is the variadic options pattern used by FromLLM —
// callers chain WithUserVisibility / WithSourceProvider as needed.
type FromLLMOption func(*Message)

// WithUserVisibility overrides the default UserVisibility (UserVisible).
func WithUserVisibility(v UserVisibility) FromLLMOption {
	return func(m *Message) { m.UserVisibility = v }
}

// WithSourceProvider stamps the originating Provider's Name() onto the
// stored message. Empty string ("") is the convention for human input.
func WithSourceProvider(p string) FromLLMOption {
	return func(m *Message) { m.SourceProvider = p }
}

// FromLLM packages an llm.Message for storage. The caller still needs
// to feed the result through Repository.AppendMessage — that layer
// fills in ID / SeqNo / CreatedAt if zero (D17 keep-it-simple).
func FromLLM(sessionID string, seqNo int, m llm.Message, opts ...FromLLMOption) Message {
	blocks := make([]llm.ContentBlock, len(m.Content))
	copy(blocks, m.Content)
	out := Message{
		SessionID:      sessionID,
		SeqNo:          seqNo,
		Role:           Role(m.Role),
		Blocks:         blocks,
		Visibility:     VisibilityLive,
		UserVisibility: UserVisible,
	}
	for _, opt := range opts {
		opt(&out)
	}
	return out
}

// ============================================================
// Todo
// ============================================================

// Todo mirrors a single row of write_plan output. Todos are managed
// "all-or-nothing" — Repository.ReplaceTodos overwrites the full list
// in a single transaction (D17).
type Todo struct {
	ID        string
	SessionID string
	Order     int
	Content   string
	Status    TodoStatus
	Owner     TodoOwner
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TodoStatus is the canonical status enum surfaced to both write_plan
// and the UI todo panel.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
	TodoCancelled  TodoStatus = "cancelled"
)

// TodoOwner — main vs sub-agent. Sub-agents push their own todos so
// the main session can track delegated work without owning it.
type TodoOwner string

const (
	TodoOwnerMain TodoOwner = "main"
	TodoOwnerSub  TodoOwner = "sub"
)

// ============================================================
// Usage
// ============================================================

// Usage is the per-session aggregate (D13). Field meanings track the
// llm.Usage struct exactly so codec / repository conversions stay
// trivial.
type Usage struct {
	PromptTokens        int
	CompletionTokens    int
	ReasoningTokens     int
	CachedPromptTokens  int
	CacheCreationTokens int
	CacheReadTokens     int
	TotalTokens         int
	CostUSD             float64
	Requests            int
}

// ============================================================
// Repository — the only persistence contract callers should depend on
// ============================================================

// Repository is the storage-agnostic interface every persistence
// implementation honors. The SQLite implementation lives at
// internal/session/store; tests can swap in an in-memory fake by
// satisfying this interface.
//
// Cancellation (D63 from R6): every method takes a context.Context and
// MUST return ctx.Err() promptly when the context is canceled.
type Repository interface {
	// ----- Session -----
	CreateSession(ctx context.Context, s Session) (Session, error)
	GetSession(ctx context.Context, id string) (Session, error) // includes UsageTotal aggregate
	ListSessions(ctx context.Context, limit, offset int) ([]Session, error)
	UpdateSession(ctx context.Context, s Session) error
	DeleteSession(ctx context.Context, id string) error

	// ----- Message -----
	AppendMessage(ctx context.Context, m Message) (Message, error)

	// ListLiveMessages — feeds the next LLM turn. Returns rows where
	// Visibility ∈ {live, summary}. UserVisibility is ignored.
	ListLiveMessages(ctx context.Context, sessionID string) ([]Message, error)

	// ListVisibleMessages — default UI render. Returns rows where
	// UserVisibility == visible AND Visibility != archived.
	ListVisibleMessages(ctx context.Context, sessionID string) ([]Message, error)

	// ListAllMessages — debug / archive viewer. No filtering.
	ListAllMessages(ctx context.Context, sessionID string) ([]Message, error)

	// ApplyCompaction atomically:
	//   1) marks the listed message ids as archived;
	//   2) inserts the supplied summary messages (Visibility forced to
	//      VisibilitySummary).
	// Messages are NEVER physically deleted (D20).
	ApplyCompaction(ctx context.Context, sessionID string, archiveIDs []string, summaries []Message) error

	// ----- Todo -----
	ListTodos(ctx context.Context, sessionID string) ([]Todo, error)
	ReplaceTodos(ctx context.Context, sessionID string, todos []Todo) error // transactional, full overwrite

	// ----- Usage -----
	AddUsage(ctx context.Context, sessionID, messageID, model string, delta Usage) error
	SessionUsage(ctx context.Context, sessionID string) (Usage, error)
	GlobalUsage(ctx context.Context) (Usage, error)
}
