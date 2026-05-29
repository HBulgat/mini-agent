// Package repl runs the interactive REPL loop and dispatches slash
// commands (/help /exit /new /sessions /resume /clear /tools /cd /mode
// /cost /model /thinking /show-* etc). It implements both uio.Sink and
// uio.Prompter against stdin/stdout.
//
// Status: skeleton only. T1.7 (loop) + T2.7 / T4.4 / T4.8 (slash
// commands) — all gated on R9.
package repl
