package network

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// HTTPError is the typed error every Provider's transport layer wraps
// non-2xx responses in. It carries enough metadata for shouldRetry +
// backoffDuration to make decisions without re-parsing the SDK's own
// error types.
//
// Provider sub-packages translate SDK-specific errors into HTTPError at
// the boundary; the rest of the network package only knows this one
// shape.
type HTTPError struct {
	StatusCode int           // 0 means "not an HTTP error"
	Status     string        // raw status line, e.g. "429 Too Many Requests"
	RetryAfter time.Duration // 0 if no Retry-After header
	Body       string        // truncated response body (≤ 1 KiB)
	Cause      error         // original SDK / network error, if any
}

// Error returns a one-line description suitable for logging. It
// intentionally avoids the verbose "wrapped" form so log lines stay
// scannable; callers needing the cause should use `errors.Unwrap`.
func (e *HTTPError) Error() string {
	if e == nil {
		return "<nil HTTPError>"
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("http %d: %s", e.StatusCode, e.Status)
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Status
}

// Unwrap exposes the underlying cause so callers can still use
// errors.Is/As against the SDK's own sentinel errors.
func (e *HTTPError) Unwrap() error { return e.Cause }

// ParseRetryAfter parses an RFC 7231 Retry-After header. Both numeric
// (seconds) and HTTP-date forms are accepted; unrecognized input
// returns 0 + a nil error so callers fall back to exponential backoff.
//
// We tolerate fractional seconds (e.g. "0.5") because some providers
// send sub-second guidance during 429 storms.
func ParseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(header, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second))
	}
	// HTTP-date form, e.g. "Wed, 21 Oct 2026 07:28:00 GMT".
	if t, err := time.Parse(time.RFC1123, header); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

// shouldRetry implements the retry-trigger matrix from §8.10.2:
//   - 5xx, 429, 408               retry
//   - other 4xx                   no retry (would just hit the same wall)
//   - net.Error / unexpected EOF  retry (transient transport failure)
//   - everything else             no retry
//
// Exposed as a function (not a method on RetryConfig) because tests
// want to feed it raw errors without spinning up a config.
func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	var herr *HTTPError
	if errors.As(err, &herr) {
		switch {
		case herr.StatusCode >= 500:
			return true
		case herr.StatusCode == 429, herr.StatusCode == 408:
			return true
		}
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}
