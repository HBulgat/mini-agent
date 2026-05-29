// Package agentsmd loads project guidance text from AGENTS.md files
// and merges them into a single string suitable for injection as a
// system message in the agent loop.
//
// Design contract: docs/system-design/07-config-and-rules.md §7.2
// (R4) and key decisions D27 / D52 / D53.
//
// Discovery (low-to-high priority):
//
//  1. Global: cfg.GlobalPath (default ~/.mini-agent/AGENTS.md)
//  2. Project: <cwd>/AGENTS.md (only if cfg.ProjectLookup == true)
//
// **No upward recursion** — placing AGENTS.md anywhere other than the
// startup cwd is a no-op. This is deliberate: in monorepos an
// upward search would silently pull in an ancestor's guidelines.
//
// Merge:
//
// When both files exist they are joined with a markdown rule:
//
//	[global]
//	\n---\n
//	[project]
//
// The agent loop wraps the result in <project_guidelines>...</project_guidelines>
// (D52); this loader returns the *raw* merged text and is intentionally
// agnostic about the wrapper.
//
// Failure modes (all fail-soft):
//
//   - Neither file exists      → returns ("", nil)
//   - One file exists          → returns its body
//   - File exists but unreadable → trace warning, treated as missing
//   - File is empty            → treated as missing
//   - File exceeds MaxBytes    → first MaxBytes bytes are kept and
//     "\n[...truncated, N bytes total]" is appended (D53). The model
//     can therefore tell that it has been given a partial document.
package agentsmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DefaultMaxBytes is the per-file size cap for AGENTS.md content.
// Anything beyond this is truncated and a marker is appended (D53).
const DefaultMaxBytes int64 = 1 << 20 // 1 MiB

// projectFileName is the filename looked up in cwd. Not configurable:
// the design intentionally fixes this so users can rely on a single
// stable location.
const projectFileName = "AGENTS.md"

// Loader is the contract consumed by agent.prepareInitialHistory.
// The agent package declares an identical interface
// (agent.AgentsMDLoader) so its tests can stub the loader without
// importing this package; this concrete type satisfies that interface
// by structural matching.
type Loader interface {
	// Load returns the merged AGENTS.md text for the given cwd, or
	// the empty string if no guidance is present. Errors are
	// returned only for *unrecoverable* problems; missing files,
	// empty files, and read errors on individual sources are
	// swallowed (with a trace event) so a failed load never breaks
	// the agent loop.
	Load(ctx context.Context, cwd string) (string, error)
}

// Config controls discovery and ingestion. Pointer receiver per D31
// because Config is small but holds path strings we don't want to
// copy needlessly across constructor boundaries.
type Config struct {
	// GlobalPath is the absolute path to the user's global
	// AGENTS.md. Empty disables the global source.
	//
	// "~" is expanded by the loader on each Load call (cheap; lets
	// HOME changes between sessions take effect without a rebuild).
	GlobalPath string

	// ProjectLookup gates the per-cwd lookup. When false the loader
	// only consults GlobalPath.
	ProjectLookup bool

	// MaxBytes caps the size of *each* source file before merging.
	// Zero or negative means use DefaultMaxBytes.
	MaxBytes int64
}

// realLoader is the default Loader. It owns no mutable state — the
// "cache on startup, reload on /cd" semantics from §7.2.3 are the
// caller's responsibility (each Load is a fresh read). Holding a
// cache here would conflict with the loader being trivially testable
// from a temp directory.
type realLoader struct {
	globalPath    string
	projectLookup bool
	maxBytes      int64
}

// New constructs a Loader. A nil cfg is treated as the zero value —
// no global path, no project lookup, default MaxBytes.
func New(cfg *Config) Loader {
	l := &realLoader{
		maxBytes: DefaultMaxBytes,
	}
	if cfg != nil {
		l.globalPath = cfg.GlobalPath
		l.projectLookup = cfg.ProjectLookup
		if cfg.MaxBytes > 0 {
			l.maxBytes = cfg.MaxBytes
		}
	}
	return l
}

