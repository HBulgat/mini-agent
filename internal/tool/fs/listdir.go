// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/invopop/jsonschema"

	"github.com/HBulgat/mini-agent/internal/tool"
)

// ListDir is the `list_dir` tool: lists directory entries with optional
// recursion and a default ignore set so the agent never drowns in
// .git/ or node_modules/ noise.
//
// Implementation choices (R7-1' §10.7 + implementation-time):
//
//   - Default ignore set covers VCS / IDE / dependency / build folders
//     that account for the bulk of "useless" matches in real repos.
//     When the caller passes a non-nil ignore_patterns list, that list
//     replaces the defaults (Unix-style: caller-passed = caller owns).
//   - Recursion is opt-in (default false) to match `ls` semantics.
//   - Total entry count is hard-capped at maxListDirEntries; overflow
//     yields ForcedTruncated=true with a Result.Display marker.
//   - Output is sorted by path so identical filesystems produce
//     identical output regardless of readdir order.
type ListDir struct {
	cwd func() string
}

// NewListDir constructs the list_dir tool. cwd is consulted on every
// Run to resolve relative paths (the REPL's `/cwd` switch takes
// effect immediately).
func NewListDir(cwd func() string) *ListDir {
	if cwd == nil {
		cwd = func() string { return "" }
	}
	return &ListDir{cwd: cwd}
}

// Hard caps. These deliberately don't appear in the schema — they're
// safety rails on tool output, not user-tunable knobs. A directory
// with more than maxListDirEntries paths is almost certainly the
// wrong target for an LLM (think /usr/share or a giant build/ dir),
// so we truncate and let the caller refine the path.
const (
	// maxListDirEntries caps the total number of entries we return.
	// 5000 is roughly enough to list a medium-sized repo's source
	// tree but small enough that the resulting prompt fits in any
	// modern context window.
	maxListDirEntries = 5000

	// maxListDirDepth caps recursion depth. Shielded against
	// pathological symlink loops; filepath.WalkDir already follows
	// the "don't descend into symlinks" default but a depth limit
	// is cheap insurance.
	maxListDirDepth = 32
)

// defaultListDirIgnore is the conservative default ignore set. These
// patterns use doublestar syntax and are matched against both the
// entry's basename and its path-relative-to-root form (the latter
// catches things like "src/node_modules"). Trailing "/" is treated
// as "this entry is a directory" — we strip it before matching and
// require IsDir.
var defaultListDirIgnore = []string{
	".git/",
	".hg/",
	".svn/",
	"node_modules/",
	"__pycache__/",
	".idea/",
	".vscode/",
	"dist/",
	"build/",
	".DS_Store",
	"*.pyc",
	"*.pyo",
	"*.class",
}

// ListDirArgs describes the JSON input to list_dir. *bool / *[]string
// pointers preserve the "caller didn't pass this field" vs "caller
// explicitly passed empty" distinction needed for the default-fill
// semantics.
type ListDirArgs struct {
	// Path is the directory to list. Relative paths are resolved
	// against the runtime cwd. Required.
	Path string `json:"path" jsonschema:"required,description=Directory path to list. Relative paths resolve against the session cwd."`

	// Recursive enables descending into subdirectories. Defaults
	// to false so behaviour matches `ls` rather than `find`.
	Recursive *bool `json:"recursive,omitempty" jsonschema:"description=When true, list every entry under path recursively (depth-first). When false, list only direct children; default: false."`

	// IgnorePatterns overrides the default ignore set when non-nil.
	// Patterns use doublestar glob syntax. Trailing "/" matches
	// directories; otherwise matches files. Patterns without "/"
	// match against basename; patterns containing "/" match against
	// the path relative to the listing root.
	IgnorePatterns *[]string `json:"ignore_patterns,omitempty" jsonschema:"description=Glob patterns to skip (doublestar syntax). When omitted, a conservative default set is used (.git/, node_modules/, __pycache__/, etc.). Pass [] to disable all filtering."`
}

// ============================================================
// tool.Tool interface
// ============================================================

// Name returns the canonical tool name.
func (l *ListDir) Name() string { return "list_dir" }

