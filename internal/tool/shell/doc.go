// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

// Package shell hosts the agent's Execute-category tools. Today that
// means just `bash`; future Execute tools (e.g. `python` REPL) would
// live here too.
//
// Why no shellwords parsing in v1:
//
//   - The hard blacklist enforced by permission.Gate already matches
//     against the raw command string with whitespace normalisation
//     (see internal/permission/matcher.go). That handles the common
//     "rm -rf /" / fork-bomb forms regardless of how they're chained.
//   - Token-level parsing of `&&` / `||` / `;` / `|` / `$()` /
//     backticks is what `bash -c` already does perfectly. We invoke
//     `/bin/bash -c <command>` and let the shell own those semantics.
//   - Future iterations may add a token-level pre-scan inside the
//     bash tool itself as defense-in-depth, but it's optional.
//
// Process group management:
//
//   - Every spawn sets `Setpgid: true` so the child becomes its own
//     pgid leader. On timeout / ctx cancel we `kill(-pgid, SIGKILL)`
//     to take down the whole tree (a long-running pipeline like
//     `sleep 1000 | grep foo` otherwise leaks the upstream child).
//
// Output discipline:
//
//   - stdout and stderr each live in a `cappedBuffer` capped at
//     50 KB. Past the cap, writes silently succeed (the writer
//     doesn't loop) but the buffer remembers it overflowed so the
//     Result can flag truncation.
//   - Non-zero exit codes are NOT errors. The Result carries
//     exit_code and the LLM decides whether to retry/fix.
package shell
