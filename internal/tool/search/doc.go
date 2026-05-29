// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

// Package search hosts the read-only search tools that the agent uses
// to navigate a codebase: grep (RE2 regex over file contents) and
// glob (doublestar pattern over file paths).
//
// Both tools follow the R7-1' template documented in
// docs/system-design/10-tool-template-and-readfile.md and the
// per-tool contract rows in §10.7.
//
// Cross-tool consistency:
//
//   - Path resolution: relative paths resolve against an injected
//     cwd callback; the same shape as fs.NewReadFile etc.
//   - Schema: every tool exposes a typed *Args struct + reflected
//     JSON schema, golden-file pinned by D83.
//   - Error codes: ErrInvalidArgs / ErrNotFound / ErrIO / ErrInterrupted
//     follow the R7-1' D75 enum.
//   - Truncation: hard caps surface as Result.ForcedTruncated=true;
//     never silent.
//
// Default ignore set:
//
//   - grep applies the same conservative ignore set that list_dir
//     uses (.git/, node_modules/, dist/, *.pyc, ...) so output is
//     comparable.
//   - glob does NOT apply the default ignore set; the entire point of
//     glob is to evaluate the user's literal pattern. Tighten the
//     pattern (e.g. 'src/**/*.go') if noise is a problem.
package search
