// Package logs wraps slog with a lumberjack rotating file handler and
// a sensitive-field redactor (api_key, authorization, cookie, etc.).
//
// It is also the implementation of trace.Recorder: trace.Event values
// are serialised as JSON Lines through the same handler.
//
// Status: skeleton only. R12 locks the field schema. Implementation
// tracked by T0.5 / T4.7.
package logs
