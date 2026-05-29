// Package fs hosts filesystem-domain tools (read_file is the first;
// write_file / edit_file / delete_file / list_dir follow in T2.x).
//
// The implementation strictly follows the template in
// docs/system-design/10-tool-template-and-readfile.md (R7-1'). Anyone
// adding a new fs tool should:
//   1. Define a typed <Name>Args struct with json + jsonschema tags.
//   2. Implement Tool.Name / Description (long-form English, when-to-use)
//      / Schema (reflected) / Category / Invoke.
//   3. Add a private decodeArgs + validateArgs pair (D78).
//   4. Embed testkit.ToolTestSuite and add tool-specific cases.
//   5. Refresh testdata/<name>.schema.golden.json via
//      `make update-tool-goldens`.

package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/invopop/jsonschema"

	"github.com/HBulgat/mini-agent/internal/tool"
)

// Default values and hard caps for read_file. These match D79 verbatim;
// changing any constant requires a doc update.
const (
	defaultOffset    = 1
	defaultLimit     = 200
	defaultMaxBytes  = 200_000
	hardCapMaxBytes  = 1_048_576
	binaryProbeBytes = 8 * 1024
	lineNumberWidth  = 6
)

// ReadFileArgs is the typed input for read_file. Field tags double as
// schema annotations (jsonschema package reflects them). Order matters
// in the generated schema's "properties" map (the package preserves
// struct declaration order), so put required-first to keep the LLM
// prompt natural.
//
// `description` follows the rule "include semantics + units + default".
type ReadFileArgs struct {
	// Path is required: absolute or workspace-relative path to a text
	// file. Empty string is rejected by validateArgs.
	Path string `json:"path" jsonschema:"required,description=Absolute or relative path to a text file"`

	// Offset is the 1-based starting line. 0 (or omitted) = 1.
	Offset int `json:"offset,omitempty" jsonschema:"description=Starting line (1-based); default: 1"`

	// Limit caps the number of lines returned. 0 (or omitted) = 200.
	Limit int `json:"limit,omitempty" jsonschema:"description=Max lines to read from offset; default: 200"`

	// MaxBytes is the byte cap on the joined output. 0 = 200000;
	// values above 1048576 are silently clamped down.
	MaxBytes int `json:"max_bytes,omitempty" jsonschema:"description=Hard cap on returned bytes; default: 200000 (200 KB) max: 1048576 (1 MB)"`
}

// CwdProvider is a tiny callback so read_file can resolve relative
// paths without depending on the session package directly. The agent
// loop (or bootstrap code) wires it to session.Cwd() at construction.
//
// The function is invoked on every Invoke call so /cwd switches in the
// REPL take effect for subsequent reads (D? — D9 by extension; the
// agent loop swaps cwd live).
type CwdProvider func() string

// ReadFile is the canonical "load text file content" tool. The struct
// holds only its dependency wiring; per-call state lives on the stack.
type ReadFile struct {
	cwd CwdProvider
}

// NewReadFile wires a ReadFile to a cwd provider. If cwd is nil, the
// tool falls back to os.Getwd() — convenient for tests but not what
// the real agent loop should pass (it should always inject session.Cwd).
func NewReadFile(cwd CwdProvider) *ReadFile {
	if cwd == nil {
		cwd = func() string {
			wd, err := os.Getwd()
			if err != nil {
				return ""
			}
			return wd
		}
	}
	return &ReadFile{cwd: cwd}
}

// Name is the tool_use identifier the LLM sees. Stable across versions.
func (r *ReadFile) Name() string { return "read_file" }