// Description is the long-form description LLMs see when picking
// tools. Style follows D71: when to use / when NOT to use / notes.
func (l *ListDir) Description() string {
	return strings.TrimSpace(`
List entries in a directory.

When to use:
- Discovering a project's structure before reading specific files
- Confirming a path exists and what's inside it
- Locating files when you don't know their exact name

When NOT to use:
- Searching by file *name pattern* across many directories — use glob instead
- Searching file *contents* — use grep instead
- Recursively listing huge directories like / or /usr — too noisy; narrow the path first

Notes:
- Default skips .git/, node_modules/, __pycache__/, .DS_Store, *.pyc, IDE folders, dist/, build/.
  Pass an explicit ignore_patterns to override; pass [] to disable all filtering.
- Output is sorted by path for determinism.
- Hard cap of 5000 entries; truncated output is flagged.
- Symlinks are not followed (we report them as entries but don't descend).
`)
}

// Schema returns the JSON Schema for ListDirArgs, reflected from the
// typed struct so docs and validation never drift apart (D69/D83).
func (l *ListDir) Schema() map[string]any {
	r := &jsonschema.Reflector{
		ExpandedStruct:             true,
		DoNotReference:             true,
		RequiredFromJSONSchemaTags: true,
		Anonymous:                  true,
		AllowAdditionalProperties:  false,
	}
	return schemaToMap(r.Reflect(&ListDirArgs{}))
}

// Category puts list_dir in the ReadOnly bucket — it never mutates
// the filesystem nor spawns subprocesses.
func (l *ListDir) Category() tool.Category { return tool.CategoryReadOnly }

// Invoke is the entry point invoked by the agent loop. The receiver is
// stateless beyond the cwd callback; concurrent calls are safe.
func (l *ListDir) Invoke(ctx context.Context, input map[string]any) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, tool.MapCtxError(err)
	}

	args, err := l.decodeArgs(input)
	if err != nil {
		return tool.Result{}, err
	}
	if err := l.validateArgs(args); err != nil {
		return tool.Result{}, err
	}

	abs := resolvePath(l.cwd, args.Path)

	info, err := os.Stat(abs)
	if err != nil {
		return tool.Result{}, tool.MapIOError(err)
	}
	if !info.IsDir() {
		return tool.Result{}, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("path %q is not a directory", args.Path),
		}
	}

	recursive := args.Recursive != nil && *args.Recursive
	ignore := defaultListDirIgnore
	if args.IgnorePatterns != nil {
		ignore = *args.IgnorePatterns
	}

	entries, truncated, err := l.collect(ctx, abs, recursive, ignore)
	if err != nil {
		return tool.Result{}, err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	return tool.Result{
		Content:         buildListDirContent(args.Path, entries, truncated),
		Display:         buildListDirDisplay(args.Path, len(entries), truncated, recursive),
		ForcedTruncated: truncated,
	}, nil
}

// ============================================================
// argument decoding & validation
// ============================================================

// decodeArgs strict-decodes input into ListDirArgs. Unknown fields
// are rejected so typos don't silently fall through.
func (l *ListDir) decodeArgs(input map[string]any) (*ListDirArgs, error) {
	if input == nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: "input is nil"}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: fmt.Sprintf("re-marshal input: %v", err)}
	}
	var out ListDirArgs
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: err.Error()}
	}
	return &out, nil
}

func (l *ListDir) validateArgs(a *ListDirArgs) error {
	if strings.TrimSpace(a.Path) == "" {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "path is required"}
	}
	return nil
}

// ============================================================
// collection
// ============================================================

// listEntry is the internal record emitted by collect. We keep the
// relative path (used for sorting and matching) and a typed "is dir"
// flag rather than the full os.FileInfo to keep memory bounded for
// huge listings.
type listEntry struct {
	rel     string // path relative to the listing root, never starts with "/"
	isDir   bool
	isLink  bool
	size    int64
}

