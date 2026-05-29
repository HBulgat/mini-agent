package tool

import (
	"context"
	"errors"
	"io/fs"
)

// ErrorCode is the closed enumeration of failure categories every tool
// must speak. The agent's failure counter (D56/D57) hashes name + code
// + argument digest, so adding a new code retroactively changes the
// signature space — only extend with team agreement.
//
// String values are stable: they go into trace events and are printed
// in error messages users see. Do not rename.
type ErrorCode string

const (
	// ErrInvalidArgs covers parse failures, missing required fields,
	// out-of-range values, and binary-file rejections in read_file.
	// Anything attributable to "the LLM or user passed bad input".
	ErrInvalidArgs ErrorCode = "invalid_args"

	// ErrPermissionDenied is what permission.Gate returns when a rule
	// blocks the operation. Hard-blacklist denies share this code
	// (the Reason field disambiguates).
	ErrPermissionDenied ErrorCode = "permission_denied"

	// ErrNotFound: the targeted file / path / item does not exist.
	// Distinguished from ErrIO so the LLM can offer a different fix
	// (try a different path vs. retry).
	ErrNotFound ErrorCode = "not_found"

	// ErrIO covers any OS-level IO failure that isn't NotFound:
	// permission denied at the filesystem layer, disk full, broken
	// pipe, EOF mid-read, etc.
	ErrIO ErrorCode = "io_error"

	// ErrTimeout is reserved for context.DeadlineExceeded propagation
	// (D63 + §10.2 R5). Tools must not invent their own "timeout"
	// codes.
	ErrTimeout ErrorCode = "timeout"

	// ErrInterrupted is reserved for context.Canceled propagation
	// (Ctrl+C in CLI, page close in Web).
	ErrInterrupted ErrorCode = "interrupted"

	// ErrToolInternal is what panic-recover wrappers and "shouldn't
	// happen" guards return. Distinct from ErrIO so trace can
	// surface it as a bug to be fixed.
	ErrToolInternal ErrorCode = "tool_internal"

	// ErrTooLarge (R7-1' new) is set together with
	// Result.ForcedTruncated when the tool's internal cap clipped the
	// response. Tools may also return it as a hard error if no useful
	// truncation is possible (e.g. a single record bigger than the
	// cap).
	ErrTooLarge ErrorCode = "too_large"

	// ErrAmbiguous (R7-1' new) is for "the request matched more than
	// one thing". The canonical user is edit_file's old_str matching
	// multiple sites; the message must include disambiguation hints.
	ErrAmbiguous ErrorCode = "ambiguous"
)

// Error is the canonical wrapper every tool should return on failure.
// Plain errors still satisfy the Tool contract but lose the structured
// code, so the agent's failure counter and the trace UI degrade.
//
// Per D31 we keep the value-receiver String/Error/Unwrap pair so
// passing *Error to fmt / errors works out of the box.
type Error struct {
	// Code is one of the ErrorCode constants above. Required.
	Code ErrorCode

	// Message is the human-readable text shown to the LLM (in the
	// tool_result block) and to the user (in the trace UI). English,
	// per D76; do not embed retry hints — the agent loop appends
	// the "try a different way" notice itself.
	Message string

	// Retryable hints to the agent loop whether a same-shape retry
	// makes sense. Most failures are not retryable (the failure
	// counter handles the "stop trying the same thing" case), but
	// transient IO / 5xx upstream errors are.
	Retryable bool

	// Cause is the underlying error if any. Populated by mapIOError
	// / mapCtxError helpers so callers can errors.Is(err, fs.ErrNotExist).
	Cause error
}

// Error makes Error satisfy the error interface. Returns just Message
// (without the code) because the LLM is shown this directly and the
// code prefix would look like noise.
func (e *Error) Error() string { return e.Message }

// Unwrap exposes the underlying cause for errors.Is / errors.As checks.
// Returning nil is harmless (errors.Unwrap is nil-safe).
func (e *Error) Unwrap() error { return e.Cause }

// IsCode is a convenience helper for tests and trace formatting.
// Equivalent to: if te, ok := err.(*Error); ok && te.Code == code.
func IsCode(err error, code ErrorCode) bool {
	var te *Error
	if !errors.As(err, &te) {
		return false
	}
	return te.Code == code
}

// MapIOError converts a stdlib filesystem error into a *Error. fs.ErrNotExist
// becomes ErrNotFound; everything else becomes ErrIO. Returns nil if err
// is nil so callers can tail-call: return result, MapIOError(err).
//
// Tools beyond fs/* may need their own mappers (e.g. shell wraps exec.Error).
// This helper sits in the tool package because R7-1' R5 mandates the same
// contract for every tool.
func MapIOError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return &Error{Code: ErrNotFound, Message: err.Error(), Cause: err}
	}
	return &Error{Code: ErrIO, Message: err.Error(), Cause: err}
}

// MapCtxError converts a context error into the canonical tool error
// per D63 + R7-1' §10.2 R5. Returns the original error unchanged if it
// is not a context error so callers can chain: return result, MapCtxError(err).
func MapCtxError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return &Error{Code: ErrInterrupted, Message: "interrupted by user", Cause: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Code: ErrTimeout, Message: "operation timed out", Cause: err}
	}
	return err
}
