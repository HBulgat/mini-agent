package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/invopop/jsonschema"

	"github.com/HBulgat/mini-agent/internal/tool"
)

// ----------------------------------------------------------------
// write_file constants
// ----------------------------------------------------------------

const (
	// Default file mode for newly-created files. 0644 is the
	// canonical "owner-write, world-read" choice that matches
	// `touch` / `cat >` behaviour and respects the user's umask.
	writeFileMode os.FileMode = 0o644

	// Default directory mode for parents created via mkdir_parents.
	// 0755 matches `mkdir -p` defaults.
	writeDirMode os.FileMode = 0o755

	// Hard cap on a single write_file call (1 MB). The LLM should
	// never need to ship more than this in one shot; larger payloads
	// should be assembled by the host process before calling.
	writeFileMaxBytes = 1_048_576
)

// WriteFileArgs is the typed input for write_file. mkdir_parents has
// a tri-state shape (`*bool`) because the JSON-Schema-aware default
// is true but Go's zero value is false; the pointer lets us tell
// "user passed false" apart from "user omitted the field".
type WriteFileArgs struct {
	// Path is required: absolute or workspace-relative path. The
	// parent directory is auto-created when MkdirParents is true.
	Path string `json:"path" jsonschema:"required,description=Absolute or relative path to the file to write"`

	// Content is the full new file content. Empty string is allowed
	// (creates an empty file). UTF-8 only — binary writes go through
	// bash.
	Content string `json:"content" jsonschema:"required,description=Full file content to write (UTF-8 text only; max 1 MB)"`

	// MkdirParents controls whether missing parent directories get
	// created automatically. Default: true. Pass false to fail with
	// ErrNotFound when the parent doesn't exist (the LLM will then
	// know to call write_file again with a different path or to use
	// bash for `mkdir -p`).
	MkdirParents *bool `json:"mkdir_parents,omitempty" jsonschema:"description=Auto-create missing parent directories; default: true"`
}

// WriteFile is the canonical "create or overwrite a file" tool.
type WriteFile struct {
	cwd CwdProvider
}

// NewWriteFile wires a WriteFile to a cwd provider. Same fallback as
// NewReadFile (os.Getwd) so tests can pass nil.
func NewWriteFile(cwd CwdProvider) *WriteFile {
	if cwd == nil {
		cwd = func() string {
			wd, err := os.Getwd()
			if err != nil {
				return ""
			}
			return wd
		}
	}
	return &WriteFile{cwd: cwd}
}

func (w *WriteFile) Name() string { return "write_file" }

func (w *WriteFile) Description() string {
	return strings.TrimSpace(`
Create or overwrite a text file with the supplied content.

When to use:
- Creating a new file from scratch (config, README, source file).
- Replacing the entire content of an existing file.
- Following a successful read_file when you intend to rewrite the whole file.

When NOT to use:
- Making a small edit to an existing file (use edit_file with old_str/new_str).
- Writing binary data (use bash with appropriate redirection).
- Creating directories (this tool writes files only; pass mkdir_parents=true
  to auto-create parent dirs, or use bash for mkdir).

Notes:
- Existing file is overwritten without prompt; the agent loop's permission
  gate is responsible for confirmation.
- Write is atomic: content is staged in a sibling temp file then renamed,
  so a crashed write never leaves a half-written file in place.
- Content is fsync'd before rename to survive OS crashes.
- Hard cap: 1 MB per call.
`)
}

func (w *WriteFile) Schema() map[string]any {
	refl := &jsonschema.Reflector{
		ExpandedStruct:             true,
		RequiredFromJSONSchemaTags: true,
		DoNotReference:             true,
		Anonymous:                  true,
		AllowAdditionalProperties:  false,
	}
	return schemaToMap(refl.Reflect(&WriteFileArgs{}))
}

// Category: write — gated behind permission.Gate.
func (w *WriteFile) Category() tool.Category { return tool.CategoryWrite }

