// Package bootstrap is the ONE place allowed to import every other
// internal package. It hand-wires components (no DI framework, see
// D3) into a runnable application.
//
// Wiring order (canonical):
//   config → logs → trace recorder → session.Store + migrate
//   → llm.Registry (per-provider) → tool.Registry → permission.Gate
//   → skill.Loader → agentsmd.Loader → compaction.Compactor
//   → agent.Loop → uio implementation (cli/repl or webapi)
//
// Status: skeleton only. T1.8.
package bootstrap
