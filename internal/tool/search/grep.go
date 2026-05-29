// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

// Package search bundles tools that hunt for code & content across the
// filesystem: grep (regex search) and glob (filename pattern). Both
// share the conservative ignore set used by list_dir so the agent
// doesn't drown in .git/ or node_modules/ noise.
package search

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/invopop/jsonschema"

	"github.com/HBulgat/mini-agent/internal/tool"
)

// resolvePath converts a possibly-relative path to absolute via the
// injected cwd callback. Mirrors the helper in internal/tool/fs to
// keep both tool packages independent.
func resolvePath(cwd func() string, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	c := ""
	if cwd != nil {
		c = cwd()
	}
	if c == "" {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(c, path))
}

// schemaToMap converts an *jsonschema.Schema to a map[string]any via
// JSON round-trip — exactly the same shape used by the fs tools.
func schemaToMap(s any) map[string]any {
	if s == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(s)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// ============================================================
// Grep
// ============================================================

// Grep is the `grep` tool: RE2 regex search across one file or every
// file under a directory tree.
//
// Implementation choices (R7-1' §10.7 + implementation-time):
//
//   - Engine: Go's regexp package (RE2). No backtracking, no PCRE
//     features. We document this in the description so the LLM
//     doesn't reach for lookaheads.
//   - Three output modes: "content" (default), "files_with_matches",
//     "count". Modes match `rg`'s common flags.
//   - multiline=true sets RE2's (?s) flag so "." matches newlines.
//   - context_before/after add lines around each hit (rg -B / -A).
//   - Default ignore set mirrors list_dir's so output is comparable.
//   - Caps: file size 10 MB (skipped + warning), total matches 1000.
type Grep struct {
	cwd func() string
}

// NewGrep constructs the grep tool. cwd resolves relative `path`.
func NewGrep(cwd func() string) *Grep {
	if cwd == nil {
		cwd = func() string { return "" }
	}
	return &Grep{cwd: cwd}
}

// Caps. These don't appear in the schema — they're safety rails.
const (
	maxGrepFileBytes    = 10 * 1024 * 1024 // 10 MB per file
	maxGrepMatches      = 1000             // total match lines / file entries
	maxGrepFiles        = 5000             // total files visited
	maxGrepContextLines = 50               // per-call upper bound on B/A
)

// defaultGrepIgnore mirrors list_dir.defaultListDirIgnore so the two
// tools agree on what's noise.
var defaultGrepIgnore = []string{
	".git/", ".hg/", ".svn/",
	"node_modules/", "__pycache__/",
	".idea/", ".vscode/",
	"dist/", "build/",
	".DS_Store", "*.pyc", "*.pyo", "*.class",
}

// GrepArgs is the JSON input. *bool / *int preserve unset semantics.
type GrepArgs struct {
	Pattern       string `json:"pattern" jsonschema:"required,description=RE2 regular expression to search for. RE2 does not support lookaheads or backreferences."`
	Path          string `json:"path" jsonschema:"required,description=File or directory to search. Relative paths resolve against the session cwd. When a directory, all matching files under it are scanned recursively."`
	OutputMode    string `json:"output_mode,omitempty" jsonschema:"description=One of 'content' (default; matching lines + context), 'files_with_matches' (just file paths), 'count' (per-file match counts); default: content,enum=content,enum=files_with_matches,enum=count"`
	Multiline     *bool  `json:"multiline,omitempty" jsonschema:"description=When true the regex '.' matches newlines (RE2 (?s) flag); default: false."`
	ContextBefore *int   `json:"context_before,omitempty" jsonschema:"description=Lines to include before each match (rg -B); default: 0; max: 50."`
	ContextAfter  *int   `json:"context_after,omitempty" jsonschema:"description=Lines to include after each match (rg -A); default: 0; max: 50."`
}

// ============================================================
// tool.Tool
// ============================================================

func (g *Grep) Name() string { return "grep" }

func (g *Grep) Description() string {
	return strings.TrimSpace(`
Search file contents with a RE2 regular expression.

When to use:
- Finding occurrences of a function name, error message, TODO marker
- Locating where a string is referenced before reading surrounding code
- Counting matches per file to prioritise where to dig deeper

When NOT to use:
- Looking up files by name pattern — use glob instead
- Listing directory contents — use list_dir instead
- Reading the contents of a known file — use read_file instead

Notes:
- Engine is Go's regexp (RE2): no lookaheads, no backreferences.
- Set multiline=true to allow '.' to match newlines (RE2 (?s) flag).
- Three output modes: 'content' (default), 'files_with_matches', 'count'.
- Skips files >10 MB and the standard noise dirs (.git/, node_modules/, etc.).
- Total match cap is 1000; output truncation is flagged.
`)
}

func (g *Grep) Schema() map[string]any {
	r := &jsonschema.Reflector{
		ExpandedStruct:             true,
		DoNotReference:             true,
		RequiredFromJSONSchemaTags: true,
		Anonymous:                  true,
		AllowAdditionalProperties:  false,
	}
	return schemaToMap(r.Reflect(&GrepArgs{}))
}

func (g *Grep) Category() tool.Category { return tool.CategoryReadOnly }

// Invoke runs the regex search.
func (g *Grep) Invoke(ctx context.Context, input map[string]any) (tool.Result, error) {
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

	pattern := args.Pattern
	if args.Multiline != nil && *args.Multiline {
		pattern = "(?s)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return tool.Result{}, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("invalid regex %q: %v", args.Pattern, err),
		}
	}

	abs := resolvePath(g.cwd, args.Path)
	info, statErr := os.Stat(abs)
	if statErr != nil {
		return tool.Result{}, tool.MapIOError(statErr)
	}

	mode := args.OutputMode
	if mode == "" {
		mode = "content"
	}
	before := defaultInt(args.ContextBefore, 0)
	after := defaultInt(args.ContextAfter, 0)
	if before > maxGrepContextLines {
		before = maxGrepContextLines
	}
	if after > maxGrepContextLines {
		after = maxGrepContextLines
	}

	files, walkErr := g.listFiles(ctx, abs, info.IsDir())
	if walkErr != nil {
		return tool.Result{}, walkErr
	}

	hits, totalMatches, truncated, oversize, err := g.scanFiles(ctx, files, abs, re, mode, before, after)
	if err != nil {
		return tool.Result{}, err
	}

	return tool.Result{
		Content:         buildGrepContent(args.Path, mode, hits, totalMatches, truncated, oversize),
		Display:         buildGrepDisplay(args.Path, mode, len(hits), totalMatches, truncated),
		ForcedTruncated: truncated,
	}, nil
}