// Description is shown in the system prompt's tool catalog. Long-form
// per D71: when to use, when NOT to use, semantics notes. We keep it
// in a raw string and TrimSpace so the source layout is readable.
func (r *ReadFile) Description() string {
	return strings.TrimSpace(`
Read text file content from the local filesystem.

When to use:
- Inspect source code, configuration files, or documentation.
- Verify file content before/after modification.
- Read a known portion of a large file via offset+limit.

When NOT to use:
- Reading binary files (use bash with file/xxd or a dedicated tool).
- Listing directory contents (use list_dir).
- Searching across many files (use grep).

Notes:
- Output includes a 1-based line number prefix in "%6d:" format on every line.
- Default reads the first 200 lines; pass offset/limit to paginate.
- Hard byte cap is 1 MB (max_bytes); larger values are silently clamped.
- Binary files (NUL byte in first 8 KB) are rejected with ErrInvalidArgs.
`)
}

// Schema reflects ReadFileArgs into a JSON Schema map. We build a
// fresh Reflector each call so concurrent registries don't share
// mutable Reflector state — the cost is microseconds and only paid
// once per request.
func (r *ReadFile) Schema() map[string]any {
	refl := &jsonschema.Reflector{
		ExpandedStruct:             true, // inline the struct, no $ref/$defs
		RequiredFromJSONSchemaTags: true, // honour `jsonschema:"required"`
		DoNotReference:             true, // no $defs at any depth
		Anonymous:                  true, // no auto-generated $id
		AllowAdditionalProperties:  false,
	}
	schema := refl.Reflect(&ReadFileArgs{})
	return schemaToMap(schema)
}

// Category: read-only IO; always allowed in every mode.
func (r *ReadFile) Category() tool.Category { return tool.CategoryReadOnly }

// Invoke is the entry point called by the agent loop after permission
// check has passed. Steps follow §10.3.2 verbatim.
func (r *ReadFile) Invoke(ctx context.Context, input map[string]any) (tool.Result, error) {
	args, err := r.decodeArgs(input)
	if err != nil {
		return tool.Result{}, err
	}
	if err := r.validateArgs(args); err != nil {
		return tool.Result{}, err
	}

	abs := r.resolvePath(args.Path)

	// Binary detection runs before the full read so we reject early
	// without paying for a multi-MB IO on a wrong-mime file.
	binary, err := probeBinary(abs)
	if err != nil {
		return tool.Result{}, tool.MapIOError(err)
	}
	if binary {
		return tool.Result{}, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("file %q appears to be binary (NUL byte in first %d bytes); use bash with appropriate tools (e.g., file/xxd) instead", abs, binaryProbeBytes),
		}
	}

	// Honour ctx cancellation between IO steps. read_file's IOs are
	// short enough that we don't need to interleave a select inside
	// os.ReadFile; checking before/after each major step (§10.2 R5)
	// is sufficient.
	if err := ctx.Err(); err != nil {
		return tool.Result{}, tool.MapCtxError(err)
	}

	raw, err := os.ReadFile(abs)
	if err != nil {
		return tool.Result{}, tool.MapIOError(err)
	}

	if err := ctx.Err(); err != nil {
		return tool.Result{}, tool.MapCtxError(err)
	}

	lines := splitLines(raw)
	totalLines := len(lines)

	offset := args.Offset
	if offset == 0 {
		offset = defaultOffset
	}
	limit := args.Limit
	if limit == 0 {
		limit = defaultLimit
	}
	maxBytes := args.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxBytes
	}
	if maxBytes > hardCapMaxBytes {
		maxBytes = hardCapMaxBytes
	}

	// UserLimited fires when the LLM/user *consciously* narrowed the
	// view: a non-default offset, or a limit smaller than the file's
	// actual line count. We compute it on the original (pre-clamped)
	// args so passing limit=10 for a 50-line file still counts.
	userLimited := args.Offset > 1 || (args.Limit > 0 && args.Limit < totalLines)

	// start is a 0-based slice index. If offset > totalLines we want
	// an empty selection (no panic); clamp to totalLines.
	start := offset - 1
	if start > totalLines {
		start = totalLines
	}
	end := start + limit
	if end > totalLines {
		end = totalLines
	}
	selected := lines[start:end]

	body, byteTruncated := joinWithLineNumbers(selected, offset, maxBytes)

	// ForcedTruncated: tool's own cap clipped output. Two ways:
	//   1. Byte cap hit (byteTruncated).
	//   2. The default 200-line limit dropped tail lines without the
	//      user explicitly asking for fewer (so userLimited == false).
	forcedTruncated := byteTruncated || (end < totalLines && !userLimited)

	content := buildContent(abs, body, offset, end, totalLines, byteTruncated, end < totalLines && !byteTruncated, maxBytes)
	display := buildDisplay(abs, totalLines, offset, end, len(raw), forcedTruncated || userLimited)

	return tool.Result{
		Content:         content,
		Display:         display,
		UserLimited:     userLimited,
		ForcedTruncated: forcedTruncated,
	}, nil
}

