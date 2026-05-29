// Package agent implements the ReAct loop, sub-agent dispatch, step / tool
// failure counters and provider-switch logic.
//
// Status: skeleton only. Full design lives in:
//   - docs/system-design/05-core-abstractions.md §5.9
//   - docs/system-design/09-agent-engine.md (R6, D49–D67)
//
// This package depends on llm / tool / compaction / permission / skill /
// trace / session / uio. It is consumed by cli/repl and webapi via the
// Runner interface.
package agent
