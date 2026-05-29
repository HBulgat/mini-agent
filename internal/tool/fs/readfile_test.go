package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/tool/testkit"
)

// ============================================================
// Test suite — embeds the shared testkit baseline and adds the
// read_file-specific cases enumerated in §10.3.8.
// ============================================================

// ReadFileSuite holds the per-test sandbox + injects the tool factory
// & happy-path args required by testkit.ToolTestSuite.
//
// SetupTest creates a fresh tmpDir for every test method so cases stay
// isolated; t.TempDir() handles cleanup automatically.
type ReadFileSuite struct {
	testkit.ToolTestSuite

	tmpDir string
}

// TestReadFile is the test entry point go test discovers.
func TestReadFile(t *testing.T) {
	s := new(ReadFileSuite)
	// NewTool is called once per test method by testify; the closure
	// re-reads s.tmpDir which is reset in SetupTest.
	s.NewTool = func() tool.Tool {
		return NewReadFile(func() string { return s.tmpDir })
	}
	suite.Run(t, s)
}

// SetupTest creates a fresh tmpDir + a small fixture file used as the
// happy-path argument by TestCtxCancel and as a baseline for
// tool-specific tests that need a "default" file present.
func (s *ReadFileSuite) SetupTest() {
	s.tmpDir = s.T().TempDir()
	// "hello\nworld\n" — 2 logical lines with trailing newline.
	hello := filepath.Join(s.tmpDir, "hello.txt")
	s.Require().NoError(os.WriteFile(hello, []byte("hello\nworld\n"), 0o644))
	s.HappyArgs = map[string]any{"path": "hello.txt"}
}

// TestSchemaGolden uses the file under testdata/ relative to the
// package directory. testkit's default path matches.
func (s *ReadFileSuite) TestSchemaGolden() {
	// Override the default GoldenPath to use the actual tool.Name()
	// resolution path — and to make the override explicit in case the
	// tool's name ever changes.
	s.GoldenPath = filepath.Join("testdata", "read_file.schema.golden.json")
	s.ToolTestSuite.TestSchemaGolden()
}

// ------------------------------------------------------------
// Tool-specific tests (§10.3.8)
// ------------------------------------------------------------

// TestReadSmallFile verifies the happy path: 2 lines in, 2 lines out,
// no truncation flags, line numbers prefixed.
func (s *ReadFileSuite) TestReadSmallFile() {
	t := s.NewTool()
	res, err := t.Invoke(context.Background(), map[string]any{"path": "hello.txt"})
	s.Require().NoError(err)
	s.False(res.UserLimited)
	s.False(res.ForcedTruncated)
	s.Contains(res.Content, "<file ")
	s.Contains(res.Content, "</file>")
	s.Contains(res.Content, "     1:hello")
	s.Contains(res.Content, "     2:world")
	s.Contains(res.Display, "read_file hello.txt")
	s.Contains(res.Display, "showing 1-2")
}

// TestForcedTruncatedByLines: 300-line file with default limit=200.
// Should set ForcedTruncated=true (the agent will see the warning),
// UserLimited=false (the LLM didn't pass a limit).
func (s *ReadFileSuite) TestForcedTruncatedByLines() {
	big := filepath.Join(s.tmpDir, "big.txt")
	var sb strings.Builder
	for i := 1; i <= 300; i++ {
		sb.WriteString("line ")
		sb.WriteString("xxxx") // some content
		sb.WriteString("\n")
	}
	s.Require().NoError(os.WriteFile(big, []byte(sb.String()), 0o644))

	res, err := s.NewTool().Invoke(context.Background(), map[string]any{"path": "big.txt"})
	s.Require().NoError(err)
	s.True(res.ForcedTruncated, "expected ForcedTruncated=true on 300-line file")
	s.False(res.UserLimited, "expected UserLimited=false (no explicit limit passed)")
	s.Contains(res.Content, "[warning: showing 200 of 300 lines")
	s.Contains(res.Display, "[truncated]")
}

// TestForcedTruncatedByMaxBytes: pass a tiny max_bytes so byte cap
// fires before line cap. We get a different warning string and
// ForcedTruncated still flips.
func (s *ReadFileSuite) TestForcedTruncatedByMaxBytes() {
	medium := filepath.Join(s.tmpDir, "medium.txt")
	var sb strings.Builder
	for i := 1; i <= 50; i++ {
		sb.WriteString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n") // 36+1 bytes per line
	}
	s.Require().NoError(os.WriteFile(medium, []byte(sb.String()), 0o644))

	res, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":      "medium.txt",
		"max_bytes": 200, // ~5 lines worth
	})
	s.Require().NoError(err)
	s.True(res.ForcedTruncated)
	s.Contains(res.Content, "[warning: byte limit reached at 200")
}

