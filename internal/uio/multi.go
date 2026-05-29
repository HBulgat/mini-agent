package uio

import (
	"github.com/HBulgat/mini-agent/internal/trace"
)

// MultiSink fans every Emit call out to a fixed list of Sinks. The
// canonical use case is "print to terminal AND record to JSON Lines":
// the bootstrap composer wraps `[ReplUIO, logsSink]` in a MultiSink and
// hands that to the agent. Order in the slice is preserved; failures
// in any one delegate cannot affect the others (Sink methods don't
// return errors by contract).
//
// MultiSink is value-type cheap (just a slice header). We keep methods
// on a non-pointer receiver because the Sink interface contract
// expects no mutation and we want callers to be able to hold the value
// directly without an extra allocation.
type MultiSink struct {
	delegates []Sink
}

// NewMultiSink builds a MultiSink. nil entries are filtered out so
// callers can construct conditionally:
//
//	uio.NewMultiSink(replSink, maybeLogsSink) // maybeLogsSink may be nil
//
// We deliberately accept Sink (not Sink interface pointers) so a value
// receiver Sink (like NopSink{}) can be passed directly.
func NewMultiSink(sinks ...Sink) *MultiSink {
	clean := make([]Sink, 0, len(sinks))
	for _, s := range sinks {
		if s == nil {
			continue
		}
		clean = append(clean, s)
	}
	return &MultiSink{delegates: clean}
}

// Len returns the number of active delegates. Useful for tests and for
// callers that want to short-circuit when no real Sink is wired.
func (m *MultiSink) Len() int {
	if m == nil {
		return 0
	}
	return len(m.delegates)
}

// EmitToken / EmitThinkingToken / etc. forward to every delegate in
// order. We intentionally don't recover from delegate panics — a
// panicking Sink is a programming bug and should surface immediately,
// not get swallowed across other delegates.

func (m *MultiSink) EmitToken(text string) {
	if m == nil {
		return
	}
	for _, d := range m.delegates {
		d.EmitToken(text)
	}
}

func (m *MultiSink) EmitThinkingToken(text string) {
	if m == nil {
		return
	}
	for _, d := range m.delegates {
		d.EmitThinkingToken(text)
	}
}

func (m *MultiSink) EmitToolCallStart(ev ToolCallStartEvent) {
	if m == nil {
		return
	}
	for _, d := range m.delegates {
		d.EmitToolCallStart(ev)
	}
}

func (m *MultiSink) EmitToolCallEnd(ev ToolCallEndEvent) {
	if m == nil {
		return
	}
	for _, d := range m.delegates {
		d.EmitToolCallEnd(ev)
	}
}

func (m *MultiSink) EmitMessage(role Role, content string) {
	if m == nil {
		return
	}
	for _, d := range m.delegates {
		d.EmitMessage(role, content)
	}
}

func (m *MultiSink) EmitTrace(e trace.Event) {
	if m == nil {
		return
	}
	for _, d := range m.delegates {
		d.EmitTrace(e)
	}
}

func (m *MultiSink) EmitInfo(msg string) {
	if m == nil {
		return
	}
	for _, d := range m.delegates {
		d.EmitInfo(msg)
	}
}

func (m *MultiSink) EmitError(err error) {
	if m == nil {
		return
	}
	for _, d := range m.delegates {
		d.EmitError(err)
	}
}

// Compile-time assertion.
var _ Sink = (*MultiSink)(nil)
