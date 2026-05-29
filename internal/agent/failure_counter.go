// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package agent

import (
	"sync"
	"sync/atomic"
)

// failureCounter tracks consecutive failures keyed by signature.
// One Loop holds one counter; sub-agents (when they exist) get their
// own (D66 — counts deliberately do NOT cross the parent/child
// boundary because a child's task context is independent).
//
// Concurrency: sync.Map handles racy Increment from the parallel
// readonly bucket (D58/D59). The stored value is *atomic.Int64 so
// the actual count update is also race-free.
//
// Why not a plain map + mutex:
//   - sync.Map's typical workload (write-once, read-many) doesn't
//     match perfectly here, but the contention is small (one
//     increment per tool call) and the API is simpler than building
//     our own RWMutex wrapper.
type failureCounter struct {
	m sync.Map // signature → *atomic.Int64
}

// newFailureCounter constructs an empty counter.
func newFailureCounter() *failureCounter { return &failureCounter{} }

// Increment bumps the failure count for sig and returns the new
// value. A first-time signature returns 1. Safe to call concurrently
// with itself or Reset.
func (c *failureCounter) Increment(sig string) int {
	v, _ := c.m.LoadOrStore(sig, new(atomic.Int64))
	return int(v.(*atomic.Int64).Add(1))
}

// Reset clears the count for sig. Called after a successful tool
// invocation so a subsequent failure starts fresh. No-op when sig
// has no entry (Delete is idempotent on sync.Map).
func (c *failureCounter) Reset(sig string) {
	c.m.Delete(sig)
}

// Get returns the current count for sig, or 0 when absent. Read-only;
// useful for tests and the future trace recorder.
func (c *failureCounter) Get(sig string) int {
	v, ok := c.m.Load(sig)
	if !ok {
		return 0
	}
	return int(v.(*atomic.Int64).Load())
}
