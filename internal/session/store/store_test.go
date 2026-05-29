package store_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HBulgat/mini-agent/internal/llm"
	"github.com/HBulgat/mini-agent/internal/session"
	"github.com/HBulgat/mini-agent/internal/session/store"
)

// newRepo opens a brand-new SQLite database under t.TempDir() and
// applies all migrations. The returned *Store is fully wired and the
// underlying file is auto-cleaned when the test ends.
func newRepo(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := store.OpenAndMigrate(path)
	if err != nil {
		t.Fatalf("OpenAndMigrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store.New(db)
}

// ---------- Migrations ----------

// TestMigrate_Idempotent verifies that calling Migrate twice in a row
// is a no-op. Without that guarantee the auto-migrate-on-startup story
// from D15 would spam the user with errors on every restart.
func TestMigrate_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idem.db")
	db, err := store.OpenAndMigrate(path)
	if err != nil {
		t.Fatalf("first OpenAndMigrate: %v", err)
	}
	defer db.Close()

	pre, err := store.Migrate(db)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if pre == 0 {
		t.Errorf("second Migrate preVersion=0, want >0 (means migrations weren't applied first time)")
	}
}

// TestMigrate_CacheColumnsExist asserts the R5 addendum (0002) actually
// added the three cache columns. We probe with a write that mentions
// every column; if any are missing we'd get a SQL error.
func TestMigrate_CacheColumnsExist(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	sess, err := repo.CreateSession(ctx, session.Session{Title: "cache-probe"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := repo.AddUsage(ctx, sess.ID, "", "test-model", session.Usage{
		PromptTokens:        10,
		CompletionTokens:    20,
		ReasoningTokens:     5,
		CachedPromptTokens:  3,
		CacheCreationTokens: 7,
		CacheReadTokens:     1,
		TotalTokens:         30,
		CostUSD:             0.5,
	}); err != nil {
		t.Fatalf("AddUsage with cache columns: %v", err)
	}

	got, err := repo.SessionUsage(ctx, sess.ID)
	if err != nil {
		t.Fatalf("SessionUsage: %v", err)
	}
	if got.CachedPromptTokens != 3 || got.CacheCreationTokens != 7 || got.CacheReadTokens != 1 {
		t.Errorf("cache totals roundtrip: got %+v", got)
	}
}

// ---------- Sessions ----------

func TestCreateAndGetSession(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	in := session.Session{Title: "hello", Cwd: "/tmp", Model: "deepseek:r1"}
	out, err := repo.CreateSession(ctx, in)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if out.ID == "" {
		t.Fatal("CreateSession should auto-fill UUIDv7")
	}
	if out.Status != session.SessionActive {
		t.Errorf("status default: got %q, want active", out.Status)
	}

	got, err := repo.GetSession(ctx, out.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Title != "hello" || got.Cwd != "/tmp" || got.Model != "deepseek:r1" {
		t.Errorf("GetSession round-trip mismatch: %+v", got)
	}
}

func TestGetSession_NotFound(t *testing.T) {
	repo := newRepo(t)
	_, err := repo.GetSession(context.Background(), "no-such-id")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("GetSession(missing): want ErrNotFound, got %v", err)
	}
}

// ---------- Messages ----------

func TestAppendAndListMessages(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	sess, _ := repo.CreateSession(ctx, session.Session{Title: "msgs"})

	// Append three messages with different visibility / user_visibility
	// combinations to exercise all three List* filters.
	specs := []struct {
		role       session.Role
		text       string
		vis        session.Visibility
		userVis    session.UserVisibility
		shouldLive bool // expected in ListLiveMessages
		shouldVis  bool // expected in ListVisibleMessages
	}{
		{session.RoleUser, "hi", session.VisibilityLive, session.UserVisible, true, true},
		{session.RoleAssistant, "hello", session.VisibilityLive, session.UserHidden, true, false},
		{session.RoleAssistant, "archived", session.VisibilityArchived, session.UserVisible, false, false},
	}
	for _, sp := range specs {
		_, err := repo.AppendMessage(ctx, session.Message{
			SessionID:      sess.ID,
			Role:           sp.role,
			Blocks:         []llm.ContentBlock{{Type: llm.BlockText, Text: sp.text}},
			Visibility:     sp.vis,
			UserVisibility: sp.userVis,
		})
		if err != nil {
			t.Fatalf("AppendMessage(%q): %v", sp.text, err)
		}
	}

	live, err := repo.ListLiveMessages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("ListLiveMessages: %v", err)
	}
	if got, want := len(live), 2; got != want {
		t.Errorf("live count: got %d, want %d (live ∈ {live, summary})", got, want)
	}

	vis, err := repo.ListVisibleMessages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("ListVisibleMessages: %v", err)
	}
	if got, want := len(vis), 1; got != want {
		t.Errorf("visible count: got %d, want %d (visible AND not archived)", got, want)
	}

	all, err := repo.ListAllMessages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("ListAllMessages: %v", err)
	}
	if got, want := len(all), 3; got != want {
		t.Errorf("all count: got %d, want %d", got, want)
	}

	// Round-trip the canonical content blocks.
	if len(all[0].Blocks) != 1 || all[0].Blocks[0].Text != "hi" {
		t.Errorf("blocks roundtrip: got %+v", all[0].Blocks)
	}
}

