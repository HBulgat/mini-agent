// Package trace — see trace.go for the canonical event types and
// Recorder interface. The disk-bound recorder lives in internal/logs
// (T1.1 / R12). This package is intentionally dependency-free so any
// other module may import it without forming an import cycle.
//
// Reference: docs/system-design/05-core-abstractions.md §5.1
package trace
