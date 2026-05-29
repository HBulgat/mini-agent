package trace

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestWithTrace_RoundTrip verifies that WithTrace + FromContext are
// each other's exact inverse — a property the agent loop relies on
// when it derives sub-spans.
func TestWithTrace_RoundTrip(t *testing.T) {
	ctx := WithTrace(context.Background(), "trace-1", "span-A")
	tid, sid, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext: ok=false after WithTrace")
	}
	if tid != "trace-1" || sid != "span-A" {
		t.Errorf("FromContext: got (%q, %q), want (trace-1, span-A)", tid, sid)
	}
}

// TestFromContext_Empty verifies the "no trace context" sentinel —
// tools called from a fresh context must report ok=false rather than
// silently using empty strings as IDs.
func TestFromContext_Empty(t *testing.T) {
	_, _, ok := FromContext(context.Background())
	if ok {
		t.Errorf("FromContext on bare ctx: ok=true, want false")
	}
}

// TestNopRecorder verifies the no-op fallback satisfies the contract
// (no panic, no allocation, no return value).
func TestNopRecorder(t *testing.T) {
	var r Recorder = NopRecorder{}
	r.Record(context.Background(), Event{
		Type:    TypeAgentStep,
		Time:    time.Now(),
		Message: "anything",
	})
}

// TestEvent_StructuralSanity is a tiny shape check — guards against
// drift between the type list and what the Event struct actually
// stores. If a new Type constant is added without a corresponding
// Fields-key being documented in R12, this test still passes (we
// don't enforce that here), but it does catch field renames.
func TestEvent_StructuralSanity(t *testing.T) {
	e := Event{
		Type:         TypeLLMRequest,
		Level:        LevelInfo,
		TraceID:      "t",
		SpanID:       "s",
		ParentSpanID: "p",
		Message:      "hello",
		Fields:       map[string]any{"model": "gpt-4o"},
		Err:          errors.New("boom"),
	}
	if e.Type != TypeLLMRequest || e.Level != LevelInfo || e.Fields["model"] != "gpt-4o" {
		t.Errorf("event field round-trip failed: %+v", e)
	}
}

// TestLevelOrdering nails down the relative ordering of the level
// constants — anything that compares Levels (e.g. a future filtering
// recorder) should rely on debug < info < warn < error.
func TestLevelOrdering(t *testing.T) {
	if !(LevelDebug < LevelInfo && LevelInfo < LevelWarn && LevelWarn < LevelError) {
		t.Errorf("Level ordering broken: debug=%d info=%d warn=%d error=%d",
			LevelDebug, LevelInfo, LevelWarn, LevelError)
	}
}