func TestAppendMessage_AutoSeq(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	sess, _ := repo.CreateSession(ctx, session.Session{})

	for i := 1; i <= 3; i++ {
		m, err := repo.AppendMessage(ctx, session.Message{
			SessionID: sess.ID,
			Role:      session.RoleUser,
			Blocks:    []llm.ContentBlock{{Type: llm.BlockText, Text: "x"}},
		})
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		if m.SeqNo != i {
			t.Errorf("seq_no auto-fill: got %d, want %d", m.SeqNo, i)
		}
	}
}

// ---------- ApplyCompaction ----------

func TestApplyCompaction(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	sess, _ := repo.CreateSession(ctx, session.Session{})

	// Five live messages — we'll archive the first three.
	var ids []string
	for i := 0; i < 5; i++ {
		m, err := repo.AppendMessage(ctx, session.Message{
			SessionID: sess.ID,
			Role:      session.RoleUser,
			Blocks:    []llm.ContentBlock{{Type: llm.BlockText, Text: "msg"}},
		})
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		ids = append(ids, m.ID)
	}

	summary := session.Message{
		Role:        session.RoleSystem,
		Blocks:      []llm.ContentBlock{{Type: llm.BlockText, Text: "<<summary of 3 msgs>>"}},
		OriginalIDs: ids[:3],
	}
	if err := repo.ApplyCompaction(ctx, sess.ID, ids[:3], []session.Message{summary}); err != nil {
		t.Fatalf("ApplyCompaction: %v", err)
	}

	live, _ := repo.ListLiveMessages(ctx, sess.ID)
	// 2 untouched live + 1 summary == 3
	if got := len(live); got != 3 {
		t.Errorf("post-compaction live count: got %d, want 3", got)
	}

	// Verify the summary message has Visibility=summary and a non-empty
	// OriginalIDs list (D24 contract).
	var foundSummary bool
	for _, m := range live {
		if m.Visibility == session.VisibilitySummary {
			foundSummary = true
			if len(m.OriginalIDs) != 3 {
				t.Errorf("summary OriginalIDs: got %d, want 3", len(m.OriginalIDs))
			}
		}
	}
	if !foundSummary {
		t.Error("expected at least one VisibilitySummary message after compaction")
	}

	// Archived messages still exist (D20 — never physically deleted).
	all, _ := repo.ListAllMessages(ctx, sess.ID)
	if got := len(all); got != 6 {
		t.Errorf("post-compaction total count: got %d, want 6 (5 original + 1 summary)", got)
	}
}

// ---------- Todos ----------

func TestReplaceTodos_FullOverwrite(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	sess, _ := repo.CreateSession(ctx, session.Session{})

	// Round 1: write three todos.
	first := []session.Todo{
		{Content: "a", Status: session.TodoPending},
		{Content: "b", Status: session.TodoInProgress},
		{Content: "c", Status: session.TodoCompleted},
	}
	if err := repo.ReplaceTodos(ctx, sess.ID, first); err != nil {
		t.Fatalf("ReplaceTodos round 1: %v", err)
	}
	got, _ := repo.ListTodos(ctx, sess.ID)
	if len(got) != 3 {
		t.Fatalf("round 1 list: got %d, want 3", len(got))
	}
	for i, todo := range got {
		if todo.Order != i+1 {
			t.Errorf("order_no: got %d, want %d", todo.Order, i+1)
		}
	}

	// Round 2: overwrite with two todos. Old ones must be gone.
	second := []session.Todo{
		{Content: "x"},
		{Content: "y"},
	}
	if err := repo.ReplaceTodos(ctx, sess.ID, second); err != nil {
		t.Fatalf("ReplaceTodos round 2: %v", err)
	}
	got, _ = repo.ListTodos(ctx, sess.ID)
	if len(got) != 2 {
		t.Fatalf("round 2 list: got %d, want 2", len(got))
	}
	if got[0].Content != "x" || got[1].Content != "y" {
		t.Errorf("round 2 content: %+v", got)
	}
}

// ---------- Usage ----------

