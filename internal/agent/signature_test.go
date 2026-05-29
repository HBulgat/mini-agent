// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package agent

import (
	"strings"
	"testing"
)

// TestSignature_StableAcrossKeyOrder confirms map key order does NOT
// affect the signature. We rely on this to count consecutive
// failures of the "same" call even when the LLM emits args with
// different field orders.
func TestSignature_StableAcrossKeyOrder(t *testing.T) {
	a := signature("read_file", map[string]any{"path": "go.mod", "limit": 100})
	b := signature("read_file", map[string]any{"limit": 100, "path": "go.mod"})
	if a != b {
		t.Errorf("signature must be order-independent: %s vs %s", a, b)
	}
}

// TestSignature_DifferentArgsDifferentSig: tweaking any arg flips
// the signature so a "different attempt" gets a fresh counter.
func TestSignature_DifferentArgsDifferentSig(t *testing.T) {
	a := signature("read_file", map[string]any{"path": "go.mod"})
	b := signature("read_file", map[string]any{"path": "README.md"})
	if a == b {
		t.Errorf("signature should differ when args differ; both = %s", a)
	}
}

// TestSignature_PathStyleDifferent codifies D56's "do not canonicalise
// paths" rule: ./go.mod and go.mod yield different signatures so
// switching path style resets the counter (the model's "different
// attempt" intuition).
func TestSignature_PathStyleDifferent(t *testing.T) {
	a := signature("read_file", map[string]any{"path": "./go.mod"})
	b := signature("read_file", map[string]any{"path": "go.mod"})
	if a == b {
		t.Errorf("./go.mod and go.mod should differ; both = %s", a)
	}
}

// TestSignature_DifferentToolsDifferentSig: same args with different
// tool names produce different signatures.
func TestSignature_DifferentToolsDifferentSig(t *testing.T) {
	args := map[string]any{"path": "go.mod"}
	a := signature("read_file", args)
	b := signature("delete_file", args)
	if a == b {
		t.Errorf("different tools should differ: both = %s", a)
	}
}

// TestSignature_NestedMapsCanonical: deeply-nested args still hash
// the same regardless of inner key order.
func TestSignature_NestedMapsCanonical(t *testing.T) {
	a := signature("x", map[string]any{
		"outer": map[string]any{"a": 1, "b": 2},
	})
	b := signature("x", map[string]any{
		"outer": map[string]any{"b": 2, "a": 1},
	})
	if a != b {
		t.Errorf("nested maps must canonicalise: %s vs %s", a, b)
	}
}

// TestSignature_FormatShape sanity-checks the "<tool>:<16hex>" form.
func TestSignature_FormatShape(t *testing.T) {
	s := signature("read_file", map[string]any{"path": "go.mod"})
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 || parts[0] != "read_file" {
		t.Errorf("malformed signature: %q", s)
	}
	if len(parts[1]) != 16 {
		t.Errorf("expected 16-hex hash; got %q (len=%d)", parts[1], len(parts[1]))
	}
}

// ============================================================
// failureCounter
// ============================================================

func TestFailureCounter_BasicIncrement(t *testing.T) {
	c := newFailureCounter()
	if got := c.Increment("k"); got != 1 {
		t.Errorf("first increment = %d; want 1", got)
	}
	if got := c.Increment("k"); got != 2 {
		t.Errorf("second increment = %d; want 2", got)
	}
	if got := c.Increment("k"); got != 3 {
		t.Errorf("third increment = %d; want 3", got)
	}
}

func TestFailureCounter_GetAfterIncrement(t *testing.T) {
	c := newFailureCounter()
	c.Increment("k")
	c.Increment("k")
	if got := c.Get("k"); got != 2 {
		t.Errorf("Get after 2 increments = %d; want 2", got)
	}
	if got := c.Get("never-set"); got != 0 {
		t.Errorf("Get on unset key = %d; want 0", got)
	}
}

func TestFailureCounter_ResetClears(t *testing.T) {
	c := newFailureCounter()
	c.Increment("k")
	c.Increment("k")
	c.Reset("k")
	if got := c.Get("k"); got != 0 {
		t.Errorf("Get after Reset = %d; want 0", got)
	}
	// Re-increment starts fresh.
	if got := c.Increment("k"); got != 1 {
		t.Errorf("Increment after Reset = %d; want 1", got)
	}
}

func TestFailureCounter_ConcurrentSafe(t *testing.T) {
	c := newFailureCounter()
	const N = 50
	const G = 4
	done := make(chan struct{}, G)
	for g := 0; g < G; g++ {
		go func() {
			for i := 0; i < N; i++ {
				c.Increment("k")
			}
			done <- struct{}{}
		}()
	}
	for g := 0; g < G; g++ {
		<-done
	}
	if got := c.Get("k"); got != N*G {
		t.Errorf("concurrent increments lost: got %d; want %d", got, N*G)
	}
}

// TestFailureCounter_DifferentKeysIndependent: counters for distinct
// signatures don't interfere.
func TestFailureCounter_DifferentKeysIndependent(t *testing.T) {
	c := newFailureCounter()
	c.Increment("a")
	c.Increment("a")
	c.Increment("b")
	if got := c.Get("a"); got != 2 {
		t.Errorf("a = %d; want 2", got)
	}
	if got := c.Get("b"); got != 1 {
		t.Errorf("b = %d; want 1", got)
	}
	c.Reset("a")
	if got := c.Get("a"); got != 0 {
		t.Errorf("a after reset = %d; want 0", got)
	}
	if got := c.Get("b"); got != 1 {
		t.Errorf("b unchanged = %d; want 1", got)
	}
}
