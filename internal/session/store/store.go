package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/HBulgat/mini-agent/internal/session"
	"github.com/HBulgat/mini-agent/internal/session/store/gen"
)

// Compile-time assertion: *Store implements session.Repository. Any
// drift between the interface and the methods below trips a build error.
var _ session.Repository = (*Store)(nil)

// Store is the SQLite-backed Repository. It owns the *sql.DB so it can
// open transactions for ApplyCompaction / ReplaceTodos (D17). The sqlc
// queries are accessed via a long-lived *gen.Queries; per-transaction
// queries are built on the fly via q.WithTx(tx).
type Store struct {
	db *sql.DB
	q  *gen.Queries
}

// New wraps an already-open *sql.DB. The caller is responsible for the
// PRAGMA / migrate work — use OpenAndMigrate for the typical path.
func New(db *sql.DB) *Store {
	return &Store{db: db, q: gen.New(db)}
}

// DB exposes the underlying *sql.DB so tests can probe state directly
// (e.g. count rows in `messages`). Production code should never use it.
func (s *Store) DB() *sql.DB { return s.db }

// ============================================================
// Sessions
// ============================================================

func (s *Store) CreateSession(ctx context.Context, in session.Session) (session.Session, error) {
	if in.ID == "" {
		v7, err := uuid.NewV7()
		if err != nil {
			return session.Session{}, fmt.Errorf("store: uuidv7: %w", err)
		}
		in.ID = v7.String()
	}
	now := time.Now()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = in.CreatedAt
	}
	if in.Status == "" {
		in.Status = session.SessionActive
	}

	row, err := s.q.CreateSession(ctx, gen.CreateSessionParams{
		ID:        in.ID,
		Title:     in.Title,
		Cwd:       in.Cwd,
		Model:     in.Model,
		Status:    string(in.Status),
		CreatedAt: in.CreatedAt.UnixMilli(),
		UpdatedAt: in.UpdatedAt.UnixMilli(),
	})
	if err != nil {
		return session.Session{}, fmt.Errorf("store: CreateSession: %w", err)
	}
	return rowToSession(row), nil
}

func (s *Store) GetSession(ctx context.Context, id string) (session.Session, error) {
	row, err := s.q.GetSession(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.Session{}, session.ErrNotFound
		}
		return session.Session{}, fmt.Errorf("store: GetSession: %w", err)
	}
	out := rowToSession(row)

	// UsageTotal is a view-only field — populate via aggregate (D13).
	// A failure here is non-fatal; we surface a zero usage rather than
	// fail the whole GetSession call.
	if usage, err := s.SessionUsage(ctx, id); err == nil {
		out.UsageTotal = usage
	}
	return out, nil
}

func (s *Store) ListSessions(ctx context.Context, limit, offset int) ([]session.Session, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.q.ListSessions(ctx, gen.ListSessionsParams{
		Limit:  int64(limit),
		Offset: int64(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("store: ListSessions: %w", err)
	}
	out := make([]session.Session, len(rows))
	for i, r := range rows {
		out[i] = rowToSession(r)
	}
	return out, nil
}

func (s *Store) UpdateSession(ctx context.Context, in session.Session) error {
	in.UpdatedAt = time.Now()
	if err := s.q.UpdateSession(ctx, gen.UpdateSessionParams{
		Title:     in.Title,
		Status:    string(in.Status),
		UpdatedAt: in.UpdatedAt.UnixMilli(),
		ID:        in.ID,
	}); err != nil {
		return fmt.Errorf("store: UpdateSession: %w", err)
	}
	return nil
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	if err := s.q.DeleteSession(ctx, id); err != nil {
		return fmt.Errorf("store: DeleteSession: %w", err)
	}
	return nil
}

// ============================================================
// Messages
// ============================================================

func (s *Store) AppendMessage(ctx context.Context, m session.Message) (session.Message, error) {
	return appendMessageWith(ctx, s.q, m)
}

// appendMessageWith implements AppendMessage against either the base
// queries or a transactional `WithTx` set. Lets ApplyCompaction reuse
// the same defaulting + encoding logic inside its tx.
func appendMessageWith(ctx context.Context, q *gen.Queries, m session.Message) (session.Message, error) {
	if m.SessionID == "" {
		return session.Message{}, errors.New("store: AppendMessage: empty SessionID")
	}
	if m.ID == "" {
		v7, err := uuid.NewV7()
		if err != nil {
			return session.Message{}, fmt.Errorf("store: uuidv7: %w", err)
		}
		m.ID = v7.String()
	}
	if m.Visibility == "" {
		m.Visibility = session.VisibilityLive
	}
	if m.UserVisibility == "" {
		m.UserVisibility = session.UserVisible
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	if m.SeqNo == 0 {
		next, err := q.NextSeqNo(ctx, m.SessionID)
		if err != nil {
			return session.Message{}, fmt.Errorf("store: NextSeqNo: %w", err)
		}
		// next_seq comes back as `interface{}` because COALESCE returns
		// a value of fluctuating affinity; modernc/sqlite resolves it
		// to int64 in practice.
		m.SeqNo = int(asInt64(next))
	}

	blocksJSON, err := encodeBlocks(m.Blocks)
	if err != nil {
		return session.Message{}, err
	}
	idsJSON, err := encodeIDs(m.OriginalIDs)
	if err != nil {
		return session.Message{}, err
	}

	row, err := q.AppendMessage(ctx, gen.AppendMessageParams{
		ID:              m.ID,
		SessionID:       m.SessionID,
		SeqNo:           int64(m.SeqNo),
		Role:            string(m.Role),
		BlocksJson:      blocksJSON,
		Tokens:          int64(m.Tokens),
		SourceProvider:  m.SourceProvider,
		Visibility:      string(m.Visibility),
		UserVisibility:  string(m.UserVisibility),
		OriginalIdsJson: idsJSON,
		CreatedAt:       m.CreatedAt.UnixMilli(),
	})
	if err != nil {
		return session.Message{}, fmt.Errorf("store: AppendMessage: %w", err)
	}
	out, err := rowToMessage(row)
	if err != nil {
		return session.Message{}, err
	}
	return out, nil
}

func (s *Store) ListLiveMessages(ctx context.Context, sessionID string) ([]session.Message, error) {
	rows, err := s.q.ListLiveMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: ListLiveMessages: %w", err)
	}
	return rowsToMessages(rows)
}

func (s *Store) ListVisibleMessages(ctx context.Context, sessionID string) ([]session.Message, error) {
	rows, err := s.q.ListVisibleMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: ListVisibleMessages: %w", err)
	}
	return rowsToMessages(rows)
}

func (s *Store) ListAllMessages(ctx context.Context, sessionID string) ([]session.Message, error) {
	rows, err := s.q.ListAllMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: ListAllMessages: %w", err)
	}
	return rowsToMessages(rows)
}