func TestUsageAggregates(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	a, _ := repo.CreateSession(ctx, session.Session{Title: "A"})
	b, _ := repo.CreateSession(ctx, session.Session{Title: "B"})

	// Two usage rows for A, one for B.
	mustAdd := func(sid string, prompt, completion int, cost float64) {
		t.Helper()
		if err := repo.AddUsage(ctx, sid, "", "m", session.Usage{
			PromptTokens:     prompt,
			CompletionTokens: completion,
			TotalTokens:      prompt + completion,
			CostUSD:          cost,
		}); err != nil {
			t.Fatalf("AddUsage: %v", err)
		}
	}
	mustAdd(a.ID, 100, 200, 0.10)
	mustAdd(a.ID, 50, 150, 0.05)
	mustAdd(b.ID, 30, 70, 0.02)

	// Per-session aggregate.
	gotA, err := repo.SessionUsage(ctx, a.ID)
	if err != nil {
		t.Fatalf("SessionUsage(a): %v", err)
	}
	if gotA.PromptTokens != 150 || gotA.TotalTokens != 500 || gotA.Requests != 2 {
		t.Errorf("session A usage: got %+v", gotA)
	}
	if gotA.CostUSD < 0.149 || gotA.CostUSD > 0.151 {
		t.Errorf("session A cost: got %v, want ~0.15", gotA.CostUSD)
	}

	// Global aggregate sums all three rows.
	global, err := repo.GlobalUsage(ctx)
	if err != nil {
		t.Fatalf("GlobalUsage: %v", err)
	}
	if global.Requests != 3 {
		t.Errorf("global requests: got %d, want 3", global.Requests)
	}
	if global.TotalTokens != 600 {
		t.Errorf("global total_tokens: got %d, want 600", global.TotalTokens)
	}

	// GetSession should fold UsageTotal in (D13).
	full, err := repo.GetSession(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetSession(a): %v", err)
	}
	if full.UsageTotal.PromptTokens != 150 {
		t.Errorf("GetSession.UsageTotal: got %+v", full.UsageTotal)
	}
}

// ---------- Roundtrip: thinking-block fields ----------

// TestThinkingBlockRoundtrip verifies the codec preserves the Anthropic
// `signature` field — the "must be returned verbatim or the next call
// gets rejected" guarantee from D21 / R3.
func TestThinkingBlockRoundtrip(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	sess, _ := repo.CreateSession(ctx, session.Session{})

	signed := []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: "let me think...", ThinkingSignature: "abc-sig-XYZ-123"},
		{Type: llm.BlockText, Text: "the answer is 42"},
		{Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "calc",
			ToolInput: map[string]any{"expr": "21*2"}},
	}
	if _, err := repo.AppendMessage(ctx, session.Message{
		SessionID: sess.ID,
		Role:      session.RoleAssistant,
		Blocks:    signed,
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	all, _ := repo.ListAllMessages(ctx, sess.ID)
	if len(all) != 1 {
		t.Fatalf("expected 1 message, got %d", len(all))
	}
	got := all[0].Blocks
	if len(got) != 3 {
		t.Fatalf("blocks count: got %d, want 3", len(got))
	}
	if got[0].ThinkingSignature != "abc-sig-XYZ-123" {
		t.Errorf("ThinkingSignature lost: got %q", got[0].ThinkingSignature)
	}
	if got[0].Thinking != "let me think..." {
		t.Errorf("Thinking text lost: got %q", got[0].Thinking)
	}
	if got[2].ToolName != "calc" {
		t.Errorf("ToolName lost: got %q", got[2].ToolName)
	}
	expr, _ := got[2].ToolInput["expr"].(string)
	if expr != "21*2" {
		t.Errorf("ToolInput.expr lost: got %v", got[2].ToolInput)
	}
}

// ---------- Domain converters (session.Message ↔ llm.Message) ----------

func TestToLLM_FromLLM(t *testing.T) {
	src := llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "ping"}},
	}
	stored := session.FromLLM("sess-1", 0, src,
		session.WithSourceProvider("anthropic"),
		session.WithUserVisibility(session.UserHidden))

	if stored.SessionID != "sess-1" || stored.Role != session.RoleUser {
		t.Errorf("FromLLM: %+v", stored)
	}
	if stored.SourceProvider != "anthropic" {
		t.Errorf("WithSourceProvider not applied: %q", stored.SourceProvider)
	}
	if stored.UserVisibility != session.UserHidden {
		t.Errorf("WithUserVisibility not applied: %q", stored.UserVisibility)
	}

	back := stored.ToLLM()
	if back.Role != llm.RoleUser || len(back.Content) != 1 || back.Content[0].Text != "ping" {
		t.Errorf("ToLLM lost data: %+v", back)
	}
}

// ---------- Sanity: ContextCancellation ----------

func TestContextCancel(t *testing.T) {
	repo := newRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before the call

	_, err := repo.CreateSession(ctx, session.Session{Title: "x"})
	if err == nil {
		t.Fatal("CreateSession with canceled ctx: want error")
	}
}

// ---------- Time ordering sanity ----------

func TestSessionUpdatedAt(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	sess, _ := repo.CreateSession(ctx, session.Session{Title: "old"})
	originalUpdated := sess.UpdatedAt

	// Force a measurable time delta. Using the explicit sleep keeps the
	// test deterministic across busy CI runners that might otherwise
	// finish the UpdateSession in <1 ms.
	time.Sleep(2 * time.Millisecond)

	sess.Title = "new"
	if err := repo.UpdateSession(ctx, sess); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}

	got, _ := repo.GetSession(ctx, sess.ID)
	if !got.UpdatedAt.After(originalUpdated) {
		t.Errorf("UpdatedAt did not advance: original=%s now=%s", originalUpdated, got.UpdatedAt)
	}
	if got.Title != "new" {
		t.Errorf("title: got %q, want new", got.Title)
	}
}