// Invoke executes the write. Steps:
//  1. decode + validate
//  2. resolve path against cwd
//  3. mkdir_parents if requested + dir missing
//  4. atomic write via temp file + rename + fsync
func (w *WriteFile) Invoke(ctx context.Context, input map[string]any) (tool.Result, error) {
	args, err := w.decodeArgs(input)
	if err != nil {
		return tool.Result{}, err
	}
	if err := w.validateArgs(args); err != nil {
		return tool.Result{}, err
	}

	abs := resolvePath(w.cwd, args.Path)

	// mkdir_parents default: true (mirrors the YAML/jsonschema doc).
	mkdir := true
	if args.MkdirParents != nil {
		mkdir = *args.MkdirParents
	}

	parent := filepath.Dir(abs)
	if mkdir {
		if err := os.MkdirAll(parent, writeDirMode); err != nil {
			return tool.Result{}, tool.MapIOError(err)
		}
	} else {
		// Confirm parent exists; otherwise return ErrNotFound so the
		// model gets a clean signal (instead of an opaque "create
		// file: ...") and can decide to either retry with mkdir or
		// pick a different path.
		if st, err := os.Stat(parent); err != nil {
			return tool.Result{}, tool.MapIOError(err)
		} else if !st.IsDir() {
			return tool.Result{}, &tool.Error{
				Code:    tool.ErrInvalidArgs,
				Message: fmt.Sprintf("write_file: parent %q is not a directory", parent),
			}
		}
	}

	// Pre-honour ctx so a Ctrl+C between resolution and IO short-
	// circuits cleanly.
	if err := ctx.Err(); err != nil {
		return tool.Result{}, tool.MapCtxError(err)
	}

	// Atomic write: stage in a sibling temp file, fsync, rename. The
	// temp file is in the same directory so rename is guaranteed to
	// be atomic on the same filesystem.
	created, err := atomicWrite(abs, []byte(args.Content))
	if err != nil {
		return tool.Result{}, tool.MapIOError(err)
	}

	display := fmt.Sprintf("write_file %s (%s)",
		filepath.Base(abs), humanizeBytes(len(args.Content)))
	if created {
		display += " → created"
	} else {
		display += " → overwritten"
	}

	verb := "Overwrote"
	if created {
		verb = "Created"
	}
	content := fmt.Sprintf(
		`<write_file path=%q bytes=%d created=%t>%s %s with %d bytes</write_file>`,
		abs, len(args.Content), created, verb, abs, len(args.Content),
	) + "\n"

	return tool.Result{
		Content: content,
		Display: display,
	}, nil
}

// ============================================================
// Private helpers (D78)
// ============================================================

func (w *WriteFile) decodeArgs(input map[string]any) (*WriteFileArgs, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("write_file: cannot marshal input: %v", err),
			Cause:   err,
		}
	}
	var args WriteFileArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return nil, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("write_file: invalid input: %v", err),
			Cause:   err,
		}
	}
	return &args, nil
}

func (w *WriteFile) validateArgs(a *WriteFileArgs) error {
	if strings.TrimSpace(a.Path) == "" {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "write_file: path is required"}
	}
	if len(a.Content) > writeFileMaxBytes {
		return &tool.Error{
			Code: tool.ErrTooLarge,
			Message: fmt.Sprintf("write_file: content is %d bytes (max %d); split the work into multiple calls or use bash",
				len(a.Content), writeFileMaxBytes),
		}
	}
	return nil
}

// ============================================================
// Shared filesystem helpers (used by edit_file / delete_file too)
// ============================================================

// resolvePath converts a possibly-relative path to absolute using the
// injected cwd provider. Shared by all write tools so they treat
// paths identically.
func resolvePath(cwd CwdProvider, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	wd := ""
	if cwd != nil {
		wd = cwd()
	}
	if wd == "" {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(wd, path))
}

// atomicWrite stages content in a sibling temp file, fsyncs, and
// renames over the target. Returns (created, err) where created is
// true iff the destination didn't exist before the call.
//
// On rename failure the temp file is best-effort cleaned up so the
// directory doesn't accumulate leftover *.tmp-* files.
func atomicWrite(target string, content []byte) (bool, error) {
	_, statErr := os.Stat(target)
	created := errors.Is(statErr, os.ErrNotExist)

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".mini-agent-write-*.tmp")
	if err != nil {
		return created, fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	// Best-effort cleanup if anything below fails before rename.
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		cleanup()
		return created, fmt.Errorf("write temp file: %w", err)
	}
	// fsync before rename so a power loss after rename can't leave
	// us with an empty/short file.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return created, fmt.Errorf("fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return created, fmt.Errorf("close temp file: %w", err)
	}
	// Match user expectation: 0644 on the destination, regardless
	// of whatever the temp file inherited from CreateTemp's 0600.
	if err := os.Chmod(tmpName, writeFileMode); err != nil {
		cleanup()
		return created, fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		cleanup()
		return created, fmt.Errorf("rename temp file to target: %w", err)
	}
	return created, nil
}