// ApplyCompaction is transactional (D17): the archive flips and
// summary inserts must be all-or-nothing. We use the same primitive set
// the agent uses outside transactions — sqlc.Queries.WithTx keeps the
// calls type-safe.
func (s *Store) ApplyCompaction(
	ctx context.Context,
	sessionID string,
	archiveIDs []string,
	summaries []session.Message,
) error {
	if sessionID == "" {
		return errors.New("store: ApplyCompaction: empty SessionID")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: BeginTx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // safe to call after Commit

	qtx := s.q.WithTx(tx)

	for _, id := range archiveIDs {
		if id == "" {
			continue
		}
		if err := qtx.MarkMessageArchived(ctx, id); err != nil {
			return fmt.Errorf("store: MarkMessageArchived(%s): %w", id, err)
		}
	}

	for _, m := range summaries {
		m.SessionID = sessionID
		m.Visibility = session.VisibilitySummary // D20: forced
		if _, err := appendMessageWith(ctx, qtx, m); err != nil {
			return fmt.Errorf("store: append summary: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: Commit: %w", err)
	}
	return nil
}

// ============================================================
// Todos
// ============================================================

func (s *Store) ListTodos(ctx context.Context, sessionID string) ([]session.Todo, error) {
	rows, err := s.q.ListTodos(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: ListTodos: %w", err)
	}
	out := make([]session.Todo, len(rows))
	for i, r := range rows {
		out[i] = rowToTodo(r)
	}
	return out, nil
}

// ReplaceTodos is the transactional counterpart of write_plan's
// "all-or-nothing" semantics (D17). We delete every existing row for
// the session and re-insert the supplied list in order.
func (s *Store) ReplaceTodos(ctx context.Context, sessionID string, todos []session.Todo) error {
	if sessionID == "" {
		return errors.New("store: ReplaceTodos: empty SessionID")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: BeginTx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := s.q.WithTx(tx)

	if err := qtx.DeleteTodosBySession(ctx, sessionID); err != nil {
		return fmt.Errorf("store: DeleteTodosBySession: %w", err)
	}

	now := time.Now()
	for i, t := range todos {
		if t.ID == "" {
			v7, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("store: uuidv7: %w", err)
			}
			t.ID = v7.String()
		}
		if t.Status == "" {
			t.Status = session.TodoPending
		}
		if t.Owner == "" {
			t.Owner = session.TodoOwnerMain
		}
		if t.CreatedAt.IsZero() {
			t.CreatedAt = now
		}
		t.UpdatedAt = now
		// Force order to the slice index so callers don't need to
		// pre-number their todos. ReplaceTodos owns ordering.
		t.Order = i + 1

		if err := qtx.InsertTodo(ctx, gen.InsertTodoParams{
			ID:        t.ID,
			SessionID: sessionID,
			OrderNo:   int64(t.Order),
			Content:   t.Content,
			Status:    string(t.Status),
			Owner:     string(t.Owner),
			CreatedAt: t.CreatedAt.UnixMilli(),
			UpdatedAt: t.UpdatedAt.UnixMilli(),
		}); err != nil {
			return fmt.Errorf("store: InsertTodo[%d]: %w", i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: Commit: %w", err)
	}
	return nil
}

// ============================================================
// Usage
// ============================================================

func (s *Store) AddUsage(ctx context.Context, sessionID, messageID, model string, delta session.Usage) error {
	if sessionID == "" {
		return errors.New("store: AddUsage: empty SessionID")
	}
	v7, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("store: uuidv7: %w", err)
	}
	var msgID *string
	if messageID != "" {
		// Copy to a stack variable so we never share the caller's
		// string header with the DB driver across goroutines.
		mid := messageID
		msgID = &mid
	}
	return s.q.AddUsage(ctx, gen.AddUsageParams{
		ID:                  v7.String(),
		SessionID:           sessionID,
		MessageID:           msgID,
		Model:               model,
		PromptTokens:        int64(delta.PromptTokens),
		CompletionTokens:    int64(delta.CompletionTokens),
		ReasoningTokens:     int64(delta.ReasoningTokens),
		CachedPromptTokens:  int64(delta.CachedPromptTokens),
		CacheCreationTokens: int64(delta.CacheCreationTokens),
		CacheReadTokens:     int64(delta.CacheReadTokens),
		TotalTokens:         int64(delta.TotalTokens),
		CostUsd:             delta.CostUSD,
		CreatedAt:           time.Now().UnixMilli(),
	})
}

func (s *Store) SessionUsage(ctx context.Context, sessionID string) (session.Usage, error) {
	row, err := s.q.SessionUsage(ctx, sessionID)
	if err != nil {
		return session.Usage{}, fmt.Errorf("store: SessionUsage: %w", err)
	}
	return session.Usage{
		PromptTokens:        int(asInt64(row.PromptTokens)),
		CompletionTokens:    int(asInt64(row.CompletionTokens)),
		ReasoningTokens:     int(asInt64(row.ReasoningTokens)),
		CachedPromptTokens:  int(asInt64(row.CachedPromptTokens)),
		CacheCreationTokens: int(asInt64(row.CacheCreationTokens)),
		CacheReadTokens:     int(asInt64(row.CacheReadTokens)),
		TotalTokens:         int(asInt64(row.TotalTokens)),
		CostUSD:             asFloat64(row.CostUsd),
		Requests:            int(row.Requests),
	}, nil
}

func (s *Store) GlobalUsage(ctx context.Context) (session.Usage, error) {
	row, err := s.q.GlobalUsage(ctx)
	if err != nil {
		return session.Usage{}, fmt.Errorf("store: GlobalUsage: %w", err)
	}
	return session.Usage{
		PromptTokens:        int(asInt64(row.PromptTokens)),
		CompletionTokens:    int(asInt64(row.CompletionTokens)),
		ReasoningTokens:     int(asInt64(row.ReasoningTokens)),
		CachedPromptTokens:  int(asInt64(row.CachedPromptTokens)),
		CacheCreationTokens: int(asInt64(row.CacheCreationTokens)),
		CacheReadTokens:     int(asInt64(row.CacheReadTokens)),
		TotalTokens:         int(asInt64(row.TotalTokens)),
		CostUSD:             asFloat64(row.CostUsd),
		Requests:            int(row.Requests),
	}, nil
}

// ============================================================
// Row → domain conversions
// ============================================================

func rowToSession(r gen.Session) session.Session {
	return session.Session{
		ID:        r.ID,
		Title:     r.Title,
		Cwd:       r.Cwd,
		Model:     r.Model,
		Status:    session.SessionStatus(r.Status),
		CreatedAt: time.UnixMilli(r.CreatedAt),
		UpdatedAt: time.UnixMilli(r.UpdatedAt),
	}
}

func rowToMessage(r gen.Message) (session.Message, error) {
	blocks, err := decodeBlocks(r.BlocksJson)
	if err != nil {
		return session.Message{}, err
	}
	ids, err := decodeIDs(r.OriginalIdsJson)
	if err != nil {
		return session.Message{}, err
	}
	return session.Message{
		ID:             r.ID,
		SessionID:      r.SessionID,
		SeqNo:          int(r.SeqNo),
		Role:           session.Role(r.Role),
		Blocks:         blocks,
		Tokens:         int(r.Tokens),
		SourceProvider: r.SourceProvider,
		Visibility:     session.Visibility(r.Visibility),
		UserVisibility: session.UserVisibility(r.UserVisibility),
		OriginalIDs:    ids,
		CreatedAt:      time.UnixMilli(r.CreatedAt),
	}, nil
}

func rowsToMessages(rows []gen.Message) ([]session.Message, error) {
	out := make([]session.Message, len(rows))
	for i, r := range rows {
		m, err := rowToMessage(r)
		if err != nil {
			return nil, fmt.Errorf("store: row[%d]: %w", i, err)
		}
		out[i] = m
	}
	return out, nil
}

func rowToTodo(r gen.Todo) session.Todo {
	return session.Todo{
		ID:        r.ID,
		SessionID: r.SessionID,
		Order:     int(r.OrderNo),
		Content:   r.Content,
		Status:    session.TodoStatus(r.Status),
		Owner:     session.TodoOwner(r.Owner),
		CreatedAt: time.UnixMilli(r.CreatedAt),
		UpdatedAt: time.UnixMilli(r.UpdatedAt),
	}
}
