// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package search

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

// Glob is the `glob` tool: filename pattern search using doublestar
// syntax (`**`, alternation, character classes).
//
// Implementation notes:
//
//   - Engine: bmatcuk/doublestar/v4 — same library used by the
//     permission gate and list_dir's ignore matcher, so users get
//     consistent semantics across tools.
//   - Cwd: relative patterns and the optional `cwd` arg both resolve
//     against the injected session.Cwd. The user can pass an absolute
//     pattern to override (it bypasses the cwd entirely).
//   - Caps: total matches 5000 (mirrors list_dir). Beyond that we
//     truncate + flag ForcedTruncated.
//   - Ordering: results are sorted by path so identical filesystems
//     produce identical output.
//   - Ignore set: glob deliberately does NOT apply the default ignore
//     set (.git/, node_modules/, etc.) because the entire point of
//     glob is to hit files matching the user's literal pattern. Use
//     a more specific pattern if noise is a problem.
type Glob struct {
	cwd func() string
}

// NewGlob constructs the glob tool. cwd resolves the optional `cwd`
// arg and any relative pattern (the latter via doublestar.Glob).
func NewGlob(cwd func() string) *Glob {
	if cwd == nil {
		cwd = func() string { return "" }
	}
	return &Glob{cwd: cwd}
}

// maxGlobResults caps the total number of paths returned. 5000 mirrors
// list_dir. doublestar.Glob walks the tree internally so we have to
// post-truncate rather than bail-early.
const maxGlobResults = 5000

// GlobArgs is the JSON input.
type GlobArgs struct {
	// Pattern is the doublestar glob to evaluate. doublestar accepts
	// `*`, `?`, `[...]` character classes, `{a,b}` alternation, and
	// `**` for recursive directory match.
	Pattern string `json:"pattern" jsonschema:"required,description=Doublestar glob pattern. Supports * ? [...] {a,b} alternation and ** for recursive descent. Example: 'src/**/*.go'"`

	// Cwd optionally overrides the session cwd for this call. When
	// empty, the session cwd is used.
	Cwd string `json:"cwd,omitempty" jsonschema:"description=Directory to search from. Relative to the session cwd; defaults to the session cwd when omitted."`
}

// ============================================================
// tool.Tool
// ============================================================

func (g *Glob) Name() string { return "glob" }

func (g *Glob) Description() string {
	return strings.TrimSpace(`
Find file paths matching a doublestar glob pattern.

When to use:
- Locating files by name pattern (e.g. all *.go under src/)
- Discovering test fixtures, configuration files, or build outputs
- Building a candidate list before reading specific files

When NOT to use:
- Searching file contents — use grep instead
- Listing every entry in one directory — use list_dir instead

Notes:
- Pattern syntax: doublestar (https://github.com/bmatcuk/doublestar).
  Supports *, ?, [...], {a,b}, and ** for recursive descent.
- Relative patterns resolve against the session cwd (or the optional
  cwd argument). Absolute patterns are honoured as-is.
- Results are sorted by path for determinism.
- Hard cap of 5000 paths; truncated output is flagged.
- Unlike list_dir, glob does NOT apply a default ignore set — your
  pattern is exactly what gets evaluated. To skip .git/, write a more
  specific pattern (e.g. 'src/**/*.go').
`)
}

func (g *Glob) Schema() map[string]any {
	r := &jsonschema.Reflector{
		ExpandedStruct:             true,
		DoNotReference:             true,
		RequiredFromJSONSchemaTags: true,
		Anonymous:                  true,
		AllowAdditionalProperties:  false,
	}
	return schemaToMap(r.Reflect(&GlobArgs{}))
}

func (g *Glob) Category() tool.Category { return tool.CategoryReadOnly }

// Invoke evaluates the glob pattern.
func (g *Glob) Invoke(ctx context.Context, input map[string]any) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, tool.MapCtxError(err)
	}
	args, err := g.decodeArgs(input)
	if err != nil {
		return tool.Result{}, err
	}
	if err := g.validateArgs(args); err != nil {
		return tool.Result{}, err
	}

	// Resolve the search root. Absolute pattern → root="" (doublestar
	// uses the os filesystem from the pattern's prefix). Relative
	// pattern → root = cwd or args.Cwd.
	root := ""
	pattern := args.Pattern
	if filepath.IsAbs(pattern) {
		// doublestar.Glob doesn't accept absolute patterns directly
		// in v4; we have to split into root + relative pattern.
		root, pattern = splitAbs(pattern)
	} else {
		base := args.Cwd
		if base == "" {
			base = g.cwd()
		}
		if base == "" {
			// Last-resort fallback so the call doesn't blow up; we
			// scan from the process cwd. This mirrors what `ls *`
			// would do in a shell.
			if wd, werr := os.Getwd(); werr == nil {
				base = wd
			}
		}
		root = base
	}

	if err := ctx.Err(); err != nil {
		return tool.Result{}, tool.MapCtxError(err)
	}

	matches, truncated, err := g.runGlob(ctx, root, pattern)
	if err != nil {
		return tool.Result{}, err
	}

	sort.Strings(matches)

	return tool.Result{
		Content:         buildGlobContent(args.Pattern, root, matches, truncated),
		Display:         buildGlobDisplay(args.Pattern, len(matches), truncated),
		ForcedTruncated: truncated,
	}, nil
}

