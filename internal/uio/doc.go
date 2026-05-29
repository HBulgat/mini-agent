// Package uio — see uio.go for the canonical Sink + Prompter contract.
//
// Built-in helpers live alongside:
//   - nop.go    NopSink + NopPrompter          (always-deny defaults)
//   - multi.go  MultiSink fan-out helper       (CLI + log dual-write)
//
// The CLI implementation lives at internal/cli/repl/uio.go (T1.7); the
// Web UI implementation lives at internal/webapi/uio.go (T5.3). The
// agent / tool / permission packages depend on these interfaces only.
package uio