// collect walks the directory tree (or just one level when !recursive)
// applying ignore patterns. Returns the entries collected, whether
// the cap was hit, and any I/O error encountered.
func (l *ListDir) collect(ctx context.Context, root string, recursive bool, ignore []string) ([]listEntry, bool, error) {
	out := make([]listEntry, 0, 64)
	truncated := false

	if !recursive {
		// Direct children only.
		dirEntries, err := os.ReadDir(root)
		if err != nil {
			return nil, false, tool.MapIOError(err)
		}
		for _, de := range dirEntries {
			if err := ctx.Err(); err != nil {
				return nil, false, tool.MapCtxError(err)
			}
			rel := de.Name()
			if matchIgnore(ignore, rel, de.IsDir()) {
				continue
			}
			info, statErr := de.Info()
			size := int64(0)
			if statErr == nil {
				size = info.Size()
			}
			isLink := de.Type()&fs.ModeSymlink != 0
			out = append(out, listEntry{rel: rel, isDir: de.IsDir(), isLink: isLink, size: size})
			if len(out) >= maxListDirEntries {
				truncated = true
				break
			}
		}
		return out, truncated, nil
	}

	// Recursive walk. We use WalkDir to avoid loading every Info up
	// front; only stat-on-demand below.
	walkErr := filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable subdir shouldn't kill the whole walk;
			// fall through skipping it.
			if path == root {
				return err // can't recover from root failure
			}
			return nil
		}
		if path == root {
			return nil // skip the root itself
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		// Normalize to forward slashes so ignore patterns and the
		// final output are OS-independent.
		rel = filepath.ToSlash(rel)

		// Depth limit.
		if strings.Count(rel, "/") >= maxListDirDepth {
			if de.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if matchIgnore(ignore, rel, de.IsDir()) {
			if de.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, statErr := de.Info()
		size := int64(0)
		if statErr == nil {
			size = info.Size()
		}
		isLink := de.Type()&fs.ModeSymlink != 0
		out = append(out, listEntry{rel: rel, isDir: de.IsDir(), isLink: isLink, size: size})

		if len(out) >= maxListDirEntries {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		// ctx errors come through here too.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, tool.MapCtxError(ctxErr)
		}
		return nil, false, tool.MapIOError(walkErr)
	}
	return out, truncated, nil
}

// matchIgnore returns true iff `rel` should be skipped given the
// ignore patterns. rel is forward-slashed and relative to the listing
// root. The match strategy:
//
//   - Pattern ending in "/": match only directories. Strip the slash,
//     then check the basename against the pattern, AND check whether
//     any path segment equals the pattern (so "node_modules/" hits
//     deep nested ones too).
//   - Pattern containing "/": full-path doublestar match.
//   - Pattern without "/": basename doublestar match.
func matchIgnore(patterns []string, rel string, isDir bool) bool {
	if len(patterns) == 0 {
		return false
	}
	base := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		base = rel[i+1:]
	}
	for _, p := range patterns {
		dirOnly := strings.HasSuffix(p, "/")
		pp := strings.TrimSuffix(p, "/")
		if dirOnly && !isDir {
			continue
		}
		switch {
		case strings.Contains(pp, "/"):
			if ok, _ := doublestar.Match(pp, rel); ok {
				return true
			}
		default:
			// Match against basename first.
			if ok, _ := doublestar.Match(pp, base); ok {
				return true
			}
			// Also match against any intermediate segment so
			// "node_modules" hits "src/node_modules/foo".
			for _, seg := range strings.Split(rel, "/") {
				if seg == pp {
					return true
				}
			}
		}
	}
	return false
}

// ============================================================
// content / display
// ============================================================

// buildListDirContent builds the LLM-facing payload. Format:
//
//	<dir path="...">
//	d  src/
//	-  README.md            842
//	l  link -> target
//	</dir>
//	[warning: truncated at 5000 entries]   <- only when ForcedTruncated
//
// The two-space gap between type and name is intentional so column
// alignment doesn't depend on exact widths (LLMs handle this fine,
// and humans can read it too).
func buildListDirContent(path string, entries []listEntry, truncated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<dir path=%q entries=%d>\n", path, len(entries))
	for _, e := range entries {
		typ := "-"
		switch {
		case e.isLink:
			typ = "l"
		case e.isDir:
			typ = "d"
		}
		name := e.rel
		if e.isDir {
			name += "/"
		}
		if e.isDir || e.isLink {
			fmt.Fprintf(&b, "%s  %s\n", typ, name)
		} else {
			fmt.Fprintf(&b, "%s  %-40s %s\n", typ, name, humanizeBytes(int(e.size)))
		}
	}
	b.WriteString("</dir>")
	if truncated {
		fmt.Fprintf(&b, "\n[warning: list truncated at %d entries; refine path or use ignore_patterns]", maxListDirEntries)
	}
	return b.String()
}

// buildListDirDisplay renders the one-line UI summary per D74. Form:
//
//	list_dir <path> (<n> entries[, recursive]) → ok [truncated]?
func buildListDirDisplay(path string, n int, truncated, recursive bool) string {
	mode := ""
	if recursive {
		mode = ", recursive"
	}
	suffix := ""
	if truncated {
		suffix = " [truncated]"
	}
	return fmt.Sprintf("list_dir %s (%d entries%s) → ok%s", path, n, mode, suffix)
}