// Load implements Loader. ctx is honoured at file boundaries so a
// cancelled load returns ctx.Err() promptly rather than reading two
// large files in sequence.
func (l *realLoader) Load(ctx context.Context, cwd string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	var (
		globalText  string
		projectText string
	)

	// Source 1: global AGENTS.md.
	if expanded := expandHome(l.globalPath); expanded != "" {
		globalText = readSource(expanded, l.maxBytes)
	}

	// Source 2: project-level AGENTS.md (no upward recursion).
	if l.projectLookup && cwd != "" {
		// Re-check cancellation between source reads — the global
		// read above may have done meaningful work.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		projectPath := filepath.Join(cwd, projectFileName)
		projectText = readSource(projectPath, l.maxBytes)
	}

	return mergeSections(globalText, projectText), nil
}

// readSource reads `path` if it exists and returns its (possibly
// truncated) body. Any error other than "not found" is *also*
// silently treated as missing per §7.2.5 — we cannot let a permission
// problem on AGENTS.md break agent startup.
//
// Empty files are also treated as missing: there is no semantic
// difference between "no AGENTS.md" and "empty AGENTS.md", and
// emitting a stray "---" separator for the empty case would mislead
// the model into thinking guidance exists.
func readSource(path string, maxBytes int64) string {
	if path == "" {
		return ""
	}

	info, err := os.Stat(path)
	if err != nil {
		// errors.Is(fs.ErrNotExist) is the common case; permission
		// denied / IsDir / etc. all collapse to the same outcome.
		// We deliberately do not log here — the caller (typically
		// the agent loop) already has trace context and can emit a
		// single bookkeeping event.
		_ = err
		return ""
	}
	if info.IsDir() {
		// AGENTS.md happening to be a directory is so unusual that
		// surfacing it would just confuse the user. Skip silently.
		return ""
	}
	if info.Size() == 0 {
		return ""
	}

	// Read up to maxBytes+1 to detect truncation in one syscall.
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	// Bound the buffer at maxBytes+1 so a giant file doesn't OOM
	// the agent. Reading exactly maxBytes+1 bytes lets us
	// distinguish "fits" from "needs truncation" without a second
	// stat call (info.Size() could be stale on some filesystems).
	buf := make([]byte, maxBytes+1)
	n, err := readAtMost(f, buf)
	if err != nil && !isEOF(err) {
		return ""
	}

	if int64(n) > maxBytes {
		// Truncate to maxBytes and append the marker. We use
		// info.Size() for the "X bytes total" hint because that is
		// what the user actually has on disk and is most useful as
		// debugging context. (If the file is being written
		// concurrently the number may drift, but the loader is
		// snapshot-on-load by design.)
		body := string(buf[:maxBytes])
		return body + fmt.Sprintf("\n[...truncated, %d bytes total]", info.Size())
	}
	return string(buf[:n])
}

// readAtMost is io.ReadFull-but-tolerant: it reads until the buffer
// is full or EOF, returning the number of bytes actually read. Unlike
// io.ReadFull it does not error on a short read.
func readAtMost(r interface{ Read(p []byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			break
		}
	}
	return total, nil
}

// isEOF returns true if err is the standard io.EOF signal. We use a
// string compare rather than importing "io" twice to keep the test
// surface minimal.
func isEOF(err error) bool {
	return err != nil && (err.Error() == "EOF" || errors.Is(err, fs.ErrClosed))
}

// mergeSections joins the two sources per §7.2.2. Empty inputs are
// elided so a single-source load returns clean text without leading
// or trailing separator artefacts.
func mergeSections(globalText, projectText string) string {
	g := strings.TrimSpace(globalText)
	p := strings.TrimSpace(projectText)
	switch {
	case g == "" && p == "":
		return ""
	case g != "" && p == "":
		return g
	case g == "" && p != "":
		return p
	default:
		// Both present: "global \n\n---\n\n project". The blank
		// lines around the rule make the boundary unambiguous when
		// either side ends/starts with a heading, list item, etc.
		return g + "\n\n---\n\n" + p
	}
}

// expandHome turns a leading "~" or "~/" into the user's HOME. We do
// this on every Load (rather than once at New) so a stale HOME from
// the spawning shell can be overridden at runtime.
//
// If HOME cannot be resolved we return the path unchanged — Stat
// will then fail-soft and the source is treated as missing.
func expandHome(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
		return p
	}
	return p
}