// ============================================================
// Private helpers (D78)
// ============================================================

// decodeArgs converts the LLM-supplied input map to ReadFileArgs with
// strict unknown-field rejection. Returning *Error here saves Invoke
// from having to wrap.
//
// Two reasons to keep it separate from validateArgs: the failure modes
// are different (JSON shape vs. semantic ranges), and the testkit
// suite calls decodeArgs implicitly via Invoke for the universal
// "junk → ErrInvalidArgs" check.
func (r *ReadFile) decodeArgs(input map[string]any) (*ReadFileArgs, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("read_file: cannot marshal input: %v", err),
			Cause:   err,
		}
	}
	var args ReadFileArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return nil, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("read_file: invalid input: %v", err),
			Cause:   err,
		}
	}
	return &args, nil
}

// validateArgs performs business-level range checks. Each branch maps
// to a clear LLM-facing message so the model can fix its call without
// retrying blindly.
func (r *ReadFile) validateArgs(a *ReadFileArgs) error {
	if strings.TrimSpace(a.Path) == "" {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "read_file: path is required"}
	}
	if a.Offset < 0 {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: fmt.Sprintf("read_file: offset must be >= 0 (got %d)", a.Offset)}
	}
	if a.Limit < 0 {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: fmt.Sprintf("read_file: limit must be >= 0 (got %d)", a.Limit)}
	}
	if a.MaxBytes < 0 {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: fmt.Sprintf("read_file: max_bytes must be >= 0 (got %d)", a.MaxBytes)}
	}
	return nil
}

// resolvePath converts a possibly-relative path to absolute via the
// shared package-level helper (defined in writefile.go) so every fs
// tool agrees on path semantics.
func (r *ReadFile) resolvePath(path string) string {
	return resolvePath(r.cwd, path)
}

// probeBinary reads the first binaryProbeBytes bytes and looks for a
// NUL byte. UTF-8 / UTF-16 BE/LE / ASCII text never contains a NUL in
// well-formed content, so this is a strong heuristic without a full
// MIME library.
//
// Returns (false, err) on IO errors (caller maps to ErrIO/ErrNotFound).
// EOF on a tiny file is not an error — short text files are fine.
func probeBinary(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, binaryProbeBytes)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}

