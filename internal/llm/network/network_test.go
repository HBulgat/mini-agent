package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

// ---------- ParseRetryAfter ----------

func TestParseRetryAfter_Seconds(t *testing.T) {
	if got := ParseRetryAfter("3"); got != 3*time.Second {
		t.Errorf("ParseRetryAfter(\"3\") = %v, want 3s", got)
	}
	if got := ParseRetryAfter("0.5"); got != 500*time.Millisecond {
		t.Errorf("ParseRetryAfter(\"0.5\") = %v, want 500ms", got)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	future := time.Now().Add(2 * time.Second).UTC().Format(time.RFC1123)
	d := ParseRetryAfter(future)
	// Allow a generous slack for clock drift between Now() calls.
	if d < 1500*time.Millisecond || d > 3*time.Second {
		t.Errorf("ParseRetryAfter(future RFC1123) = %v, want ~2s", d)
	}
}

func TestParseRetryAfter_Invalid(t *testing.T) {
	if got := ParseRetryAfter("not a date"); got != 0 {
		t.Errorf("invalid input = %v, want 0", got)
	}
	if got := ParseRetryAfter(""); got != 0 {
		t.Errorf("empty input = %v, want 0", got)
	}
}

// ---------- shouldRetry ----------

func TestShouldRetry(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"500 retries", &HTTPError{StatusCode: 500}, true},
		{"503 retries", &HTTPError{StatusCode: 503}, true},
		{"429 retries", &HTTPError{StatusCode: 429}, true},
		{"408 retries", &HTTPError{StatusCode: 408}, true},
		{"400 no retry", &HTTPError{StatusCode: 400}, false},
		{"401 no retry", &HTTPError{StatusCode: 401}, false},
		{"net.Error retries", &net.OpError{Op: "dial", Err: errors.New("x")}, true},
		{"plain error no retry", errors.New("boom"), false},
		{"nil no retry", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldRetry(c.err); got != c.want {
				t.Errorf("shouldRetry(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// ---------- WithRetry ----------

// TestWithRetry_NoRetryOnSuccess: a successful first call returns
// immediately and never sleeps.
func TestWithRetry_NoRetryOnSuccess(t *testing.T) {
	calls := 0
	got, err := WithRetry(context.Background(), DefaultRetryConfig(),
		func(ctx context.Context) (string, error) {
			calls++
			return "ok", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" || calls != 1 {
		t.Errorf("got=%q calls=%d, want ok / 1", got, calls)
	}
}

// TestWithRetry_RetriesUntilSuccess: a 500 → 500 → 200 sequence
// produces three attempts and ends in success.
func TestWithRetry_RetriesUntilSuccess(t *testing.T) {
	cfg := &RetryConfig{Max: 3, BackoffBase: time.Millisecond, Jitter: 0}
	calls := 0
	got, err := WithRetry(context.Background(), cfg,
		func(ctx context.Context) (int, error) {
			calls++
			if calls < 3 {
				return 0, &HTTPError{StatusCode: 500}
			}
			return 42, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if got != 42 || calls != 3 {
		t.Errorf("got=%d calls=%d, want 42 / 3", got, calls)
	}
}

// TestWithRetry_NonRetryableStops: a 400 returns immediately, no
// retries.
func TestWithRetry_NonRetryableStops(t *testing.T) {
	cfg := &RetryConfig{Max: 5, BackoffBase: time.Millisecond, Jitter: 0}
	calls := 0
	_, err := WithRetry(context.Background(), cfg,
		func(ctx context.Context) (string, error) {
			calls++
			return "", &HTTPError{StatusCode: 400}
		})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 4xx)", calls)
	}
}

// TestWithRetry_MaxExhausted: persistent 500s eventually stop after
// Max+1 attempts.
func TestWithRetry_MaxExhausted(t *testing.T) {
	cfg := &RetryConfig{Max: 2, BackoffBase: time.Millisecond, Jitter: 0}
	calls := 0
	_, err := WithRetry(context.Background(), cfg,
		func(ctx context.Context) (int, error) {
			calls++
			return 0, &HTTPError{StatusCode: 503, Status: "boom"}
		})
	if err == nil {
		t.Fatal("expected exhausted error")
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (Max+1)", calls)
	}
}

// TestWithRetry_CtxCancellationStops: cancelling ctx mid-backoff
// returns ctx.Err immediately rather than waiting out the timer.
func TestWithRetry_CtxCancellationStops(t *testing.T) {
	cfg := &RetryConfig{Max: 5, BackoffBase: 5 * time.Second, Jitter: 0}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := WithRetry(ctx, cfg, func(ctx context.Context) (int, error) {
			return 0, &HTTPError{StatusCode: 500}
		})
		done <- err
	}()

	// Give the loop time to enter the backoff sleep, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WithRetry did not honor cancellation within 2s")
	}
}

// ---------- HTTPError ----------

func TestHTTPError_ErrorString(t *testing.T) {
	e := &HTTPError{StatusCode: 429, Status: "429 Too Many"}
	if got := e.Error(); got == "" {
		t.Errorf("empty Error()")
	}
	cause := errors.New("connection reset")
	wrap := &HTTPError{Cause: cause}
	if !errors.Is(wrap, cause) {
		t.Errorf("Unwrap broken: %v", wrap)
	}
}

func TestHTTPError_NilSafe(t *testing.T) {
	var e *HTTPError
	if got := e.Error(); got == "" {
		t.Error("nil Error() should return placeholder, got empty")
	}
}

// ---------- backoffDuration ----------

// TestBackoff_HonorsRetryAfter: when the server hints, we use that
// regardless of attempt index.
func TestBackoff_HonorsRetryAfter(t *testing.T) {
	cfg := &RetryConfig{BackoffBase: time.Second, Jitter: 0.5}
	err := &HTTPError{StatusCode: 429, RetryAfter: 7 * time.Second}
	if got := backoffDuration(0, cfg, err); got != 7*time.Second {
		t.Errorf("backoff with Retry-After = %v, want 7s", got)
	}
}

// TestBackoff_Exponential: without a hint, base * 2^attempt grows the
// expected way.
func TestBackoff_Exponential(t *testing.T) {
	cfg := &RetryConfig{BackoffBase: 100 * time.Millisecond, Jitter: 0}
	for attempt, want := range map[int]time.Duration{
		0: 100 * time.Millisecond,
		1: 200 * time.Millisecond,
		2: 400 * time.Millisecond,
		3: 800 * time.Millisecond,
	} {
		got := backoffDuration(attempt, cfg, errors.New("net"))
		if got != want {
			t.Errorf("attempt %d: got %v, want %v", attempt, got, want)
		}
	}
}

// TestBackoff_JitterStaysInBounds: with jitter, values should fall
// within ±Jitter of base.
func TestBackoff_JitterStaysInBounds(t *testing.T) {
	cfg := &RetryConfig{BackoffBase: 100 * time.Millisecond, Jitter: 0.2}
	base := 100 * time.Millisecond
	low, high := time.Duration(float64(base)*0.8), time.Duration(float64(base)*1.2)
	for i := 0; i < 100; i++ {
		got := backoffDuration(0, cfg, errors.New("x"))
		if got < low || got > high {
			t.Errorf("iter %d: got %v, want in [%v, %v]", i, got, low, high)
		}
	}
}

// ---------- WithTimeout ----------

func TestWithTimeout_AppliesTotal(t *testing.T) {
	cfg := &TimeoutConfig{Total: 200 * time.Millisecond}
	ctx, cancel := WithTimeout(context.Background(), cfg)
	defer cancel()
	select {
	case <-ctx.Done():
		// Expected: ctx fires after ~200ms.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WithTimeout did not enforce Total budget")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("got %v, want DeadlineExceeded", ctx.Err())
	}
}

func TestWithTimeout_NilConfigStillBounds(t *testing.T) {
	ctx, cancel := WithTimeout(context.Background(), nil)
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Error("nil config should still apply a fallback deadline")
	}
}

// quick smoke test — ensure the test file compiles a fully wired
// config.
func TestSmoke_Defaults(t *testing.T) {
	rc := DefaultRetryConfig()
	tc := DefaultTimeoutConfig()
	if rc.Max <= 0 || tc.Total <= 0 {
		t.Errorf("defaults broken: %+v %+v", rc, tc)
	}
	_ = fmt.Sprintf("%v %v", rc, tc) // touch fmt import
}