// ============================================================
// argument decoding & validation
// ============================================================

func (g *Glob) decodeArgs(input map[string]any) (*GlobArgs, error) {
	if input == nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: "input is nil"}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: fmt.Sprintf("re-marshal input: %v", err)}
	}
	var out GlobArgs
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: err.Error()}
	}
	return &out, nil
}

func (g *Glob) validateArgs(a *GlobArgs) error {
	if strings.TrimSpace(a.Pattern) == "" {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "pattern is required"}
	}
	if !doublestar.ValidatePattern(a.Pattern) {
		return &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("invalid glob pattern %q", a.Pattern),
		}
	}
	return nil
}

// splitAbs separates an absolute pattern into (rootDir, relPattern).
// The rootDir is the leading literal portion (no glob metacharacters);
// the relPattern is everything after, including the first dir that
// contains globs. doublestar.Glob is run against an os.DirFS rooted
// at rootDir, with relPattern as the pattern argument.
func splitAbs(pattern string) (string, string) {
	// Strip leading "/", then walk segment by segment until we hit
	// the first segment containing a glob meta character. Everything
	// before becomes root; the rest becomes the relative pattern.
	parts := strings.Split(filepath.ToSlash(pattern), "/")
	rootParts := []string{""}
	patternStart := 0
	for i, p := range parts {
		if i == 0 && p == "" {
			continue // leading "/" => empty first segment
		}
		if hasGlobMeta(p) {
			patternStart = i
			break
		}
		rootParts = append(rootParts, p)
		patternStart = i + 1
	}
	root := strings.Join(rootParts, "/")
	if root == "" {
		root = "/"
	}
	rel := strings.Join(parts[patternStart:], "/")
	return root, rel
}

// hasGlobMeta reports whether the segment contains characters that
// doublestar treats as metacharacters: * ? [ ] { } .
func hasGlobMeta(s string) bool {
	for _, c := range s {
		switch c {
		case '*', '?', '[', ']', '{', '}':
			return true
		}
	}
	return false
}

// ============================================================
// runGlob: invoke doublestar against a fs.FS rooted at `root`
// ============================================================

// runGlob walks the doublestar pattern relative to root, returning the
// (root-prefixed) absolute paths plus a truncated flag.
func (g *Glob) runGlob(ctx context.Context, root, pattern string) ([]string, bool, error) {
	if root == "" {
		// Empty root with a relative pattern means "search from the
		// process cwd". We materialise it now so doublestar gets a
		// real root.
		wd, err := os.Getwd()
		if err != nil {
			return nil, false, tool.MapIOError(err)
		}
		root = wd
	}
	fsys := os.DirFS(root)
	out := make([]string, 0, 32)
	truncated := false
	err := doublestar.GlobWalk(fsys, pattern, func(p string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		full := filepath.Join(root, p)
		out = append(out, full)
		if len(out) >= maxGlobResults {
			truncated = true
			return doublestar.SkipDir
		}
		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, tool.MapCtxError(ctxErr)
		}
		// doublestar reports a malformed pattern via err here
		// (validatePattern caught the obvious cases earlier; this
		// is the runtime fallback).
		return nil, false, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("glob walk failed: %v", err),
		}
	}
	return out, truncated, nil
}

// ============================================================
// content / display
// ============================================================

// buildGlobContent renders the LLM payload — one path per line under
// a <glob> tag.
func buildGlobContent(pattern, root string, matches []string, truncated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<glob pattern=%q root=%q matches=%d>\n", pattern, root, len(matches))
	for _, p := range matches {
		fmt.Fprintf(&b, "%s\n", p)
	}
	b.WriteString("</glob>")
	if truncated {
		fmt.Fprintf(&b, "\n[warning: result truncated at %d paths; tighten the pattern]", maxGlobResults)
	}
	return b.String()
}

// buildGlobDisplay: "glob <pattern> (<n> match[es]) → ok [truncated]?"
func buildGlobDisplay(pattern string, n int, truncated bool) string {
	suffix := ""
	if truncated {
		suffix = " [truncated]"
	}
	return fmt.Sprintf("glob %s (%d match%s) → ok%s", pattern, n, pluralES(n), suffix)
}
