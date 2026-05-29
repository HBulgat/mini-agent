package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/invopop/jsonschema"

	"github.com/HBulgat/mini-agent/internal/tool"
)

// ----------------------------------------------------------------
// edit_file constants
// ----------------------------------------------------------------

const (
	// editFileMaxBytes caps the total file size edit_file will load
	// into memory. Larger files should be edited via multiple
	// targeted reads + writes. We're conservative because the LLM
	// often passes verbose old_str/new_str pairs and the substitution
	// step doubles the working set briefly.
	editFileMaxBytes = 2 * 1024 * 1024

	// ambiguousPreviewCount is how many matching line numbers we
	// surface when old_str hits more than `expected_occurrences`
	// places. Bigger numbers crowd the LLM's context; 3 is enough
	// to disambiguate via "look for the one near line X".
	ambiguousPreviewCount = 3
)

// EditFileArgs is the typed input for edit_file. The
// expected_occurrences pointer follows the same tri-state pattern as
// WriteFileArgs.MkdirParents — 0 (zero value) is a valid intent (which
// means "match exactly zero times — refuse to edit if found at all"),
// so we need a *int to distinguish it from "field omitted".
type EditFileArgs struct {
	// Path is required: absolute or workspace-relative path to an
	// EXISTING file. edit_file refuses to create new files (use
	// write_file for that).
	Path string `json:"path" jsonschema:"required,description=Absolute or relative path to an existing text file"`

	// OldStr is the literal substring to replace. Must not be empty
	// (an empty old_str would mean "replace nothing", which is
	// useless). Matches are case-sensitive and exact (no regex).
	OldStr string `json:"old_str" jsonschema:"required,description=Literal substring to find (must be unique unless expected_occurrences is set)"`

	// NewStr is the literal replacement. Empty string is allowed
	// (effectively a deletion of OldStr).
	NewStr string `json:"new_str" jsonschema:"required,description=Literal replacement text (empty string deletes OldStr)"`

	// ExpectedOccurrences pins how many times OldStr should match.
	// Default: 1 (single, unique edit). Set to N>1 for "global
	// replace, expecting exactly N hits". A mismatch yields
	// ErrAmbiguous so the LLM can adjust old_str instead of doing
	// the wrong thing.
	ExpectedOccurrences *int `json:"expected_occurrences,omitempty" jsonschema:"description=Required exact match count; default: 1"`
}

// EditFile is the targeted in-place editor. Use it for every change
// short of "rewrite the whole file" — the strict match-count contract
// makes it dramatically safer than blind sed-style replaces.
type EditFile struct {
	cwd CwdProvider
}

func NewEditFile(cwd CwdProvider) *EditFile {
	if cwd == nil {
		cwd = func() string {
			wd, err := os.Getwd()
			if err != nil {
				return ""
			}
			return wd
		}
	}
	return &EditFile{cwd: cwd}
}

func (e *EditFile) Name() string { return "edit_file" }

func (e *EditFile) Description() string {
	return strings.TrimSpace(`
Replace a literal substring inside an existing text file.

When to use:
- Make a small, targeted change to a known location in a file.
- Refactor: rename a symbol, fix a typo, change a constant.
- Apply a fix after read_file revealed the exact wording you need to change.

When NOT to use:
- Creating a new file (use write_file).
- Rewriting more than ~50% of a file (use write_file with the new content).
- Editing binary files (unsupported).
- Pattern matching (no regex; old_str is a literal substring).

Notes:
- Match is case-sensitive and literal — old_str must appear in the file
  *byte-for-byte* (including indentation and trailing spaces).
- By default expected_occurrences=1: old_str MUST appear exactly once.
  Pass expected_occurrences=N to allow N replacements; mismatch yields
  an "ambiguous match" error with the first 3 hit line numbers so you
  can include more surrounding context in old_str.
- File must already exist; edit_file refuses to create new files.
- File size cap: 2 MB.
`)
}

func (e *EditFile) Schema() map[string]any {
	refl := &jsonschema.Reflector{
		ExpandedStruct:             true,
		RequiredFromJSONSchemaTags: true,
		DoNotReference:             true,
		Anonymous:                  true,
		AllowAdditionalProperties:  false,
	}
	return schemaToMap(refl.Reflect(&EditFileArgs{}))
}

func (e *EditFile) Category() tool.Category { return tool.CategoryWrite }

