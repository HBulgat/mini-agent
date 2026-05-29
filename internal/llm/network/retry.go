package network

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

// RetryConfig captures the §8.10 / D44 policy. Zero values are NOT
// usable — call DefaultRetryConfig() to get a populated default.
type RetryConfig struct {
	Max         int           // max additional attempts beyond the first; 0 disables retry
	BackoffBase time.Duration // first backoff sleep
	Jitter      float64       // ± fraction of base; 0.2 means ± 20 %
}

// DefaultRetryConfig returns the §8.10.1 baseline (3 retries,
// 1s base, ± 20 % jitter). Production callers pull values from
// config.LLM.Network instead; tests use this.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		Max:         3,
		BackoffBase: time.Second,
		Jitter:      0.2,
	}
}

// TimeoutConfig matches §8.10.4. Both fields default to non-zero values
// in DefaultTimeoutConfig.
type TimeoutConfig struct {
	Request time.Duration // single-attempt budget (waiting + reading the stream)
	Total   time.Duration // total budget across retries
}

// DefaultTimeoutConfig returns the §8.10.4 baseline (120s per attempt,
// 300s total).
func DefaultTimeoutConfig() *TimeoutConfig {
	return &TimeoutConfig{
		Request: 120 * time.Second,
		Total:   300 * time.Second,
	}
}

// WithTimeout wraps `parent` with the total budget. Returns the new
// context + its cancel — callers MUST call cancel to release the
// timer (we can't defer it here because the stream reader holds the
// context past WithTimeout's return).
func WithTimeout(parent context.Context, cfg *TimeoutConfig) (context.Context, context.CancelFunc) {
	if cfg == nil || cfg.Total <= 0 {
		// Caller forgot to plumb a config; fall back to a long-but-bounded
		// budget so a buggy site doesn't wedge the whole agent.
		return context.WithTimeout(parent, 5*time.Minute)
	}
	return context.WithTimeout(parent, cfg.Total)
}

// WithRetry runs `fn` up to cfg.Max+1 times, sleeping between attempts
// per the exponential-backoff + jitter formula in §8.10.3. `fn` is
// expected to honor ctx promptly — we add no inner timeout of our own
// (callers wrap with context.WithTimeout themselves before invoking
// `fn`).
//
// The generic-typed wrapper avoids boxing arbitrary results into
// `any` everywhere; the network package callers all need different
// concrete return types.
func WithRetry[T any](
	ctx context.Context,
	cfg *RetryConfig,
	fn func(ctx context.Context) (T, error),
) (T, error) {
	if cfg == nil {
		cfg = DefaultRetryConfig()
	}
	var (
		zero    T
		lastErr error
	)
	for attempt := 0; attempt <= cfg.Max; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := fn(ctx)
		if err == nil {
			return result, nil
		}
		if !shouldRetry(err) {
			return zero, err
		}
		if attempt == cfg.Max {
			lastErr = err
			break
		}
		sleep := backoffDuration(attempt, cfg, err)
		// We use a select so a ctx cancellation during sleep returns
		// promptly rather than waiting out the timer.
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(sleep):
		}
		lastErr = err
	}
	return zero, fmt.Errorf("retry exhausted after %d attempt(s): %w", cfg.Max+1, lastErr)
}

// backoffDuration computes the sleep before attempt+1. We honor the
// server's Retry-After hint first (it's authoritative), then fall back
// to exponential backoff with ± jitter.
func backoffDuration(attempt int, cfg *RetryConfig, err error) time.Duration {
	// Honor Retry-After if present.
	if herr, ok := err.(*HTTPError); ok && herr.RetryAfter > 0 {
		return herr.RetryAfter
	}
	// Exponential: base * 2^attempt → 1s / 2s / 4s / 8s …
	base := cfg.BackoffBase * time.Duration(1<<attempt)
	if cfg.Jitter <= 0 {
		return base
	}
	// Jitter ∈ [-Jitter, +Jitter] of base. We pull a fresh random per
	// call instead of holding a *rand.Rand — modern Go's global rand is
	// already seeded and concurrency-safe.
	delta := (rand.Float64()*2 - 1) * cfg.Jitter * float64(base)
	out := base + time.Duration(delta)
	if out < 0 {
		out = base / 2
	}
	return out
}