// ============================================================
// argument decoding & validation
// ============================================================

func (g *Grep) decodeArgs(input map[string]any) (*GrepArgs, error) {
	if input == nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: "input is nil"}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: fmt.Sprintf("re-marshal input: %v", err)}
	}
	var out GrepArgs
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: err.Error()}
	}
	return &out, nil
}

func (g *Grep) validateArgs(a *GrepArgs) error {
	if strings.TrimSpace(a.Pattern) == "" {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "pattern is required"}
	}
	if strings.TrimSpace(a.Path) == "" {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "path is required"}
	}
	switch a.OutputMode {
	case "", "content", "files_with_matches", "count":
		// ok
	default:
		return &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("output_mode must be one of content/files_with_matches/count, got %q", a.OutputMode),
		}
	}
	if a.ContextBefore != nil && *a.ContextBefore < 0 {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "context_before must be >= 0"}
	}
	if a.ContextAfter != nil && *a.ContextAfter < 0 {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "context_after must be >= 0"}
	}
	return nil
}

func defaultInt(p *int, fallback int) int {
	if p == nil {
		return fallback
	}
	return *p
}

// ============================================================
// listFiles: produce the (relative-to-root) list of files to scan
// ============================================================

func (g *Grep) listFiles(ctx context.Context, root string, isDir bool) ([]string, error) {
	if !isDir {
		return []string{root}, nil
	}
	out := make([]string, 0, 64)
	walkErr := filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		if path == root {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if matchIgnore(defaultGrepIgnore, rel, de.IsDir()) {
			if de.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if de.IsDir() {
			return nil
		}
		// Only walk regular files; skip symlinks to avoid loops.
		if de.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		out = append(out, path)
		if len(out) >= maxGrepFiles {
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, tool.MapCtxError(ctxErr)
		}
		return nil, tool.MapIOError(walkErr)
	}
	sort.Strings(out)
	return out, nil
}

// matchIgnore is the same algorithm as fs.matchIgnore but lives here
// to avoid an inter-tool import. See fs/listdir.go for full docs.
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
			if ok, _ := doublestar.Match(pp, base); ok {
				return true
			}
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
// scanFiles: regex-scan each file, honouring caps & modes
// ============================================================

// grepHit represents one match. For mode=files_with_matches we set
// only Path; for mode=count we set Path + Count; for mode=content
// we set Path + Lines (the line text and any context lines).
type grepHit struct {
	Path  string       // path relative to scan root (or absolute when single-file)
	Count int          // populated in count mode
	Lines []grepLine   // populated in content mode
}

// grepLine is a single line in a content-mode hit. ContextOnly=true
// indicates this line is from -B/-A padding rather than itself a match.
type grepLine struct {
	LineNo      int
	Text        string
	ContextOnly bool
}

// scanFiles iterates the file list, scans each, and returns aggregated
// hits + total match count + truncation flag + count of oversize-skipped
// files (used in the warning).
func (g *Grep) scanFiles(
	ctx context.Context, files []string, root string, re *regexp.Regexp,
	mode string, ctxBefore, ctxAfter int,
) (hits []grepHit, totalMatches int, truncated bool, oversize int, err error) {
	hits = make([]grepHit, 0, 32)

	for _, full := range files {
		if err := ctx.Err(); err != nil {
			return nil, 0, false, 0, tool.MapCtxError(err)
		}
		st, statErr := os.Stat(full)
		if statErr != nil {
			continue // ignore unreadable file mid-walk
		}
		if st.Size() > maxGrepFileBytes {
			oversize++
			continue
		}
		rel := full
		if root != full {
			if r, e := filepath.Rel(root, full); e == nil {
				rel = filepath.ToSlash(r)
			}
		}
		hit, matches, scanErr := scanOneFile(full, rel, re, mode, ctxBefore, ctxAfter)
		if scanErr != nil {
			continue
		}
		if matches == 0 {
			continue
		}
		hits = append(hits, hit)
		totalMatches += matches
		if totalMatches >= maxGrepMatches {
			truncated = true
			break
		}
	}
	return hits, totalMatches, truncated, oversize, nil
}

// scanOneFile reads one file fully and returns its grepHit (zero
// matches → returns hit with Count=0; caller should drop it). The
// approach is deliberately simple: read the whole file (already
// capped at 10 MB), split into lines once, match line-by-line.
//
// Note on multiline regex: when the pattern uses (?s) we still match
// per-line because line-by-line is what 'rg' does. For genuine
// cross-line patterns the user can preprocess (e.g. use \n in pattern)
// — full multiline semantics is out of scope for v1.
func scanOneFile(
	full, rel string, re *regexp.Regexp, mode string, ctxBefore, ctxAfter int,
) (grepHit, int, error) {
	f, err := os.Open(full)
	if err != nil {
		return grepHit{}, 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024) // up to 4 MB per line

	var (
		all      []string
		matchIdx []int // 1-based line numbers of matches
	)
	for scanner.Scan() {
		all = append(all, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return grepHit{}, 0, err
	}

	for i, line := range all {
		if re.MatchString(line) {
			matchIdx = append(matchIdx, i+1)
		}
	}

	hit := grepHit{Path: rel}
	if len(matchIdx) == 0 {
		return hit, 0, nil
	}

	switch mode {
	case "files_with_matches":
		// Just present the file once.
		return hit, 1, nil

	case "count":
		hit.Count = len(matchIdx)
		return hit, len(matchIdx), nil

	case "content", "":
		hit.Lines = collectContentLines(all, matchIdx, ctxBefore, ctxAfter)
		return hit, len(matchIdx), nil
	}
	return hit, 0, nil
}

// collectContentLines builds the per-file line set for content mode,
// merging overlapping context windows so a tight cluster of hits
// doesn't produce duplicated padding.
func collectContentLines(all []string, matchIdx []int, ctxBefore, ctxAfter int) []grepLine {
	matchSet := make(map[int]bool, len(matchIdx))
	for _, m := range matchIdx {
		matchSet[m] = true
	}
	keep := make(map[int]bool, len(matchIdx)*(ctxBefore+ctxAfter+1))
	for _, m := range matchIdx {
		for i := m - ctxBefore; i <= m+ctxAfter; i++ {
			if i >= 1 && i <= len(all) {
				keep[i] = true
			}
		}
	}
	keys := make([]int, 0, len(keep))
	for k := range keep {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	out := make([]grepLine, 0, len(keys))
	for _, k := range keys {
		out = append(out, grepLine{
			LineNo:      k,
			Text:        all[k-1],
			ContextOnly: !matchSet[k],
		})
	}
	return out
}

// ============================================================
// content / display
// ============================================================

// buildGrepContent renders the LLM-facing payload. Format mirrors
// ripgrep's familiar output: "<path>:<line>:<text>" for matches,
// "<path>-<line>-<text>" for context lines (the dash distinguishes).
// Wrapped in <grep> ... </grep> per D73.
func buildGrepContent(path, mode string, hits []grepHit, totalMatches int, truncated bool, oversize int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<grep path=%q mode=%q files=%d matches=%d>\n", path, mode, len(hits), totalMatches)

	switch mode {
	case "files_with_matches":
		for _, h := range hits {
			fmt.Fprintf(&b, "%s\n", h.Path)
		}
	case "count":
		for _, h := range hits {
			fmt.Fprintf(&b, "%s:%d\n", h.Path, h.Count)
		}
	default: // "content"
		for _, h := range hits {
			for _, ln := range h.Lines {
				sep := ":"
				if ln.ContextOnly {
					sep = "-"
				}
				fmt.Fprintf(&b, "%s%s%d%s%s\n", h.Path, sep, ln.LineNo, sep, ln.Text)
			}
		}
	}
	b.WriteString("</grep>")
	if truncated {
		fmt.Fprintf(&b, "\n[warning: truncated at %d total matches; refine pattern or path]", maxGrepMatches)
	}
	if oversize > 0 {
		fmt.Fprintf(&b, "\n[warning: %d file(s) skipped because they exceed %d bytes]", oversize, maxGrepFileBytes)
	}
	return b.String()
}

// buildGrepDisplay renders the one-line UI summary per D74:
//
//	grep <path> (<n> files, <m> matches) → ok [truncated]?
func buildGrepDisplay(path, mode string, files, matches int, truncated bool) string {
	suffix := ""
	if truncated {
		suffix = " [truncated]"
	}
	switch mode {
	case "files_with_matches":
		return fmt.Sprintf("grep %s (%d file%s) → ok%s", path, files, pluralS(files), suffix)
	default:
		return fmt.Sprintf("grep %s (%d file%s, %d match%s) → ok%s",
			path, files, pluralS(files), matches, pluralES(matches), suffix)
	}
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func pluralES(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}