// Invoke executes the edit. Steps:
//  1. decode + validate
//  2. resolve path; refuse if doesn't exist
//  3. read full file (with size cap)
//  4. count occurrences; reject mismatch with helpful error
//  5. perform substitution
//  6. atomic write back
func (e *EditFile) Invoke(ctx context.Context, input map[string]any) (tool.Result, error) {
	args, err := e.decodeArgs(input)
	if err != nil {
		return tool.Result{}, err
	}
	if err := e.validateArgs(args); err != nil {
		return tool.Result{}, err
	}

	abs := resolvePath(e.cwd, args.Path)

	// File must already exist (edit, not create).
	st, err := os.Stat(abs)
	if err != nil {
		return tool.Result{}, tool.MapIOError(err)
	}
	if st.IsDir() {
		return tool.Result{}, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("edit_file: %q is a directory; edit_file works on files only", abs),
		}
	}
	if st.Size() > editFileMaxBytes {
		return tool.Result{}, &tool.Error{
			Code: tool.ErrTooLarge,
			Message: fmt.Sprintf("edit_file: file %q is %d bytes (max %d); edit smaller chunks via multiple calls",
				abs, st.Size(), editFileMaxBytes),
		}
	}

	if err := ctx.Err(); err != nil {
		return tool.Result{}, tool.MapCtxError(err)
	}

	raw, err := os.ReadFile(abs)
	if err != nil {
		return tool.Result{}, tool.MapIOError(err)
	}

	// Default expected_occurrences = 1 (single, unique edit).
	expected := 1
	if args.ExpectedOccurrences != nil {
		expected = *args.ExpectedOccurrences
	}

	// Count actual occurrences.
	actual := strings.Count(string(raw), args.OldStr)

	if actual == 0 {
		return tool.Result{}, &tool.Error{
			Code:    tool.ErrNotFound,
			Message: fmt.Sprintf("edit_file: old_str not found in %q", abs),
		}
	}

	if actual != expected {
		// Find the line numbers of the first ambiguousPreviewCount
		// matches so the LLM can disambiguate by widening old_str.
		lineNums := findOccurrenceLines(raw, args.OldStr, ambiguousPreviewCount)
		return tool.Result{}, &tool.Error{
			Code: tool.ErrAmbiguous,
			Message: fmt.Sprintf(
				"edit_file: old_str matched %d places in %q (expected %d). First %d hit line numbers: %v. Widen old_str with surrounding context, or pass expected_occurrences=%d to apply globally.",
				actual, abs, expected, len(lineNums), lineNums, actual),
		}
	}

	// Perform the substitution. ReplaceAll is fine because we've
	// already verified the count matches exactly.
	newContent := strings.ReplaceAll(string(raw), args.OldStr, args.NewStr)

	if err := ctx.Err(); err != nil {
		return tool.Result{}, tool.MapCtxError(err)
	}

	if _, err := atomicWrite(abs, []byte(newContent)); err != nil {
		return tool.Result{}, tool.MapIOError(err)
	}

	bytesDelta := len(newContent) - len(raw)
	deltaSign := "+"
	if bytesDelta < 0 {
		deltaSign = "-"
		bytesDelta = -bytesDelta
	}

	display := fmt.Sprintf("edit_file %s (%d replacement%s, %s%s)",
		filepath.Base(abs),
		expected,
		pluralS(expected),
		deltaSign,
		humanizeBytes(bytesDelta),
	)
	content := fmt.Sprintf(
		`<edit_file path=%q occurrences=%d delta_bytes=%d>%d substitution(s) applied to %s</edit_file>`,
		abs, expected, len(newContent)-len(raw), expected, abs,
	) + "\n"

	return tool.Result{
		Content: content,
		Display: display,
	}, nil
}

// ============================================================
// Private helpers (D78)
// ============================================================

func (e *EditFile) decodeArgs(input map[string]any) (*EditFileArgs, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("edit_file: cannot marshal input: %v", err),
			Cause:   err,
		}
	}
	var args EditFileArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return nil, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("edit_file: invalid input: %v", err),
			Cause:   err,
		}
	}
	return &args, nil
}

func (e *EditFile) validateArgs(a *EditFileArgs) error {
	if strings.TrimSpace(a.Path) == "" {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "edit_file: path is required"}
	}
	if a.OldStr == "" {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "edit_file: old_str must not be empty"}
	}
	if a.OldStr == a.NewStr {
		return &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: "edit_file: old_str equals new_str; nothing would change",
		}
	}
	if a.ExpectedOccurrences != nil && *a.ExpectedOccurrences <= 0 {
		return &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("edit_file: expected_occurrences must be > 0 (got %d)", *a.ExpectedOccurrences),
		}
	}
	return nil
}

// findOccurrenceLines returns up to `max` 1-based line numbers where
// `needle` occurs in `raw`. Stops once `max` hits are collected so
// gigantic files don't blow the response size on the error path.
//
// We use bytes.Index in a loop and a cumulative newline counter to
// translate byte offsets to line numbers in O(N). Simpler and more
// obviously correct than fusing the two passes.
func findOccurrenceLines(raw []byte, needle string, maxHits int) []int {
	if needle == "" || len(raw) == 0 || maxHits <= 0 {
		return nil
	}
	hits := make([]int, 0, maxHits)
	needleBytes := []byte(needle)
	startOffset := 0
	for len(hits) < maxHits {
		idx := bytes.Index(raw[startOffset:], needleBytes)
		if idx < 0 {
			break
		}
		absoluteHit := startOffset + idx
		// 1-based line number = (newlines before hit) + 1.
		hits = append(hits, bytes.Count(raw[:absoluteHit], []byte{'\n'})+1)
		// Advance past this hit so overlapping matches don't double-
		// count (consistent with strings.Count semantics).
		startOffset = absoluteHit + len(needleBytes)
	}
	return hits
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
