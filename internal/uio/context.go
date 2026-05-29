// Per-request Prompter / Sink threading.
//
// Tools are constructed once (at bootstrap step 5) and registered in
// the global tool.Registry. But the *active* Prompter / Sink come
// from RunInput on each agent.Loop.Run call — they may differ
// between a CLI run and a concurrent Web run sharing the same
// process. To bridge these two scopes without forcing every tool to
// take Prompter as a constructor argument that's then ignored at
// runtime, we thread the per-request values through context.Context.
//
// Usage:
//
//	ctx = uio.WithPrompter(ctx, in.Prompter)
//	ctx = uio.WithSink(ctx, in.Sink)
//	res, err := tool.Invoke(ctx, input)   // tool reads ctx values
//
// Lookup helpers return (value, true) if the key is set and the
// value is non-nil; (nil, false) otherwise. Tools that need the
// Prompter must fail-closed with ErrInvalidArgs when no Prompter is
// available, never block on the Nop variant.

package uio

import "context"

type ctxKey int

const (
	ctxKeyPrompter ctxKey = iota
	ctxKeySink
)

// WithPrompter returns a child context that carries p. Passing nil
// is permitted and a subsequent PrompterFromContext will report
// (nil, false) — useful in tests that want to assert "no prompter".
func WithPrompter(parent context.Context, p Prompter) context.Context {
	return context.WithValue(parent, ctxKeyPrompter, p)
}

// PrompterFromContext returns the Prompter previously stashed via
// WithPrompter, if any.
func PrompterFromContext(ctx context.Context) (Prompter, bool) {
	v := ctx.Value(ctxKeyPrompter)
	if v == nil {
		return nil, false
	}
	p, ok := v.(Prompter)
	if !ok || p == nil {
		return nil, false
	}
	return p, true
}

// WithSink returns a child context that carries s. Mostly useful
// for tools that want to emit status updates without sitting on the
// Sink wire (current users: none, but the pattern is symmetric).
func WithSink(parent context.Context, s Sink) context.Context {
	return context.WithValue(parent, ctxKeySink, s)
}

// SinkFromContext mirrors PrompterFromContext for the Sink.
func SinkFromContext(ctx context.Context) (Sink, bool) {
	v := ctx.Value(ctxKeySink)
	if v == nil {
		return nil, false
	}
	s, ok := v.(Sink)
	if !ok || s == nil {
		return nil, false
	}
	return s, true
}