// splitLines splits raw on '\n' preserving empty trailing lines exactly
// once: a trailing newline produces one fewer logical line (matching
// `wc -l` semantics that humans expect).
//
// We don't use bufio.Scanner because it silently strips trailing
// newlines and reorders bytes for very long lines.
func splitLines(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	s := string(raw)
	// Strip a single trailing "\n" so a 3-line file ending with \n
	// reports total_lines=3, not 4.
	if strings.HasSuffix(s, "\n") {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n")
}

// joinWithLineNumbers builds the body of the <file> block: each line
// gets a "<6-wide-right-aligned-lineno>:<line>\n" prefix. Returns
// (body, true) if the byte budget was exhausted before all selected
// lines were written.
//
// We compute the budget on the prefixed bytes (what the LLM actually
// sees) so a request of max_bytes=200 returns ~200 bytes of viewable
// content, not 200 bytes of raw payload + arbitrary prefix overhead.
func joinWithLineNumbers(lines []string, startLine, maxBytes int) (string, bool) {
	var sb strings.Builder
	sb.Grow(min(maxBytes, 64*1024))
	for i, line := range lines {
		entry := fmt.Sprintf("%*d:%s\n", lineNumberWidth, startLine+i, line)
		if sb.Len()+len(entry) > maxBytes {
			return sb.String(), true
		}
		sb.WriteString(entry)
	}
	return sb.String(), false
}

// min is the simplest possible helper; Go 1.21+ has it as a builtin
// but we keep the explicit one in case the module's GOTOOLCHAIN drops
// to 1.20 in a future bisect.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// buildContent assembles the structured <file ...> wrapper around the
// already-line-numbered body. byteTrunc / lineTrunc are mutually
// exclusive in practice (byte truncation also drops lines), so we
// emit at most one warning line.
//
// rangeStart / rangeEnd are 1-based inclusive (e.g. lines 1-10).
func buildContent(absPath, body string, rangeStart, rangeEnd, total int, byteTrunc, lineTrunc bool, maxBytes int) string {
	displayedLines := rangeEnd - rangeStart + 1
	if rangeEnd < rangeStart {
		// edge case: empty selection (e.g. offset > total_lines)
		displayedLines = 0
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(
		`<file path=%q lines=%d total_lines=%d range="%d-%d" bytes=%d>`+"\n",
		absPath,
		displayedLines,
		total,
		rangeStart, rangeEnd,
		len(body),
	))
	sb.WriteString(body)
	sb.WriteString("</file>\n")
	switch {
	case byteTrunc:
		fmt.Fprintf(&sb, "[warning: byte limit reached at %d; content truncated. Pass max_bytes or narrow range.]\n", maxBytes)
	case lineTrunc:
		fmt.Fprintf(&sb, "[warning: showing %d of %d lines. Use offset/limit to read the rest.]\n", displayedLines, total)
	}
	return sb.String()
}

// buildDisplay produces the user-facing one-liner per D74 / D81. We
// surface the raw byte count (1.2 KB style) and total line count so
// the user can quickly judge whether the result is a tiny config or
// a chunk of a giant log.
func buildDisplay(absPath string, total, rangeStart, rangeEnd, fileBytes int, truncated bool) string {
	name := filepath.Base(absPath)
	sizeStr := humanizeBytes(fileBytes)
	suffix := ""
	if truncated {
		suffix = " [truncated]"
	}
	if rangeEnd < rangeStart {
		return fmt.Sprintf("read_file %s (%s, %d lines) → empty range%s", name, sizeStr, total, suffix)
	}
	return fmt.Sprintf("read_file %s (%s, %d lines) → showing %d-%d%s",
		name, sizeStr, total, rangeStart, rangeEnd, suffix)
}

// humanizeBytes is a tiny, allocation-free formatter. We don't use
// docker-style "GiB" because the typical reader is a developer's text
// file and the simpler "12.3 KB" form is more readable.
func humanizeBytes(n int) string {
	const (
		_ = 1 << (10 * iota)
		kib
		mib
		gib
	)
	switch {
	case n < kib:
		return fmt.Sprintf("%d B", n)
	case n < mib:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kib))
	case n < gib:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mib))
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(gib))
	}
}

// schemaToMap round-trips an *jsonschema.Schema into the canonical
// map[string]any shape Tool.Schema returns. The library's *Schema is
// already JSON-friendly via Marshal so this is one round-trip and a
// dynamic typing.
//
// Returning nil on a marshalling failure is intentional: the registry's
// startup validator (D84) will reject the tool, surfacing the bug at
// boot rather than at request time.
func schemaToMap(s *jsonschema.Schema) map[string]any {
	if s == nil {
		return nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}
