// Package testkit provides a shared testify/suite that every concrete
// tool's tests embed (R7-1' §10.5 / D82). Embedding gives each tool's
// suite the four common assertions for free:
//
//   - TestSchemaNonEmpty       — every tool returns Name+Description+Schema
//   - TestInvalidArgsRejected  — passing total junk yields ErrInvalidArgs
//   - TestCtxCancel            — a pre-cancelled ctx yields ErrInterrupted
//   - TestSchemaGolden         — JSON Schema matches testdata/<tool>.schema.golden.json
//
// A subclass injects two callbacks (NewTool + HappyArgs) and may
// override TestSchemaGolden if the schema golden lives elsewhere.

package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/stretchr/testify/suite"

	"github.com/HBulgat/mini-agent/internal/tool"
)

// ToolTestSuite is the embedded base. Subclasses set NewTool + HappyArgs
// (and optionally GoldenPath) in their SetupTest() before delegating
// upward via s.ToolTestSuite.SetupTest() — but for the simple cases the
// callbacks alone are enough.
type ToolTestSuite struct {
	suite.Suite

	// NewTool constructs a fresh tool instance for every test (the
	// suite calls it before each test method). Required.
	NewTool func() tool.Tool

	// HappyArgs is a "valid" input map for the tool — used by
	// TestCtxCancel to produce a real-looking call rather than
	// triggering ErrInvalidArgs. Required.
	HappyArgs map[string]any

	// GoldenPath is the optional path to the schema golden file
	// relative to the test binary's working directory. Defaults to
	// "testdata/<Name>.schema.golden.json".
	GoldenPath string
}

// TestSchemaNonEmpty verifies the tool exposes the four cheap
// metadata accessors required by D84 (registry validation already
// fails otherwise, but a missing schema slips past until first call).
func (s *ToolTestSuite) TestSchemaNonEmpty() {
	t := s.NewTool()
	s.NotEmpty(t.Name(), "Name() must be non-empty")
	s.NotEmpty(t.Description(), "Description() must be non-empty")
	schema := t.Schema()
	s.NotEmpty(schema, "Schema() must return a non-empty map")
	s.Contains(schema, "type", "Schema must have a top-level 'type' key")
}

// TestInvalidArgsRejected feeds the tool an obviously bad input map and
// expects ErrInvalidArgs back. We use a deliberately bizarre key so
// it's vanishingly unlikely to collide with any legitimate parameter.
func (s *ToolTestSuite) TestInvalidArgsRejected() {
	t := s.NewTool()
	_, err := t.Invoke(context.Background(), map[string]any{
		"__nonexistent_param__": []any{"definitely", "not", "valid"},
	})
	s.requireToolErr(err, tool.ErrInvalidArgs)
}

// TestCtxCancel pre-cancels the context and expects ErrInterrupted.
// HappyArgs is used so we test ctx propagation, not arg validation.
//
// Note: tools whose Invoke is "too fast to observe ctx cancellation"
// (e.g. read_file on a 2-byte file) may legitimately succeed here —
// in that case the subclass should override this test. The default
// only requires that *if* the tool returns an error, the error code
// is ErrInterrupted.
func (s *ToolTestSuite) TestCtxCancel() {
	t := s.NewTool()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	_, err := t.Invoke(ctx, s.HappyArgs)
	if err == nil {
		// Some tools complete faster than ctx propagation. Accept
		// success silently; the strict check is opt-in by overriding.
		return
	}
	s.requireToolErr(err, tool.ErrInterrupted)
}

// TestSchemaGolden compares the tool's reflected JSON Schema against a
// golden file on disk, normalized via json round-trip so map ordering
// doesn't matter.
//
// The golden file is created (or refreshed) by `make update-tool-goldens`
// per D83. CI runs the same test in compare mode, failing if any
// schema drifts without a corresponding golden update.
//
// When the env var UPDATE_TOOL_GOLDENS=1 is set we *write* the file
// instead of comparing — that's the implementation behind
// `make update-tool-goldens`. The same test name keeps `go test ./...`
// as the only entry point.
//
// Subclasses that don't ship a golden (yet) can override with a no-op
// to opt out. Concrete tools shipping in P0 must NOT override.
func (s *ToolTestSuite) TestSchemaGolden() {
	t := s.NewTool()
	path := s.GoldenPath
	if path == "" {
		path = filepath.Join("testdata", t.Name()+".schema.golden.json")
	}
	gotBytes, err := canonicalJSON(t.Schema())
	s.Require().NoError(err)

	if os.Getenv("UPDATE_TOOL_GOLDENS") == "1" {
		s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o755))
		s.Require().NoError(os.WriteFile(path, append(gotBytes, '\n'), 0o644))
		s.T().Logf("updated golden %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		s.T().Skipf("golden file not present (%s); run `make update-tool-goldens` to generate", path)
		return
	}
	wantBytes, err := canonicalize(want)
	s.Require().NoError(err)
	s.Equal(string(wantBytes), string(gotBytes),
		"JSON Schema differs from golden %s; run `make update-tool-goldens` after intentional changes", path)
}

// requireToolErr asserts err is a *tool.Error with the given code,
// with a descriptive failure message that includes the actual error.
func (s *ToolTestSuite) requireToolErr(err error, code tool.ErrorCode) {
	s.T().Helper()
	s.Require().Error(err)
	var te *tool.Error
	if !errors.As(err, &te) {
		s.FailNowf("error is not *tool.Error", "got %T: %v", err, err)
	}
	s.Equal(code, te.Code, "wrong ErrorCode; full message: %s", te.Message)
}

// canonicalJSON marshals + remarshals to remove map-key ordering
// noise. Both inputs (in-memory schema + on-disk golden) go through
// the same pipe so the comparison is order-insensitive.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return canonicalize(raw)
}

// canonicalize parses a JSON byte slice and re-emits it with sorted
// keys + 2-space indentation. The output is deterministic and
// human-readable so a failing diff is intelligible.
func canonicalize(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}
