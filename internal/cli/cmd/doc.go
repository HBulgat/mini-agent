// Package cmd hosts the cobra command tree:
//   - root  — drops into the REPL (or one-shot via -p / --print)
//   - serve — starts the Web UI backend (gin)
//   - migrate — runs DB migrations explicitly (auto-run on startup
//     anyway, see D15)
//   - version — prints build info
//
// Status: skeleton only. T0.6 (root + version), T5.4 (serve).
package cmd