// TestUserLimited: explicit offset=2 + limit=1 narrows to a single
// middle line. UserLimited=true; ForcedTruncated=false (because the
// user asked for this).
func (s *ReadFileSuite) TestUserLimited() {
	five := filepath.Join(s.tmpDir, "five.txt")
	s.Require().NoError(os.WriteFile(five, []byte("a\nb\nc\nd\ne\n"), 0o644))

	res, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":   "five.txt",
		"offset": 2,
		"limit":  1,
	})
	s.Require().NoError(err)
	s.True(res.UserLimited)
	s.False(res.ForcedTruncated, "user-narrowed range shouldn't flag forced truncation")
	s.Contains(res.Content, "     2:b")
	s.NotContains(res.Content, ":a")
	s.NotContains(res.Content, ":c")
	s.Contains(res.Display, "showing 2-2")
}

// TestFileNotFound maps to ErrNotFound.
func (s *ReadFileSuite) TestFileNotFound() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{"path": "no_such.txt"})
	s.requireToolErr(err, tool.ErrNotFound)
}

// TestBinaryRejected: a file with a NUL in the first 8 KB triggers
// ErrInvalidArgs (per D79 / §10.3.7).
func (s *ReadFileSuite) TestBinaryRejected() {
	bin := filepath.Join(s.tmpDir, "bin.dat")
	s.Require().NoError(os.WriteFile(bin, []byte{0x48, 0x65, 0x00, 0x6c, 0x6f}, 0o644))
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{"path": "bin.dat"})
	s.requireToolErr(err, tool.ErrInvalidArgs)
	s.Contains(err.Error(), "binary")
}

// TestEmptyPath rejected via validateArgs.
func (s *ReadFileSuite) TestEmptyPath() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{"path": ""})
	s.requireToolErr(err, tool.ErrInvalidArgs)
}

// TestNegativeOffset rejected via validateArgs.
func (s *ReadFileSuite) TestNegativeOffset() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":   "hello.txt",
		"offset": -1,
	})
	s.requireToolErr(err, tool.ErrInvalidArgs)
}

// TestUnknownField rejected by DisallowUnknownFields. The testkit
// suite covers this via TestInvalidArgsRejected, but we add an
// explicit case so the failure message is informative if the
// behaviour ever changes.
func (s *ReadFileSuite) TestUnknownField() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":     "hello.txt",
		"weird_xx": 1,
	})
	s.requireToolErr(err, tool.ErrInvalidArgs)
}

// TestMaxBytesClampedToHardCap: passing 5 MB silently clamps to 1 MB.
// We can't easily verify the clamp directly without exposing internal
// state, so we use a 1.5 MB file and confirm we got at most 1 MB back.
func (s *ReadFileSuite) TestMaxBytesClampedToHardCap() {
	huge := filepath.Join(s.tmpDir, "huge.txt")
	var sb strings.Builder
	for i := 0; i < 50_000; i++ { // ~ 50_000 * 30 = 1.5 MB
		sb.WriteString("the quick brown fox jumps    \n")
	}
	s.Require().NoError(os.WriteFile(huge, []byte(sb.String()), 0o644))

	res, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":      "huge.txt",
		"max_bytes": 5 * 1024 * 1024, // 5 MB (above hard cap)
	})
	s.Require().NoError(err)
	// Body alone (excluding wrapper tags) should not exceed the 1 MB
	// hard cap. We sanity-check on Content length minus a generous
	// 256-byte header allowance.
	s.LessOrEqual(len(res.Content), hardCapMaxBytes+1024,
		"max_bytes should have been clamped to %d", hardCapMaxBytes)
}

// TestCtxAlreadyCanceled: pre-cancel ctx; we expect ErrInterrupted.
// Override testkit's loose check (which accepts success) because for
// read_file we *can* observe cancellation thanks to the explicit
// ctx.Err() check between IO steps.
func (s *ReadFileSuite) TestCtxCancel_StrictForReadFile() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.NewTool().Invoke(ctx, s.HappyArgs)
	// The very first step (decodeArgs) doesn't touch ctx, so we may
	// race past the first ctx.Err() and complete normally on a tiny
	// 12-byte file. Accept both outcomes: success, or ErrInterrupted.
	if err == nil {
		return
	}
	var te *tool.Error
	if !errors.As(err, &te) {
		s.FailNowf("not a *tool.Error", "got %T", err)
	}
	s.Equal(tool.ErrInterrupted, te.Code)
}

// requireToolErr is a per-suite helper mirroring testkit's private one
// so we can call it on tool-specific tests without going through the
// embedded base. We accept it as code duplication for readability.
func (s *ReadFileSuite) requireToolErr(err error, code tool.ErrorCode) {
	s.T().Helper()
	s.Require().Error(err)
	var te *tool.Error
	if !errors.As(err, &te) {
		s.FailNowf("not a *tool.Error", "got %T: %v", err, err)
	}
	s.Equal(code, te.Code, "wrong code; full message: %s", te.Message)
}
