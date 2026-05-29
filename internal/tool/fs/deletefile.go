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

// DeleteFileArgs is the typed input for delete_file. Intentionally
// minimal — directory deletion goes through `bash` because that's
// where the user's hard-blacklist + permission gate has the right
// affordances (ask once, see what's about to disappear).
type DeleteFileArgs struct {
	// Path is required: absolute or workspace-relative path to a
	// regular file (not a directory).
	Path string `json:"path" jsonschema:"required,description=Absolute or relative path to the file to delete"`
}

// DeleteFile is the "remove a single regular file" tool. Refuses to
// touch directories and refuses if the target doesn't exist (so the
// LLM gets a clean signal instead of a silent no-op).
type DeleteFile struct {
	cwd CwdProvider
}

func NewDeleteFile(cwd CwdProvider) *DeleteFile {
	if cwd == nil {
		cwd = func() string {
			wd, err := os.Getwd()
			if err != nil {
				return ""
			}
			return wd
		}
	}
	return &DeleteFile{cwd: cwd}
}

func (d *DeleteFile) Name() string { return "delete_file" }

func (d *DeleteFile) Description() string {
	return strings.TrimSpace(`
Delete a single regular file from the filesystem.

When to use:
- Remove a stale file you have positively identified (e.g. an obsolete
  test fixture, a generated artifact, a temporary file).

When NOT to use:
- Removing a directory or a directory tree (use bash with rm -rf, scoped
  through the permission gate).
- Bulk deletion (call delete_file once per file; the agent loop's parallel
  bucketing will run safe ones together).
- "Cleaning up" without a known target — read_file / list_dir first.

Notes:
- Path must point to a regular file. Directories return ErrInvalidArgs.
- Missing files return ErrNotFound (no silent no-op).
- The permission gate applies the path-level rules and the hard blacklist
  before this tool runs; the tool itself does not re-check.
- Deletion is non-recoverable from the agent's side. The CLI may surface
  an "are you sure?" approval depending on mode.
`)
}

func (d *DeleteFile) Schema() map[string]any {
	refl := &jsonschema.Reflector{
		ExpandedStruct:             true,
		RequiredFromJSONSchemaTags: true,
		DoNotReference:             true,
		Anonymous:                  true,
		AllowAdditionalProperties:  false,
	}
	return schemaToMap(refl.Reflect(&DeleteFileArgs{}))
}

func (d *DeleteFile) Category() tool.Category { return tool.CategoryWrite }

// Invoke removes the file. Steps:
//  1. decode + validate
//  2. resolve path; refuse on directory / missing
//  3. os.Remove
func (d *DeleteFile) Invoke(ctx context.Context, input map[string]any) (tool.Result, error) {
	args, err := d.decodeArgs(input)
	if err != nil {
		return tool.Result{}, err
	}
	if err := d.validateArgs(args); err != nil {
		return tool.Result{}, err
	}

	abs := resolvePath(d.cwd, args.Path)

	st, err := os.Stat(abs)
	if err != nil {
		return tool.Result{}, tool.MapIOError(err)
	}
	if st.IsDir() {
		return tool.Result{}, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("delete_file: %q is a directory; use bash with rm -rf for directory removal", abs),
		}
	}
	if !st.Mode().IsRegular() {
		return tool.Result{}, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("delete_file: %q is not a regular file (mode=%v); refusing to delete", abs, st.Mode()),
		}
	}

	if err := ctx.Err(); err != nil {
		return tool.Result{}, tool.MapCtxError(err)
	}

	size := st.Size()
	if err := os.Remove(abs); err != nil {
		return tool.Result{}, tool.MapIOError(err)
	}

	display := fmt.Sprintf("delete_file %s (%s) → removed",
		filepath.Base(abs), humanizeBytes(int(size)))
	content := fmt.Sprintf(
		`<delete_file path=%q bytes=%d>removed %s (%d bytes)</delete_file>`,
		abs, size, abs, size,
	) + "\n"

	return tool.Result{
		Content: content,
		Display: display,
	}, nil
}

// ============================================================
// Private helpers (D78)
// ============================================================

func (d *DeleteFile) decodeArgs(input map[string]any) (*DeleteFileArgs, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("delete_file: cannot marshal input: %v", err),
			Cause:   err,
		}
	}
	var args DeleteFileArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return nil, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("delete_file: invalid input: %v", err),
			Cause:   err,
		}
	}
	return &args, nil
}

func (d *DeleteFile) validateArgs(a *DeleteFileArgs) error {
	if strings.TrimSpace(a.Path) == "" {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "delete_file: path is required"}
	}
	return nil
}
