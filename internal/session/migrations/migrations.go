// Package migrations exposes the embedded SQL migration files as a
// regular embed.FS so any caller (the store package, the
// `mini-agent migrate` subcommand, future tests) can apply them
// without re-implementing the file walk.
//
// We can't `//go:embed` from internal/session/store/db.go because Go's
// embed directive forbids `..` paths — the embed has to live in a
// package whose source sits next to the assets. This file's only job
// is to host that directive.
package migrations

import "embed"

// FS contains every *.sql file at this directory's top level.
// golang-migrate's iofs source is happy with a sub-FS of "."  but for
// API symmetry with `embed.FS` consumers we expose the root.
//
//go:embed *.sql
var FS embed.FS

// Subdir is the path inside FS where the SQL files actually live.
// With the directive above the assets are at FS root, so this is just
// ".". We expose it as a constant anyway so callers don't hard-code
// the magic string.
const Subdir = "."
